//! 对专属控制输出实施非阻塞写和整帧截止时间；慢消费者不能把媒体退出卡在 join。
use std::{
    io,
    os::fd::{AsRawFd, BorrowedFd},
    time::{Duration, Instant},
};

/// 借用一个仍由调用方持有的控制 fd；只在本写线程的生命周期内改变非阻塞标志。
pub(super) struct ControlOutput<'fd> {
    fd: BorrowedFd<'fd>,
    original_flags: libc::c_int,
    timeout: Duration,
}

impl<'fd> ControlOutput<'fd> {
    /// 先设置 O_NONBLOCK，使后续 write 即使面对不读取的管道也能返回到截止时间检查。
    pub(super) fn new(fd: BorrowedFd<'fd>, timeout: Duration) -> io::Result<Self> {
        // SAFETY: fd 的借用保证它仍有效，F_GETFL 不接收额外指针。
        let flags = unsafe { libc::fcntl(fd.as_raw_fd(), libc::F_GETFL) };
        if flags < 0 {
            return Err(io::Error::last_os_error());
        }
        // SAFETY: F_SETFL 接受整型标志，只为有效的借用 fd 增加非阻塞位。
        if unsafe { libc::fcntl(fd.as_raw_fd(), libc::F_SETFL, flags | libc::O_NONBLOCK) } < 0 {
            return Err(io::Error::last_os_error());
        }
        Ok(Self {
            fd,
            original_flags: flags,
            timeout,
        })
    }

    /// 全部字节共用一次绝对截止时间，部分写入和 Interrupted 都不能无限延长期限。
    pub(super) fn write_frame(
        &mut self,
        mut bytes: &[u8],
        shutdown: Option<Instant>,
    ) -> io::Result<()> {
        // 事件循环退出后，所有剩余回复共享一次截止时间，不能每帧再延长一秒。
        let frame_deadline = Instant::now() + self.timeout;
        let deadline = shutdown.map_or(frame_deadline, |deadline| deadline.min(frame_deadline));
        while !bytes.is_empty() {
            if Instant::now() >= deadline {
                return Err(io::Error::new(
                    io::ErrorKind::TimedOut,
                    "control output deadline exceeded",
                ));
            }
            // SAFETY: fd 仍由调用方持有；字节片段在同步 write 返回之前有效，长度完全匹配。
            let written =
                unsafe { libc::write(self.fd.as_raw_fd(), bytes.as_ptr().cast(), bytes.len()) };
            if written > 0 {
                bytes = &bytes[written as usize..];
                continue;
            }
            if written == 0 {
                return Err(io::Error::new(
                    io::ErrorKind::WriteZero,
                    "control output wrote no bytes",
                ));
            }
            let error = io::Error::last_os_error();
            match error.kind() {
                io::ErrorKind::Interrupted => continue,
                io::ErrorKind::WouldBlock => self.wait_writable(deadline)?,
                _ => return Err(error),
            }
        }
        Ok(())
    }

    /// 等待可写时让出 CPU；poll 的等待值取剩余时间，不使用无限等待或反复短自旋。
    fn wait_writable(&self, deadline: Instant) -> io::Result<()> {
        loop {
            let remaining = deadline.saturating_duration_since(Instant::now());
            if remaining.is_zero() {
                return Err(io::Error::new(
                    io::ErrorKind::TimedOut,
                    "control output deadline exceeded",
                ));
            }
            let millis = remaining
                .as_millis()
                .saturating_add(1)
                .min(libc::c_int::MAX as u128) as libc::c_int;
            let mut descriptor = libc::pollfd {
                fd: self.fd.as_raw_fd(),
                events: libc::POLLOUT,
                revents: 0,
            };
            // SAFETY: descriptor 是有效的单元素 pollfd 存储，fd 借用有效，等待有有限上限。
            let result = unsafe { libc::poll(&mut descriptor, 1, millis) };
            if result < 0 {
                let error = io::Error::last_os_error();
                if error.kind() == io::ErrorKind::Interrupted {
                    continue;
                }
                return Err(error);
            }
            if result == 0 {
                continue;
            }
            if descriptor.revents & (libc::POLLERR | libc::POLLHUP | libc::POLLNVAL) != 0 {
                return Err(io::Error::new(
                    io::ErrorKind::BrokenPipe,
                    "control output disconnected",
                ));
            }
            if descriptor.revents & libc::POLLOUT != 0 {
                return Ok(());
            }
        }
    }
}

impl Drop for ControlOutput<'_> {
    /// 正常离开写线程时恢复原标志；进程退出仍由系统关闭 fd，不在这里关闭借用资源。
    fn drop(&mut self) {
        // SAFETY: 借用生命周期保证 fd 在析构期间仍有效，恢复构造时读取的整数标志。
        unsafe {
            libc::fcntl(self.fd.as_raw_fd(), libc::F_SETFL, self.original_flags);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        io::Read,
        os::{fd::AsFd, unix::net::UnixStream},
    };

    #[test]
    /// 持续不读取的控制端只允许占用固定时窗，恢复 fd 标志后仍由原持有者负责关闭。
    fn unread_socket_has_a_write_deadline() {
        let (output, _input) = UnixStream::pair().unwrap();
        let mut writer = ControlOutput::new(output.as_fd(), Duration::from_millis(30)).unwrap();
        let started = Instant::now();
        let error = writer
            .write_frame(&vec![0; 4 * 1024 * 1024], None)
            .unwrap_err();
        assert_eq!(error.kind(), io::ErrorKind::TimedOut);
        assert!(started.elapsed() < Duration::from_secs(1));
    }

    #[test]
    /// 正常短帧原样写出，不拼接额外字节，也不依赖有缓冲 stdout 的隐式 flush。
    fn complete_frame_preserves_bytes() {
        let (output, mut input) = UnixStream::pair().unwrap();
        let mut writer = ControlOutput::new(output.as_fd(), Duration::from_secs(1)).unwrap();
        writer
            .write_frame(b"{\"id\":1,\"ok\":true}\n", None)
            .unwrap();
        let mut received = [0; 19];
        input.read_exact(&mut received).unwrap();
        assert_eq!(&received, b"{\"id\":1,\"ok\":true}\n");
    }

    #[test]
    /// 清理窗口到期后，即使输出端可写也不继续逐帧续期，防止积压队列拖延退出。
    fn shutdown_deadline_is_shared_across_frames() {
        let (output, _input) = UnixStream::pair().unwrap();
        let mut writer = ControlOutput::new(output.as_fd(), Duration::from_secs(1)).unwrap();
        let error = writer
            .write_frame(b"{}\n", Some(Instant::now()))
            .unwrap_err();
        assert_eq!(error.kind(), io::ErrorKind::TimedOut);
    }
}
