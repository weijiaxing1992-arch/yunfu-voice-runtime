//! 父进程继承的专用 PCM 数据通道；固定二进制报文不进入 JSON 控制队列。
//! 一次最多100ms音频，只有一个待写回复；对端不读取时暂停此通道而非阻塞RTP。
use super::pcm_turn::{TurnState, TurnStatus};
use anyhow::{ensure, Context, Result};
use mio::{net::UnixDatagram, Interest, Registry, Token};
use std::{io, os::fd::FromRawFd, time::Instant};

pub const TOKEN: Token = Token(usize::MAX);
pub const MAX_SAMPLES: usize = 800;
const HEADER: usize = 44;
const MAX_REQUEST: usize = HEADER + MAX_SAMPLES * 2;
pub const REPLY_SIZE: usize = 96;

/// 解析线程不创建PCM Vec；仅有效count范围可被入队。错误头尽可能回显完整身份字段。
pub struct Push {
    pub request_id: u64,
    pub session: u64,
    pub turn_id: u64,
    pub offset: u64,
    pub count: usize,
    pub samples: [i16; MAX_SAMPLES],
    pub valid: bool,
}

fn number(bytes: &[u8], at: usize) -> u64 {
    bytes
        .get(at..at + 8)
        .map_or(0, |part| u64::from_le_bytes(part.try_into().unwrap()))
}

impl Push {
    pub fn parse(bytes: &[u8]) -> Self {
        let mut input = Self {
            request_id: number(bytes, 8),
            session: number(bytes, 16),
            turn_id: number(bytes, 24),
            offset: number(bytes, 32),
            count: 0,
            samples: [0; MAX_SAMPLES],
            valid: false,
        };
        if bytes.len() < HEADER
            || &bytes[..4] != b"RSP1"
            || bytes[4..8] != [0; 4]
            || bytes[42..44] != [0; 2]
            || input.request_id == 0
            || input.session == 0
            || input.turn_id == 0
        {
            return input;
        }
        let count = u16::from_le_bytes(bytes[40..42].try_into().unwrap()) as usize;
        if !(160..=MAX_SAMPLES).contains(&count)
            || count % 160 != 0
            || bytes.len() != HEADER + count * 2
        {
            return input;
        }
        for (sample, pair) in input.samples[..count]
            .iter_mut()
            .zip(bytes[HEADER..].chunks_exact(2))
        {
            *sample = i16::from_le_bytes(pair.try_into().unwrap());
        }
        input.count = count;
        input.valid = true;
        input
    }
}

/// 数值来自同一worker一次快照；失败不把requested turn冒充当前turn。
pub fn reply(input: &Push, code: u16, status: Option<&TurnStatus>) -> [u8; REPLY_SIZE] {
    let mut bytes = [0; REPLY_SIZE];
    bytes[..4].copy_from_slice(b"RSR1");
    bytes[4..6].copy_from_slice(&code.to_le_bytes());
    for (at, value) in [
        (8, input.request_id),
        (16, input.session),
        (24, input.turn_id),
    ] {
        bytes[at..at + 8].copy_from_slice(&value.to_le_bytes());
    }
    if let Some(status) = status {
        for (at, value) in [
            (32, status.turn_id),
            (40, status.accepted_samples),
            (48, status.sent_samples),
            (56, status.queued_samples),
            (64, status.discarded_samples),
            (72, status.oldest_age_ms),
        ] {
            bytes[at..at + 8].copy_from_slice(&value.to_le_bytes());
        }
        bytes[80] = match status.state {
            TurnState::Idle => 0,
            TurnState::Buffering => 1,
            TurnState::Playing => 2,
            TurnState::Draining => 3,
            TurnState::Stopping => 4,
            TurnState::Completed => 5,
            TurnState::Stopped => 6,
            TurnState::Failed => 7,
        };
    }
    bytes
}

pub struct Lane {
    socket: UnixDatagram,
    /// 最多保留一次已执行请求的回复；尚未回出之前不会读取下一批PCM。
    pending: Option<([u8; REPLY_SIZE], Instant)>,
    pub readable: bool,
}

