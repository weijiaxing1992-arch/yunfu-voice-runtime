//! 使用专属子进程和匿名管道验证控制失联边界，不连接主服务或产生媒体压力。
use rustswitch_media::media::protocol::WorkerConfig;
use std::{
    io::Write,
    process::{Command, Stdio},
    thread,
    time::{Duration, Instant},
};

#[test]
/// 控制端保留 stdout 管道却不读取时，worker 必须在有限时间内退出，而不能卡在写线程 join。
fn unread_control_output_does_not_keep_worker_alive() {
    let config = WorkerConfig {
        playback_root: String::new(),
        connect_sockets: false,
        worker_id: 0,
        bind_ip: "127.0.0.1".parse().unwrap(),
        // 不发送 allocate，本测试不会绑定这些声明端口。
        port_start: 1024,
        port_end: 1027,
        excluded_port_blocks: Vec::new(),
        max_calls: 1,
        receive_buffer_bytes: 65_536,
        port_reuse_delay_ms: 0,
        max_packets_per_second_per_leg: 100,
        allowed_remote_networks: vec!["127.0.0.0/8".parse().unwrap()],
        cpu_core: None,
    };
    let mut child = Command::new(env!("CARGO_BIN_EXE_rustswitch-media"))
        .args(["--worker-config", &serde_json::to_string(&config).unwrap()])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .unwrap();
    let mut input = child.stdin.take().unwrap();
    // 保留 stdout 的读取端，不能用关闭管道触发 BrokenPipe 来替代真正的慢消费者故障。
    let _unread_output = child.stdout.take().unwrap();
    let producer = thread::spawn(move || {
        for id in 1..=2000 {
            if writeln!(input, "{{\"id\":{id},\"op\":\"stats\"}}").is_err() {
                break;
            }
        }
    });
    let deadline = Instant::now() + Duration::from_secs(3);
    let exit = loop {
        if let Some(exit) = child.try_wait().unwrap() {
            break Some(exit);
        }
        if Instant::now() >= deadline {
            break None;
        }
        thread::sleep(Duration::from_millis(10));
    };
    // 失败复现也先回收本测试子进程，解除生产者写管道等待，再断言，避免遗留测试资源。
    if exit.is_none() {
        child.kill().unwrap();
        child.wait().unwrap();
    }
    producer.join().unwrap();
    assert!(exit.is_some(), "控制输出积压后 worker 超过三秒仍未退出");
    assert!(!exit.unwrap().success(), "控制失联不应返回成功退出码");
}
