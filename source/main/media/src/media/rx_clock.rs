//! 将进程内 Instant 映射到同主机的共享单调时钟，供 Go 识别 OS 缓冲区中的旧音频。
//! 只使用公开时钟 API；不读取 Instant 私有布局，也不使用可能校时跳变的 UTC。
use anyhow::{ensure, Context, Result};
use std::time::Instant;

#[cfg(target_os = "linux")]
pub const DOMAIN: u16 = 1;
#[cfg(target_os = "macos")]
pub const DOMAIN: u16 = 2;
#[cfg(not(any(target_os = "linux", target_os = "macos")))]
pub const DOMAIN: u16 = 0;

/// Rust Instant 在 Linux 使用 MONOTONIC，在 macOS 使用 UPTIME_RAW；域不能混用。
fn shared_now_ns() -> Result<u64> {
    #[cfg(target_os = "linux")]
    let clock = libc::CLOCK_MONOTONIC;
    #[cfg(target_os = "macos")]
    let clock = libc::CLOCK_UPTIME_RAW;
    #[cfg(any(target_os = "linux", target_os = "macos"))]
    {
        let mut value = libc::timespec {
            tv_sec: 0,
            tv_nsec: 0,
        };
        // SAFETY: timespec 是有效且独占的 C 缓冲区；系统调用只填写当前时钟值。
        let rc = unsafe { libc::clock_gettime(clock, &mut value) };
        ensure!(rc == 0, "RX monotonic clock unavailable");
        ensure!(
            (0..1_000_000_000).contains(&value.tv_nsec),
            "RX invalid clock nanoseconds"
        );
        u64::try_from(value.tv_sec)
            .ok()
            .and_then(|seconds| seconds.checked_mul(1_000_000_000))
            .and_then(|ns| ns.checked_add(value.tv_nsec as u64))
            .filter(|ns| *ns != 0)
            .context("RX monotonic clock out of range")
    }
    #[cfg(not(any(target_os = "linux", target_os = "macos")))]
    anyhow::bail!("RX shared monotonic clock unsupported on this platform")
}

/// 同一次订阅永久保留该映射。前置采样给出真实观察时间的下界，抢占只会提前期限。
pub(super) struct ClockAnchor {
    instant: Instant,
    lower_ns: u64,
}
impl ClockAnchor {
    pub(super) fn new() -> Result<Self> {
        let lower_ns = shared_now_ns()?;
        let instant = Instant::now();
        let upper_ns = shared_now_ns()?;
        let uncertainty = upper_ns
            .checked_sub(lower_ns)
            .context("RX clock moved backwards")?;
        // 订阅建立只进行一次有界校准；过慢时明确拒绝本次订阅，不阻塞媒体等待重试。
        ensure!(
            uncertainty <= 1_000_000,
            "RX clock calibration exceeded 1ms"
        );
        Ok(Self { instant, lower_ns })
    }

    pub(super) fn observation_lower_ns(&self, observed: Instant) -> Option<u64> {
        let value = if let Some(delta) = observed.checked_duration_since(self.instant) {
            self.lower_ns
                .checked_add(u64::try_from(delta.as_nanos()).ok()?)
        } else {
            self.lower_ns
                .checked_sub(u64::try_from(self.instant.duration_since(observed).as_nanos()).ok()?)
        }?;
        (value != 0).then_some(value)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;

    #[test]
    fn anchor_preserves_signed_offsets_and_rejects_overflow() {
        let instant = Instant::now();
        let anchor = ClockAnchor {
            instant,
            lower_ns: 1_000_000_000,
        };
        assert_eq!(
            anchor.observation_lower_ns(instant + Duration::from_millis(20)),
            Some(1_020_000_000)
        );
        assert_eq!(
            anchor.observation_lower_ns(instant - Duration::from_millis(20)),
            Some(980_000_000)
        );
        assert_eq!(
            anchor.observation_lower_ns(instant - Duration::from_secs(1)),
            None
        );
        let overflow = ClockAnchor {
            instant,
            lower_ns: u64::MAX - 1,
        };
        assert_eq!(
            overflow.observation_lower_ns(instant + Duration::from_nanos(2)),
            None
        );
    }

    #[test]
    fn actual_mapping_never_moves_observation_into_future() {
        let anchor = ClockAnchor::new().unwrap();
        let observed = Instant::now();
        let upper = shared_now_ns().unwrap();
        let mapped = anchor.observation_lower_ns(observed).unwrap();
        assert!(mapped >= anchor.lower_ns && mapped <= upper);
        assert!(matches!(DOMAIN, 1 | 2));
    }
}
