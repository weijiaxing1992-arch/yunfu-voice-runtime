//! 固定接收缓冲；Linux 对同一 socket 批量收包，不等待凑满批次。
//! 非 Linux 使用逐包回退路径；这里保留原始数据报边界，不执行 RTP 解析。
use mio::net::UdpSocket;
use std::{
    io,
    net::{Ipv4Addr, SocketAddr, SocketAddrV4},
};

/// 单包缓冲容量；worker 会保守拒绝达到此长度的包，兼顾回退平台的截断检测。
// 支持L16 48k双声道20ms及Opus高码率120ms；每worker复用一个批次，非每呼叫预分配。
pub const MAX_PACKET: usize = 8192;
/// 一次接收最多处理的同 socket 报文数，不代表实际每批总能收到八个包。
pub const BATCH_SIZE: usize = 8;

/// 一次接收的缓冲槽位，内容只在下一次接收覆盖之前有效。
pub struct Packet {
    /// 可重复使用的报文字节存储；有效可读长度还需与 len 和容量共同检查。
    pub bytes: [u8; MAX_PACKET],
    /// 数据报长度；Linux 的 MSG_TRUNC 可返回大于缓冲容量的原始长度。
    pub len: usize,
    /// 内核报告的源地址，供 worker 校验协商对端。
    pub source: SocketAddr,
    /// Linux socket 累计溢出计数；缺失表示此包没有携带该辅助信息。
    pub kernel_drops: Option<u32>,
}
impl Default for Packet {
    /// 初始化空槽位；未收到数据时的占位地址不能被当作合法对端。
    fn default() -> Self {
        Self {
            bytes: [0; MAX_PACKET],
            len: 0,
            source: SocketAddrV4::new(Ipv4Addr::UNSPECIFIED, 0).into(),
            kernel_drops: None,
        }
    }
}

/// Linux C 描述符固定在同一个堆分配内；移动 ReceiveBatch 不会移动指针指向的元数据。
#[cfg(target_os = "linux")]
struct LinuxReceiveState {
    /// 内核连续读取的 mmsghdr 数组；每次重置返回长度和可写容量。
    messages: [libc::mmsghdr; BATCH_SIZE],
    /// IPv4 源地址存储；只在内核返回足够长且地址族正确时读取。
    addresses: [libc::sockaddr_in; BATCH_SIZE],
    /// usize 对齐的辅助区；有效范围由每次调用返回的 msg_controllen 决定。
    controls: [[usize; 4]; BATCH_SIZE],
    /// 指向实际 Packet 字节的向量；允许安全调用者替换 packets Box，故每次重绑基址。
    vectors: [libc::iovec; BATCH_SIZE],
}

#[cfg(target_os = "linux")]
impl LinuxReceiveState {
    /// 分配后再建立内部指针；Box 所有者移动不会破坏地址，且不把裸指针暴露给调用者。
    fn new() -> Box<Self> {
        // SAFETY: 这些 libc 描述符、整数和裸指针均允许全零初值；同步内核调用前
        // 下方及 prepare 会填入所有将使用的指针和长度，绝不解引用初始化中的空指针。
        let mut state: Box<Self> = Box::new(unsafe { std::mem::zeroed() });
        for index in 0..BATCH_SIZE {
            state.messages[index].msg_hdr.msg_name =
                std::ptr::from_mut(&mut state.addresses[index]).cast();
            state.messages[index].msg_hdr.msg_iov = &mut state.vectors[index];
            state.messages[index].msg_hdr.msg_iovlen = 1;
            state.messages[index].msg_hdr.msg_control = state.controls[index].as_mut_ptr().cast();
            state.vectors[index].iov_len = MAX_PACKET;
        }
        state
    }

    /// 仅准备当前额度允许的槽位；保留内核返回边界外的旧字节，不把它们当作新辅助数据。
    fn prepare(&mut self, packets: &mut [Packet; BATCH_SIZE], limit: usize) {
        for (index, packet) in packets.iter_mut().take(limit).enumerate() {
            self.vectors[index].iov_base = packet.bytes.as_mut_ptr().cast();
            let message = &mut self.messages[index];
            message.msg_hdr.msg_namelen =
                std::mem::size_of::<libc::sockaddr_in>() as libc::socklen_t;
            // glibc 使用 usize，musl 使用 socklen_t；固定辅助区仍显式检查窄化边界。
            #[allow(clippy::useless_conversion, reason = "glibc同宽但musl需要检查窄化")]
            let control_length = std::mem::size_of_val(&self.controls[index])
                .try_into()
                .expect("固定辅助缓冲长度必须能由目标平台 msghdr 表示");
            message.msg_hdr.msg_controllen = control_length;
            message.msg_hdr.msg_flags = 0;
            message.msg_len = 0;
        }
    }
}

