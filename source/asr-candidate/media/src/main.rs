//! 媒体进程入口：由 Go 控制面传入配置启动 worker，或单独检查可信 C 编解码插件。
//! 两种模式互斥；插件检查只验证示例 ABI，并不建立转码通话。
use anyhow::Result;
use clap::Parser;

#[derive(Parser)]
#[command(
    version,
    about = "RustSwitch Rust media worker; launched by the Go controller"
)]
// 启动参数只在进程启动时读取，不承担运行中的配置热更新。
// 此处使用普通注释，避免 clap 将说明作为 help 文本而改变既有 CLI 输出。
struct Args {
    // 控制面传入的完整 worker JSON 配置，不是配置文件路径。
    #[arg(long, conflicts_with_all = ["check_codec", "audio_capabilities", "check_audio"])]
    worker_config: Option<String>,
    // 要加载检查的本地原生动态库；其代码在当前检查进程内执行。
    #[arg(long)]
    check_codec: Option<std::path::PathBuf>,
    // 只读探测离线音频能力，缺少原生库在对应JSON条目中说明。
    #[arg(long, conflicts_with_all = ["check_codec", "check_audio"])]
    audio_capabilities: bool,
    // 执行P0真编解码自检；缺库或语义失败输出失败JSON并返回非零。
    #[arg(long, conflicts_with = "check_codec")]
    check_audio: bool,
}

/// 优先执行显式选择的插件检查，否则解析配置并把进程生命周期交给媒体 worker。
fn main() -> Result<()> {
    let args = Args::parse();
    if args.audio_capabilities {
        println!("{}", rustswitch_media::audio::capabilities());
        return Ok(());
    }
    if args.check_audio {
        return rustswitch_media::audio::check();
    }
    if let Some(path) = args.check_codec {
        return rustswitch_media::codec::check(&path);
    }
    let text = args
        .worker_config
        .ok_or_else(|| anyhow::anyhow!("--worker-config is required"))?;
    rustswitch_media::media::worker::run(serde_json::from_str(&text)?)
}
