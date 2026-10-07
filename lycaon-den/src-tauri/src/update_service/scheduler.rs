//! The background cadence: a first check after launch, six-hourly checks, bounded retries.
use super::{check, download, emit, now, UpdateService};
use std::time::Duration;
use tauri::{AppHandle, Manager};
const FIRST_CHECK_SECS: u64 = 30;
const CHECK_INTERVAL_SECS: u64 = 6 * 60 * 60;
const DISABLED_RECHECK_SECS: u64 = 60 * 60;
/// A check that stepped aside for in-flight work retries soon without counting as a failure.
const BUSY_RETRY_SECS: u64 = 5 * 60;
pub(super) fn retry_delay(failures: u32) -> u64 {
    (1800u64.saturating_mul(1u64 << failures.saturating_sub(1).min(4))).min(CHECK_INTERVAL_SECS)
}
pub fn start_update_scheduler(app: AppHandle) {
    tauri::async_runtime::spawn(async move {
        let service = app.state::<UpdateService>();
        let mut wake = service.wake.subscribe();
        let mut delay = FIRST_CHECK_SECS;
        let mut failures = 0;
        loop {
            {
                let mut inner = service.inner.lock().await;
                inner.state.next_check_at = inner
                    .state
                    .automatic_updates_enabled
                    .then_some(now() + delay);
                emit(&app, &mut inner.state);
            }
            tokio::select! { _ = tokio::time::sleep(Duration::from_secs(delay)) => {}, _ = wake.changed() => {} }
            let enabled = service.inner.lock().await.state.automatic_updates_enabled;
            if !enabled {
                delay = DISABLED_RECHECK_SECS;
                continue;
            }
            let generation = service
                .generation
                .load(std::sync::atomic::Ordering::Acquire);
            let checked = check::run_check(&app, true).await;
            let downloaded = download::automatic(&app).await;
            if !service.current(generation) {
                delay = 0;
                continue;
            }
            delay = match (checked, downloaded) {
                (Ok(check::Check::Skipped(_)), _) => BUSY_RETRY_SECS,
                (Err(_), _) | (_, Err(_)) => {
                    failures += 1;
                    retry_delay(failures) + rand::random::<u8>() as u64
                }
                _ => {
                    failures = 0;
                    CHECK_INTERVAL_SECS
                }
            };
        }
    });
}
