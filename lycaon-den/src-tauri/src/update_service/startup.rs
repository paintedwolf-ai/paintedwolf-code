//! Supervision releases engine admission after a failed startup update attempt.
use super::{transaction, UpdateError, UpdateErrorCode, UpdateService};
use tauri::{AppHandle, Manager};

async fn supervise(
    attempt: impl std::future::Future<Output = Result<bool, UpdateError>> + Send + 'static,
) -> Result<bool, UpdateError> {
    tauri::async_runtime::spawn(attempt)
        .await
        .map_err(|error| UpdateError::new(UpdateErrorCode::Interrupted, error))?
}

pub fn start(app: AppHandle) {
    tauri::async_runtime::spawn(async move {
        let startup = app.clone();
        let result =
            supervise(async move { crate::app_exit::install_update(&startup, None, true).await })
                .await;
        match result {
            // A committed restart must not admit an engine into the exiting process.
            Ok(true) => return,
            Err(error) => transaction::resume(&app, Some(error)).await,
            Ok(false) => {}
        }
        app.state::<UpdateService>().finish_startup(&app).await;
        super::scheduler::start_update_scheduler(app);
    });
}

#[cfg(test)]
mod tests {
    use super::*;
    #[tokio::test]
    async fn panicking_attempt_returns_a_recoverable_diagnostic() {
        let error = supervise(async { panic!("injected startup failure") })
            .await
            .unwrap_err();
        assert_eq!(error.code, UpdateErrorCode::Interrupted);
        assert!(error.detail.unwrap().contains("injected startup failure"));
    }
    #[tokio::test]
    async fn committed_activation_remains_distinct_from_engine_admission() {
        assert!(supervise(async { Ok(true) }).await.unwrap());
        assert!(!supervise(async { Ok(false) }).await.unwrap());
        assert_eq!(
            supervise(async { Err(UpdateErrorCode::Cancelled.into()) })
                .await
                .unwrap_err()
                .code,
            UpdateErrorCode::Cancelled
        );
    }
}
