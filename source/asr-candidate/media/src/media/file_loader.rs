//! 文件 I/O 与 WAV 解码只在每 worker 唯一的有界加载线程执行，媒体循环从不等待磁盘。
//! 目录 fd 在启动时固定；逐级 openat + O_NOFOLLOW 避免路径检查与实际打开之间的符号链接竞态。
use super::wav::{decode_wav, MAX_WAV_BYTES};
use anyhow::{ensure, Context, Result};
use crossbeam_channel::{bounded, Receiver, Sender};
use mio::Waker;
use std::{
    collections::VecDeque,
    ffi::CString,
    fs::{File, Metadata, OpenOptions},
    io::{self, Read},
    os::{
        fd::{AsRawFd, FromRawFd},
        unix::fs::{MetadataExt, OpenOptionsExt},
    },
    path::Path,
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc,
    },
    time::{Duration, Instant},
};

/// 加载等待不超过两秒；这是媒体任务交付期限，不把内核可能阻塞的文件读取移回媒体线程。
pub const LOAD_TIMEOUT: Duration = Duration::from_secs(2);
/// 队列、结果和缓存均使用固定预算；不存在每通话启动加载线程的路径。
const LOAD_QUEUE: usize = 16;
const CACHE_ENTRIES: usize = 8;
const CACHE_BYTES: usize = 4 * 1024 * 1024;

/// 仅包含目录内相对文件名；不允许 URL、空组件、父目录或平台歧义分隔符。
pub(super) fn validate_path(path: &str) -> Result<()> {
    ensure!(
        !path.is_empty() && path.len() <= 240 && !path.starts_with('/'),
        "invalid relative WAV path"
    );
    ensure!(
        !path
            .chars()
            .any(|c| c.is_control() || c == '\\' || c == ':'),
        "invalid WAV path character"
    );
    let parts: Vec<_> = path.split('/').collect();
    ensure!(
        parts.len() <= 8
            && parts
                .iter()
                .all(|p| !p.is_empty() && *p != "." && *p != ".." && p.len() <= 128),
        "invalid WAV path components"
    );
    ensure!(
        path.to_ascii_lowercase().ends_with(".wav"),
        "file playback requires .wav"
    );
    Ok(())
}