/// worker 循环持有的一组接收槽位；批次之间复用缓冲，避免正常收包路径分配内存。
pub struct ReceiveBatch {
    /// 固定堆存储，不为每个就绪 socket 创建一组新数据缓冲。
    pub packets: Box<[Packet; BATCH_SIZE]>,
    /// 累计接收调用次数，包括返回 WouldBlock 的调用，用于判断批量化实际收益。
    pub syscalls: u64,
    /// 仅当已经明确观察到当前 socket 无包可读时置真。
    pub drained: bool,
    /// 仅 Linux 需要复用的 C 元数据；独占固定堆地址，不随单个会话重复分配。
    #[cfg(target_os = "linux")]
    linux: Box<LinuxReceiveState>,
}
impl Default for ReceiveBatch {
    /// 进程事件循环启动时一次性分配整组报文存储。
    fn default() -> Self {
        Self {
            packets: Box::new(std::array::from_fn(|_| Packet::default())),
            syscalls: 0,
            drained: false,
            #[cfg(target_os = "linux")]
            linux: LinuxReceiveState::new(),
        }
    }
}
impl ReceiveBatch {
    /// 常规调用保持完整批次；工作量保护可通过 receive_limited 缩小本次内核读取上限。
    pub fn receive(&mut self, socket: &UdpSocket) -> io::Result<usize> {
        self.receive_limited(socket, BATCH_SIZE)
    }

    /// 可移植回退：逐包读取到批次上限或无数据，保留本轮已成功接收的包。
    /// 只有实际遇到 WouldBlock 才宣告排空，避免边缘触发就绪事件被提前丢失。
    #[cfg(not(target_os = "linux"))]
    pub fn receive_limited(&mut self, socket: &UdpSocket, limit: usize) -> io::Result<usize> {
        assert!((1..=BATCH_SIZE).contains(&limit));
        self.drained = false;
        for index in 0..limit {
            self.syscalls += 1;
            let packet = &mut self.packets[index];
            match socket.recv_from(&mut packet.bytes) {
                Ok((len, source)) => {
                    packet.len = len;
                    packet.source = source;
                    packet.kernel_drops = None;
                }
                Err(error) if index > 0 && error.kind() == io::ErrorKind::WouldBlock => {
                    self.drained = true;
                    return Ok(index);
                }
                Err(error) => return if index > 0 { Ok(index) } else { Err(error) },
            }
        }
        Ok(limit)
    }

