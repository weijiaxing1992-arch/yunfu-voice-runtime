//! Rust 媒体核心的公共模块入口。
//! 限流、媒体转发与插件 ABI 检查分开组织；不提供 FreeSWITCH 原生模块宿主。
/// 按单调时钟限制每条媒体流的报文速率。
pub mod admission;
/// 可信 C 插件的示例 ABI 检查器；尚未接入实时转码媒体路径。
pub mod codec;
/// worker 控制协议、UDP I/O、报文校验和端口生命周期。
pub mod media;

/// 独立离线 PCM 编解码 SDK，不隐式改变 RTP 直通路径。
pub mod audio;
