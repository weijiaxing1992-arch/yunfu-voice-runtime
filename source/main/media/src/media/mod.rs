//! 媒体 worker 内部组件；所有会话状态由单个事件循环线程拥有。
/// 控制回复的非阻塞写与整帧截止时间，仅供当前 worker 内部使用。
mod control;
/// 本地终结媒体的有界电话事件发送与共享 RTP 时间线。
mod dtmf_sender;
/// 固定有界的安全文件加载线程，文件读取不进入媒体收发热路径。
mod file_loader;
/// 有界电话事件日志和真实 G.711 提示音，不提供语音识别或完整 FreeSWITCH 播放器。
mod interaction;
/// 固定大小的接收缓冲与不同操作系统的非阻塞收包实现。
pub mod io;
pub mod jitter;
/// 音频数据专用二进制通道，不占用JSON控制请求队列。
mod pcm_transport;
/// 本地流式PCM的固定容量播放队列与轮次失效控制。
mod pcm_turn;
/// 四端口分配与释放后的隔离期管理。
pub mod ports;
mod processed;
/// 与 Go 控制面交换的 JSON 请求、响应和统计字段。
pub mod protocol;
pub mod rtcp;
/// 转发前执行的 RTP/RTCP 结构边界校验，不负责解码或加密认证。
pub mod rtp;
/// 实时处理使用逐方向RX/TX、编码包重排以及固定容量期限堆。
pub mod rtp_state;
/// 独立A腿收音的有界输出，慢ASR消费不等待媒体工作线程。
mod rx_export;
mod scheduler;
/// 严格解析有限的 8 kHz 单声道 RIFF/WAVE 格式。
mod wav;
/// 进程启动、会话命令、就绪调度与报文转发。
pub mod worker;