/// 一份任务拥有独立取消位；代际 token 与编号同时校验，不能让迟到结果作用于复用后的会话。
pub(super) struct LoadJob {
    pub slot: usize,
    pub token: usize,
    pub playback_id: u64,
    pub path: String,
    pub cancelled: Arc<AtomicBool>,
    pub deadline: Instant,
}
/// 仅返回不可变有界 PCM 与错误文字，不把开放文件句柄暴露给媒体线程。
pub(super) struct LoadResult {
    pub slot: usize,
    pub token: usize,
    pub playback_id: u64,
    pub result: std::result::Result<Arc<[i16]>, String>,
}
/// 加载器句柄不 join 可能陷入底层文件系统的读取；worker 退出时进程统一回收线程与 fd。
pub(super) struct FileLoader {
    jobs: Sender<LoadJob>,
    pub results: Receiver<LoadResult>,
    stopping: Arc<AtomicBool>,
}
impl FileLoader {
    /// 根目录只在媒体服务启动时打开一次；空根由调用方禁用此对象。
    pub fn start(root: &str, wake: Arc<Waker>) -> Result<Self> {
        ensure!(
            Path::new(root).is_absolute() && root != "/" && root.len() <= 4096,
            "invalid playback root"
        );
        let directory = OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_DIRECTORY | libc::O_NOFOLLOW | libc::O_NONBLOCK | libc::O_CLOEXEC)
            .open(root)
            .context("cannot open playback root directory")?;
        ensure!(
            directory.metadata()?.is_dir(),
            "playback root is not a directory"
        );
        let (jobs, input) = bounded::<LoadJob>(LOAD_QUEUE);
        let (output, results) = bounded::<LoadResult>(LOAD_QUEUE);
        let stopping = Arc::new(AtomicBool::new(false));
        let thread_stopping = Arc::clone(&stopping);
        std::thread::Builder::new()
            .name("wav-loader".into())
            .spawn(move || {
                let mut cache = Cache::default();
                while !thread_stopping.load(Ordering::Relaxed) {
                    let job = match input.recv_timeout(Duration::from_millis(100)) {
                        Ok(job) => job,
                        Err(crossbeam_channel::RecvTimeoutError::Timeout) => continue,
                        Err(crossbeam_channel::RecvTimeoutError::Disconnected) => break,
                    };
                    if expired(&job, &thread_stopping) {
                        continue;
                    }
                    let result = load(&directory, &job, &thread_stopping, &mut cache)
                        .map_err(|error| format!("file_load_failed: {error}"));
                    if expired(&job, &thread_stopping) {
                        continue;
                    }
                    let mut result = LoadResult {
                        slot: job.slot,
                        token: job.token,
                        playback_id: job.playback_id,
                        result,
                    };
                    // 满结果队列只阻塞这一个加载线程，等待有截止时间；不会阻塞媒体循环或无界堆积。
                    loop {
                        match output.send_timeout(result, Duration::from_millis(20)) {
                            Ok(()) => {
                                let _ = wake.wake();
                                break;
                            }
                            Err(crossbeam_channel::SendTimeoutError::Timeout(pending)) => {
                                if expired(&job, &thread_stopping) {
                                    break;
                                }
                                result = pending;
                            }
                            Err(crossbeam_channel::SendTimeoutError::Disconnected(_)) => return,
                        }
                    }
                }
            })?;
        Ok(Self {
            jobs,
            results,
            stopping,
        })
    }
    /// 只尝试入队；未入队就返回明确错误，不能在媒体线程等待文件或空队列槽位。
    pub fn submit(&self, job: LoadJob) -> Result<()> {
        self.jobs
            .try_send(job)
            .map_err(|_| anyhow::anyhow!("file loader queue full or unavailable"))
    }
}
impl Drop for FileLoader {
    /// 禁止交付后续结果；最后的 sender/receiver 随对象析构关闭，不等慢文件系统。
    fn drop(&mut self) {
        self.stopping.store(true, Ordering::Relaxed);
    }
}

/// 截止和取消均为任务拥有者给出的状态，不能由文件内容延长。
fn expired(job: &LoadJob, stopping: &AtomicBool) -> bool {
    job.cancelled.load(Ordering::Relaxed)
        || stopping.load(Ordering::Relaxed)
        || Instant::now() >= job.deadline
}

/// 始终以已经打开的父目录 fd 定位下一组件，即使攻击者同时替换路径也不会跟随 symlink 逃逸。
fn open_beneath(root: &File, path: &str) -> Result<File> {
    validate_path(path)?;
    let parts: Vec<_> = path.split('/').collect();
    let mut parent = root.try_clone()?;
    for (index, part) in parts.iter().enumerate() {
        let name = CString::new(*part)?;
        let mut flags = libc::O_RDONLY | libc::O_NOFOLLOW | libc::O_NONBLOCK | libc::O_CLOEXEC;
        if index + 1 < parts.len() {
            flags |= libc::O_DIRECTORY;
        }
        // SAFETY: parent 持有有效目录 fd，name 有 NUL 终止且无内部 NUL；不创建文件，无需 mode 参数。
        let fd = unsafe { libc::openat(parent.as_raw_fd(), name.as_ptr(), flags) };
        if fd < 0 {
            let error = io::Error::last_os_error();
            if error.kind() == io::ErrorKind::NotFound {
                anyhow::bail!("file_not_found");
            }
            return Err(error).context("cannot open relative WAV component");
        }
        // SAFETY: openat 返回的新 fd 所有权只转交一次，File 会在替换或退出时关闭它。
        parent = unsafe { File::from_raw_fd(fd) };
    }
    Ok(parent)
}

