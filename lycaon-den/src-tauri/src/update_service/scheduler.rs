use super::{check, download, emit, now, UpdateService};
use std::time::Duration;
use tauri::{AppHandle, Manager};
pub(super) fn retry_delay(failures: u32) -> u64 {
    (1800u64.saturating_mul(1u64 << failures.saturating_sub(1).min(4))).min(21600)
}
pub fn start_update_scheduler(app: AppHandle) {
    tauri::async_runtime::spawn(async move {
        let service = app.state::<UpdateService>();
        let mut delay = 30;
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
            tokio::select! { _ = tokio::time::sleep(Duration::from_secs(delay)) => {}, _ = service.wake.notified() => {} }
            let enabled = service.inner.lock().await.state.automatic_updates_enabled;
            if !enabled {
                delay = 3600;
                continue;
            }
            let generation = service
                .generation
                .load(std::sync::atomic::Ordering::Acquire);
            let _ = check::run_check(&app, true).await;
            let _ = download::automatic(&app).await;
            if !service.current(generation) {
                delay = 0;
                continue;
            }
            let failed = service.inner.lock().await.state.last_error.is_some();
            if failed {
                failures += 1;
                delay = retry_delay(failures) + rand::random::<u8>() as u64;
            } else {
                failures = 0;
                delay = 21600;
            }
        }
    });
}