impl Lane {
    /// 仅接管父进程约定的FD3；先验证Unix数据报类型，绝不接管stdin/stdout或普通文件。
    pub fn from_environment(registry: &Registry) -> Result<Option<Self>> {
        let Some(value) = std::env::var_os("RUSTSWITCH_PCM_FD") else {
            return Ok(None);
        };
        ensure!(value == "3", "PCM descriptor must be inherited FD3");
        let mut kind: libc::c_int = 0;
        let mut length = std::mem::size_of_val(&kind) as libc::socklen_t;
        ensure!(
            // SAFETY: 两个指针各自指向有效整数，长度准确；getsockopt只检查尚未接管的FD3。
            unsafe {
                libc::getsockopt(
                    3,
                    libc::SOL_SOCKET,
                    libc::SO_TYPE,
                    (&mut kind as *mut libc::c_int).cast(),
                    &mut length,
                )
            } == 0
                && kind == libc::SOCK_DGRAM,
            "PCM descriptor is not a datagram socket"
        );
        // SAFETY: sockaddr_storage是无引用的C地址存储，全零有效；随后由getsockname填写实际地址。
        let mut address: libc::sockaddr_storage = unsafe { std::mem::zeroed() };
        let mut address_length = std::mem::size_of_val(&address) as libc::socklen_t;
        // SAFETY: 地址缓冲及其长度变量均有效，缓冲容量足够容纳任意受支持socket地址。
        let address_result = unsafe {
            libc::getsockname(
                3,
                (&mut address as *mut libc::sockaddr_storage).cast(),
                &mut address_length,
            )
        };
        ensure!(
            address_result == 0 && i32::from(address.ss_family) == libc::AF_UNIX,
            "PCM descriptor is not Unix domain"
        );
        // SAFETY: FD3由父进程专门交给本模块，之前没有Rust所有者，下面只接管一次并由socket析构关闭。
        let standard = unsafe { std::os::unix::net::UnixDatagram::from_raw_fd(3) };
        standard
            .local_addr()
            .context("PCM descriptor is not Unix domain")?;
        standard
            .peer_addr()
            .context("PCM descriptor must have an inherited peer")?;
        standard.set_nonblocking(true)?;
        let mut socket = UnixDatagram::from_std(standard);
        registry.register(&mut socket, TOKEN, Interest::READABLE)?;
        Ok(Some(Self {
            socket,
            pending: None,
            readable: false,
        }))
    }

    pub fn has_pending(&self) -> bool {
        self.pending.is_some()
    }

    /// 比合法报文多读一个字节，超长数据报即使被内核截断也不会变成合法前缀。
    pub fn receive(&mut self) -> io::Result<Option<Push>> {
        if self.pending.is_some() || !self.readable {
            return Ok(None);
        }
        let mut bytes = [0; MAX_REQUEST + 1];
        match self.socket.recv(&mut bytes) {
            Ok(count) => Ok(Some(Push::parse(&bytes[..count]))),
            Err(e) if e.kind() == io::ErrorKind::WouldBlock => {
                self.readable = false;
                Ok(None)
            }
            Err(e) if e.kind() == io::ErrorKind::Interrupted => Ok(None),
            Err(e) => Err(e),
        }
    }

    pub fn respond(&mut self, bytes: [u8; REPLY_SIZE]) -> io::Result<()> {
        debug_assert!(self.pending.is_none());
        self.pending = Some((bytes, Instant::now()));
        self.flush()
    }

    /// 回复有一秒总期限，超过后让调用方关闭数据通道；控制面与已建立RTP独立存活。
    pub fn flush(&mut self) -> io::Result<()> {
        let Some((bytes, since)) = self.pending.as_ref() else {
            return Ok(());
        };
        if since.elapsed() >= std::time::Duration::from_secs(1) {
            return Err(io::Error::new(
                io::ErrorKind::TimedOut,
                "PCM reply deadline exceeded",
            ));
        }
        match self.socket.send(bytes) {
            Ok(REPLY_SIZE) => {
                self.pending = None;
                Ok(())
            }
            Ok(_) => Err(io::Error::new(
                io::ErrorKind::WriteZero,
                "partial PCM datagram reply",
            )),
            Err(e)
                if matches!(
                    e.kind(),
                    io::ErrorKind::WouldBlock | io::ErrorKind::Interrupted
                ) || e.raw_os_error() == Some(libc::ENOBUFS) =>
            {
                // macOS的Unix数据报接收队列满返回ENOBUFS；该回复未送出，沿用同一待写槽和总期限。
                // 不能立即隔离短暂慢读，也不能重新执行已受理的PCM批次。
                Ok(())
            }
            Err(e) => Err(e),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn binary_length_reserved_and_sample_endian_are_checked() {
        let mut bytes = vec![0; HEADER + 320];
        bytes[..4].copy_from_slice(b"RSP1");
        for at in [8, 16, 24] {
            bytes[at] = 1;
        }
        bytes[40..42].copy_from_slice(&160u16.to_le_bytes());
        bytes[44..46].copy_from_slice(&(-32768i16).to_le_bytes());
        let input = Push::parse(&bytes);
        assert!(input.valid);
        assert_eq!(input.samples[0], -32768);
        bytes.push(0);
        assert!(!Push::parse(&bytes).valid);
        bytes.pop();
        bytes[4] = 1;
        assert!(!Push::parse(&bytes).valid);
        assert!(!Push::parse(&[]).valid);
    }
}