#[derive(Clone, PartialEq, Eq)]
/// 每次仍重新安全打开文件并读取元数据；缓存不允许绕过路径权限、inode 更换和内容变更检查。
struct Fingerprint {
    device: u64,
    inode: u64,
    size: u64,
    modified: (i64, i64),
    changed: (i64, i64),
}
impl From<&Metadata> for Fingerprint {
    fn from(m: &Metadata) -> Self {
        Self {
            device: m.dev(),
            inode: m.ino(),
            size: m.len(),
            modified: (m.mtime(), m.mtime_nsec()),
            changed: (m.ctime(), m.ctime_nsec()),
        }
    }
}
#[derive(Default)]
/// 8 项/4 MiB 的 LRU PCM 缓存；活跃播放通过 Arc 保持自己的不可变副本，缓存淘汰不打断播放。
struct Cache {
    entries: VecDeque<(Fingerprint, Arc<[i16]>)>,
    bytes: usize,
}
impl Cache {
    /// 命中项移到队尾；不复制 PCM，只增加有界活跃任务拥有的引用。
    fn get(&mut self, key: &Fingerprint) -> Option<Arc<[i16]>> {
        let index = self.entries.iter().position(|(saved, _)| saved == key)?;
        let entry = self.entries.remove(index)?;
        let pcm = Arc::clone(&entry.1);
        self.entries.push_back(entry);
        Some(pcm)
    }
    /// 缓存预算只计算 PCM 字节；活跃任务另由最多 64 个交互任务的硬上限约束。
    fn insert(&mut self, key: Fingerprint, pcm: Arc<[i16]>) {
        let bytes = std::mem::size_of_val(pcm.as_ref());
        while self.entries.len() >= CACHE_ENTRIES || self.bytes + bytes > CACHE_BYTES {
            if let Some((_, previous)) = self.entries.pop_front() {
                self.bytes -= std::mem::size_of_val(previous.as_ref());
            } else {
                break;
            }
        }
        self.bytes += bytes;
        self.entries.push_back((key, pcm));
    }
}

