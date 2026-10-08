//! A healthy engine confirms startup before the previous bundle is removed.
use super::journal::{read, write, Phase};
use crate::update_service::{
    emit,
    installer::{self, executable, hash},
    staging, Failure, Installation, UpdateError, UpdateService,
};
use std::{fs, time::Duration};
use tauri::{AppHandle, Manager};

pub async fn confirm_startup(
    app: &AppHandle,
    info: &crate::sidecar::SidecarInfo,
) -> Result<(), UpdateError> {
    let Some(mut t) = read()? else {
        return Ok(());
    };
    if t.phase == Phase::Activated
        && t.candidate.version == env!("PAINTED_WOLF_VERSION")
        && info.generation != 0
        && installer::bundle().is_ok_and(|bundle| bundle == t.target)
        && hash(&executable(&t.target))? == t.next_hash
    {
        let client = reqwest::Client::builder()
            .no_proxy()
            .redirect(reqwest::redirect::Policy::none())
            .timeout(Duration::from_secs(3))
            .build()
            .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        let response = client
            .get(format!("http://127.0.0.1:{}/health", info.port))
            .send()
            .await
            .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        if !response.status().is_success() {
            return Ok(());
        }
        let bytes = response
            .bytes()
            .await
            .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        let health: serde_json::Value = serde_json::from_slice(&bytes)
            .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        if health.get("status").and_then(|value| value.as_str()) != Some("ok") {
            return Ok(());
        }
        let service = app.state::<UpdateService>();
        let mut inner = service.inner.lock().await;
        if !read()?.is_some_and(|current| current.id == t.id && current.phase == Phase::Activated) {
            return Ok(());
        }
        t.phase = Phase::StartupConfirmed;
        write(&t)?;
        let previous = t.prepared_bundle.clone();
        staging::forget_ready_for(&t.candidate.release_id)?;
        if inner.state.installation == Installation::AwaitingStartup {
            inner.state.installation = Installation::None;
            emit(app, &mut inner.state);
        }
        drop(inner);
        tauri::async_runtime::spawn_blocking(move || {
            if previous.exists() && !installer::owned_directory(&previous) {
                return Err(Failure::VerificationFailed.into());
            }
            match fs::remove_dir_all(previous) {
                Ok(()) => Ok(()),
                Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
                Err(error) => Err(UpdateError::new(Failure::StateUnavailable, error)),
            }
        })
        .await
        .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))??;
    }
    Ok(())
}