    /// Linux 非阻塞批量接收同一 fd 的数据报，同时读取 socket 溢出辅助计数。
    /// 短批次本身不证明已排空；调用方仍需继续调度到明确观察到 WouldBlock。
    #[cfg(target_os = "linux")]
    pub fn receive_limited(&mut self, socket: &UdpSocket, limit: usize) -> io::Result<usize> {
        assert!((1..=BATCH_SIZE).contains(&limit));
        self.drained = false;
        use std::os::fd::AsRawFd;
        self.linux.prepare(&mut self.packets, limit);
        self.syscalls += 1;
        // SAFETY: fd 有效，固定 Box 中的描述符、地址和辅助区均存活且互不重叠；
        // prepare 已按本次 packets Box 重绑数据指针，调用期间 &mut self 禁止替换缓冲。
        // 内核最多访问 limit 个槽位；MSG_DONTWAIT 与空超时指针保证不会等待凑满批次。
        let count = unsafe {
            libc::recvmmsg(
                socket.as_raw_fd(),
                self.linux.messages.as_mut_ptr(),
                limit as u32,
                // 两个标志均为非负小常量；由 libc 签名推断 glibc 的 i32 或 musl 的 u32。
                (libc::MSG_DONTWAIT | libc::MSG_TRUNC) as _,
                std::ptr::null_mut(),
            )
        };
        if count < 0 {
            return Err(io::Error::last_os_error());
        }
        for index in 0..count as usize {
            let packet = &mut self.packets[index];
            let message = &self.linux.messages[index];
            let address = self.linux.addresses[index];
            packet.len = message.msg_len as usize;
            packet.kernel_drops = None;
            packet.source = if message.msg_hdr.msg_namelen as usize
                >= std::mem::size_of::<libc::sockaddr_in>()
                && address.sin_family == libc::AF_INET as libc::sa_family_t
            {
                SocketAddrV4::new(
                    Ipv4Addr::from(address.sin_addr.s_addr.to_ne_bytes()),
                    u16::from_be(address.sin_port),
                )
                .into()
            } else {
                SocketAddrV4::new(Ipv4Addr::UNSPECIFIED, 0).into()
            };
            // SAFETY: 安全依据：辅助缓冲按 usize 对齐并把容量交给内核；
            // CMSG 遍历依据内核返回边界，读取计数前确认类型与四字节数据长度。
            unsafe {
                let mut header = libc::CMSG_FIRSTHDR(&message.msg_hdr);
                while !header.is_null() {
                    if (*header).cmsg_level == libc::SOL_SOCKET
                        && (*header).cmsg_type == libc::SO_RXQ_OVFL
                        // CMSG_LEN(4) 仅包含固定头与四字节计数，安全适配 usize/u32 字段。
                        && (*header).cmsg_len >= libc::CMSG_LEN(4) as _
                    {
                        packet.kernel_drops = Some(std::ptr::read_unaligned(
                            libc::CMSG_DATA(header).cast::<u32>(),
                        ));
                    }
                    header = libc::CMSG_NXTHDR(&message.msg_hdr, header);
                }
            }
        }
        Ok(count as usize)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    /// 允许本地 UDP 调度存在短暂延迟，但用一秒上限避免测试无限等待。
    fn wait_receive(batch: &mut ReceiveBatch, socket: &UdpSocket) -> usize {
        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(1);
        loop {
            match batch.receive(socket) {
                Ok(count) => return count,
                Err(error)
                    if error.kind() == io::ErrorKind::WouldBlock
                        && std::time::Instant::now() < deadline =>
                {
                    std::thread::sleep(std::time::Duration::from_millis(1))
                }
                Err(error) => panic!("receive failed: {error}"),
            }
        }
    }
    #[test]
    /// 验证批次内包序、来源和长度边界，并确认超大数据报不会被误认为完整短包。
    fn batched_datagrams_keep_boundaries_and_detect_oversize() {
        let receiver = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
        receiver.set_nonblocking(true).unwrap();
        let target = receiver.local_addr().unwrap();
        let receiver = UdpSocket::from_std(receiver);
        let sender = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
        let source = sender.local_addr().unwrap();
        for value in 0..BATCH_SIZE {
            sender.send_to(&[value as u8; 172], target).unwrap();
        }
        let mut batch = ReceiveBatch::default();
        let mut received = 0;
        while received < BATCH_SIZE {
            let count = wait_receive(&mut batch, &receiver);
            for packet in &batch.packets[..count] {
                assert_eq!(packet.source, source);
                assert_eq!(packet.len, 172);
                assert_eq!(packet.bytes[..172], [received as u8; 172]);
                received += 1;
            }
        }
        assert_eq!(
            batch.receive(&receiver).unwrap_err().kind(),
            io::ErrorKind::WouldBlock
        );
        sender.send_to(&[0u8; MAX_PACKET + 1], target).unwrap();
        assert_eq!(wait_receive(&mut batch, &receiver), 1);
        assert!(batch.packets[0].len >= MAX_PACKET);
    }

    #[cfg(target_os = "linux")]
    #[test]
    /// 固定描述符跨对象移动仍有效；每轮仅重置活跃槽位，并重绑被调用者替换的Packet堆存储。
    fn linux_descriptors_reset_active_slots_and_rebind_replaced_buffers() {
        let mut batch = ReceiveBatch::default();
        batch.linux.prepare(&mut batch.packets, BATCH_SIZE);
        let original = batch.linux.as_ref() as *const LinuxReceiveState;
        let mut moved = vec![batch].pop().unwrap();
        assert_eq!(original, moved.linux.as_ref() as *const LinuxReceiveState);
        let previous = std::mem::replace(
            &mut moved.packets,
            Box::new(std::array::from_fn(|_| Packet::default())),
        );
        assert_ne!(previous.as_ptr(), moved.packets.as_ptr());
        drop(previous);
        for message in &mut moved.linux.messages {
            message.msg_hdr.msg_namelen = 0;
            message.msg_hdr.msg_controllen = 0;
            message.msg_hdr.msg_flags = libc::MSG_TRUNC;
            message.msg_len = 123;
        }
        moved.linux.prepare(&mut moved.packets, 1);
        let message = &moved.linux.messages[0];
        assert_eq!(message.msg_len, 0);
        assert_eq!(message.msg_hdr.msg_flags, 0);
        assert_eq!(message.msg_hdr.msg_namelen as usize, 16);
        assert_eq!(
            message.msg_hdr.msg_controllen as usize,
            4 * std::mem::size_of::<usize>()
        );
        assert_eq!(
            moved.linux.vectors[0].iov_base,
            moved.packets[0].bytes.as_mut_ptr().cast()
        );
        assert_eq!(
            message.msg_hdr.msg_iov,
            std::ptr::from_mut(&mut moved.linux.vectors[0])
        );
        assert_eq!(moved.linux.messages[1].msg_len, 123);
        assert_eq!(moved.linux.messages[1].msg_hdr.msg_namelen, 0);
    }

    #[cfg(target_os = "linux")]
    #[test]
    /// 同一批次轮换fd和来源，先截断再读短包；旧地址、长度、标志或丢包值不得污染新包。
    fn linux_reuse_switches_sockets_sources_and_clears_truncation() {
        let make_receiver = || {
            let socket = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
            socket.set_nonblocking(true).unwrap();
            let address = socket.local_addr().unwrap();
            (UdpSocket::from_std(socket), address)
        };
        let (first, first_address) = make_receiver();
        let (second, second_address) = make_receiver();
        let sender_a = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
        let sender_b = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
        let mut batch = ReceiveBatch::default();
        sender_a
            .send_to(&[7u8; MAX_PACKET + 1], first_address)
            .unwrap();
        assert_eq!(wait_receive(&mut batch, &first), 1);
        assert_eq!(batch.packets[0].len, MAX_PACKET + 1);
        assert_eq!(batch.packets[0].source, sender_a.local_addr().unwrap());
        assert_ne!(
            batch.linux.messages[0].msg_hdr.msg_flags & libc::MSG_TRUNC,
            0
        );
        let previous = std::mem::replace(
            &mut batch.packets,
            Box::new(std::array::from_fn(|_| Packet::default())),
        );
        assert_ne!(previous.as_ptr(), batch.packets.as_ptr());
        drop(previous);
        batch.packets[0].kernel_drops = Some(1234);
        // SAFETY: 辅助区按usize对齐且有32字节，足够容纳目标libc的CMSG头和u32。
        // 人工放置一份旧SO_RXQ_OVFL，验证随后内核返回长度0时不会读取历史数据。
        unsafe {
            let header = batch.linux.controls[0].as_mut_ptr().cast::<libc::cmsghdr>();
            assert!(
                libc::CMSG_SPACE(4) as usize <= std::mem::size_of_val(&batch.linux.controls[0])
            );
            (*header).cmsg_len = libc::CMSG_LEN(4) as _;
            (*header).cmsg_level = libc::SOL_SOCKET;
            (*header).cmsg_type = libc::SO_RXQ_OVFL;
            std::ptr::write_unaligned(libc::CMSG_DATA(header).cast::<u32>(), 1234);
        }
        sender_b.send_to(&[9u8; 172], second_address).unwrap();
        assert_eq!(wait_receive(&mut batch, &second), 1);
        assert_eq!(batch.packets[0].len, 172);
        assert_eq!(batch.packets[0].source, sender_b.local_addr().unwrap());
        assert_eq!(&batch.packets[0].bytes[..172], &[9u8; 172]);
        assert_eq!(batch.packets[0].kernel_drops, None);
        assert_eq!(batch.linux.messages[0].msg_hdr.msg_controllen, 0);
        assert_eq!(
            batch.linux.messages[0].msg_hdr.msg_flags & libc::MSG_TRUNC,
            0
        );
        assert!(!batch.drained, "短批次不能代替明确的EAGAIN");
        assert_eq!(
            batch.receive(&second).unwrap_err().kind(),
            io::ErrorKind::WouldBlock
        );
    }
}