/// 以固定缓冲逐块读取；FIFO/设备等在读取前拒绝，文件变化和超时都不能留下可播放的半截结果。
fn load(
    root: &File,
    job: &LoadJob,
    stopping: &AtomicBool,
    cache: &mut Cache,
) -> Result<Arc<[i16]>> {
    ensure!(!expired(job, stopping), "file load cancelled or timed out");
    let mut file = open_beneath(root, &job.path)?;
    let metadata = file.metadata()?;
    ensure!(
        metadata.is_file() && metadata.mode() & 0o444 != 0,
        "WAV must be a readable regular file"
    );
    ensure!(
        metadata.nlink() == 1,
        "hard-linked WAV files are not accepted"
    );
    ensure!(
        (12..=MAX_WAV_BYTES as u64).contains(&metadata.len()),
        "WAV file size exceeds limit or is too short"
    );
    let key = Fingerprint::from(&metadata);
    ensure!(!expired(job, stopping), "file load cancelled or timed out");
    if let Some(pcm) = cache.get(&key) {
        return Ok(pcm);
    }
    let mut bytes = Vec::with_capacity(metadata.len() as usize);
    let mut buffer = [0; 65536];
    loop {
        ensure!(!expired(job, stopping), "file load cancelled or timed out");
        let count = match file.read(&mut buffer) {
            Ok(n) => n,
            Err(error) if error.kind() == io::ErrorKind::Interrupted => continue,
            Err(error) => return Err(error.into()),
        };
        if count == 0 {
            break;
        }
        ensure!(
            bytes.len() + count <= MAX_WAV_BYTES,
            "WAV file grew beyond limit"
        );
        bytes.extend_from_slice(&buffer[..count]);
    }
    ensure!(
        Fingerprint::from(&file.metadata()?) == key && bytes.len() as u64 == key.size,
        "WAV changed while loading"
    );
    let pcm: Arc<[i16]> = decode_wav(&bytes)?.into();
    ensure!(!expired(job, stopping), "file load cancelled or timed out");
    cache.insert(key, Arc::clone(&pcm));
    Ok(pcm)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        fs,
        os::unix::fs::{symlink, PermissionsExt},
        path::PathBuf,
        sync::atomic::AtomicU64,
    };

    /// 每项测试只删除自己创建的目录，外部目标也位于本夹具内，避免碰部署文件。
    struct Directory(PathBuf);
    impl Directory {
        fn new() -> Self {
            static NEXT: AtomicU64 = AtomicU64::new(0);
            let path = std::env::temp_dir().join(format!(
                "rustswitch-wav-{}-{}",
                std::process::id(),
                NEXT.fetch_add(1, Ordering::Relaxed)
            ));
            fs::create_dir(&path).unwrap();
            Self(path)
        }
    }
    impl Drop for Directory {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.0);
        }
    }

    /// 独立构造一个完整 PCM16 WAVE，和解析器测试不共享生成器。
    fn wav(sample: i16) -> Vec<u8> {
        let mut bytes = b"RIFF".to_vec();
        bytes.extend_from_slice(&38u32.to_le_bytes());
        bytes.extend_from_slice(b"WAVEfmt ");
        bytes.extend_from_slice(&16u32.to_le_bytes());
        for value in [1u16, 1] {
            bytes.extend_from_slice(&value.to_le_bytes());
        }
        bytes.extend_from_slice(&8000u32.to_le_bytes());
        bytes.extend_from_slice(&16000u32.to_le_bytes());
        bytes.extend_from_slice(&2u16.to_le_bytes());
        bytes.extend_from_slice(&16u16.to_le_bytes());
        bytes.extend_from_slice(b"data");
        bytes.extend_from_slice(&2u32.to_le_bytes());
        bytes.extend_from_slice(&sample.to_le_bytes());
        bytes
    }
    /// 固定同代际任务；测试只更改明确的路径、取消位或截止时间。
    fn job(path: &str) -> LoadJob {
        LoadJob {
            slot: 0,
            token: 5,
            playback_id: 1,
            path: path.into(),
            cancelled: Arc::new(AtomicBool::new(false)),
            deadline: Instant::now() + LOAD_TIMEOUT,
        }
    }

    #[test]
    /// 路径形状在进入加载队列前即可拒绝，不能用平台特殊分隔符或百分号 URL 逃出根。
    fn relative_paths_have_one_unambiguous_bounded_namespace() {
        for path in ["a.wav", "menu/欢迎.WAV"] {
            assert!(validate_path(path).is_ok());
        }
        for path in [
            "",
            "/a.wav",
            "../a.wav",
            "a/../b.wav",
            "a//b.wav",
            "a/./b.wav",
            "a\\b.wav",
            "https:a.wav",
            "a\n.wav",
            "a.wav/",
            "a.mp3",
            "a/b/c/d/e/f/g/h/i.wav",
        ] {
            assert!(validate_path(path).is_err(), "{path:?}");
        }
        assert!(validate_path(&format!("{}.wav", "a".repeat(129))).is_err());
    }

    #[test]
    /// 最终 symlink、中间目录 symlink、硬链接、目录及无读权限文件都在读取音频前拒绝。
    fn file_boundaries_reject_links_directories_and_permissions() {
        let d = Directory::new();
        fs::create_dir(d.0.join("root")).unwrap();
        fs::create_dir(d.0.join("outside")).unwrap();
        fs::write(d.0.join("outside/secret.wav"), wav(999)).unwrap();
        symlink(d.0.join("outside/secret.wav"), d.0.join("root/link.wav")).unwrap();
        symlink(d.0.join("outside"), d.0.join("root/dir")).unwrap();
        fs::hard_link(d.0.join("outside/secret.wav"), d.0.join("root/hard.wav")).unwrap();
        fs::create_dir(d.0.join("root/directory.wav")).unwrap();
        fs::write(d.0.join("root/locked.wav"), wav(123)).unwrap();
        fs::set_permissions(d.0.join("root/locked.wav"), fs::Permissions::from_mode(0o0)).unwrap();
        let root = File::open(d.0.join("root")).unwrap();
        let stopped = AtomicBool::new(false);
        let mut cache = Cache::default();
        for path in [
            "link.wav",
            "dir/secret.wav",
            "hard.wav",
            "directory.wav",
            "locked.wav",
        ] {
            assert!(
                load(&root, &job(path), &stopped, &mut cache).is_err(),
                "{path}"
            );
        }
        assert_eq!(
            load(&root, &job("missing.wav"), &stopped, &mut cache)
                .unwrap_err()
                .to_string(),
            "file_not_found"
        );
    }

    #[test]
    /// FIFO 不等待 writer，避免错误部署文件把唯一加载线程卡死在 open。
    fn fifo_is_rejected_without_blocking() {
        let d = Directory::new();
        let name = CString::new(d.0.join("pipe.wav").to_str().unwrap()).unwrap();
        // SAFETY: name 是本夹具目录内的 NUL 终止新路径，mode 仅设置当前测试 FIFO 权限。
        assert_eq!(unsafe { libc::mkfifo(name.as_ptr(), 0o600) }, 0);
        let root = File::open(&d.0).unwrap();
        let now = Instant::now();
        assert!(load(
            &root,
            &job("pipe.wav"),
            &AtomicBool::new(false),
            &mut Cache::default()
        )
        .is_err());
        assert!(now.elapsed() < Duration::from_secs(1));
    }

    #[test]
    /// 安全打开返回的 fd 固定 inode；最终组件反复原子换成外部 symlink 也不读取外部内容。
    fn atomic_symlink_replacement_cannot_escape_open_directory() {
        let d = Directory::new();
        let root_path = d.0.join("root");
        fs::create_dir(&root_path).unwrap();
        let outside = d.0.join("outside.wav");
        fs::write(&outside, wav(999)).unwrap();
        fs::write(root_path.join("tone.wav"), wav(123)).unwrap();
        let thread_root = root_path.clone();
        let writer = std::thread::spawn(move || {
            for _ in 0..100 {
                symlink(&outside, thread_root.join("swap")).unwrap();
                fs::rename(thread_root.join("swap"), thread_root.join("tone.wav")).unwrap();
                fs::write(thread_root.join("next"), wav(123)).unwrap();
                fs::rename(thread_root.join("next"), thread_root.join("tone.wav")).unwrap();
            }
        });
        let root = File::open(&root_path).unwrap();
        for _ in 0..300 {
            if let Ok(mut file) = open_beneath(&root, "tone.wav") {
                let mut bytes = Vec::new();
                file.read_to_end(&mut bytes).unwrap();
                assert_eq!(bytes, wav(123));
            }
        }
        writer.join().unwrap();
    }

    #[test]
    /// 缓存命中复用 PCM，原子更新产生新 inode；取消、截止和队列满都可明确拒绝。
    fn cache_refresh_cancellation_deadline_and_queue_are_bounded() {
        let d = Directory::new();
        fs::write(d.0.join("tone.wav"), wav(123)).unwrap();
        let root = File::open(&d.0).unwrap();
        let stopping = AtomicBool::new(false);
        let mut cache = Cache::default();
        let first = load(&root, &job("tone.wav"), &stopping, &mut cache).unwrap();
        let second = load(&root, &job("tone.wav"), &stopping, &mut cache).unwrap();
        assert!(Arc::ptr_eq(&first, &second));
        fs::write(d.0.join("replace"), wav(456)).unwrap();
        fs::rename(d.0.join("replace"), d.0.join("tone.wav")).unwrap();
        let third = load(&root, &job("tone.wav"), &stopping, &mut cache).unwrap();
        assert_eq!(&*third, &[456]);
        assert!(!Arc::ptr_eq(&first, &third));
        let mut cancelled = job("tone.wav");
        cancelled.cancelled.store(true, Ordering::Relaxed);
        assert!(load(&root, &cancelled, &stopping, &mut cache).is_err());
        cancelled.cancelled.store(false, Ordering::Relaxed);
        cancelled.deadline = Instant::now();
        assert!(load(&root, &cancelled, &stopping, &mut cache).is_err());
        let (sender, _receiver) = bounded(LOAD_QUEUE);
        let (_output, results) = bounded(LOAD_QUEUE);
        let loader = FileLoader {
            jobs: sender,
            results,
            stopping: Arc::new(AtomicBool::new(false)),
        };
        for _ in 0..LOAD_QUEUE {
            loader.submit(job("tone.wav")).unwrap();
        }
        assert!(loader.submit(job("tone.wav")).is_err());
        for i in 0..32 {
            cache.insert(
                Fingerprint {
                    device: 1,
                    inode: i,
                    size: 480000,
                    modified: (0, 0),
                    changed: (0, 0),
                },
                vec![0i16; 240000].into(),
            );
        }
        assert!(cache.entries.len() <= CACHE_ENTRIES);
        assert!(cache.bytes <= CACHE_BYTES);
    }
}
