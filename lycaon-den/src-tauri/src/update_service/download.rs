//! Preparation: stream the signed artifact, verify it, and build the replacement bundle.
use super::{
    emit, staging, Candidate, Failure, Installation, NativeUpdateState, UpdateError, UpdateService,
    DOWNLOAD_ORIGIN, DOWNLOAD_REQUEST_TIMEOUT,
};
use futures_util::StreamExt;
use std::{sync::atomic::Ordering, time::Duration};
use tauri::{AppHandle, Manager};
use tokio::io::AsyncWriteExt;

#[cfg(target_os = "macos")]
type PreparationPermit = (
    tokio::sync::OwnedMutexGuard<()>,
    super::installer::InstallationLease,
);
#[cfg(not(target_os = "macos"))]
type PreparationPermit = tokio::sync::OwnedMutexGuard<()>;

pub fn start_automatic(app: AppHandle) {
    tauri::async_runtime::spawn(async move {
        let _ = automatic(&app).await;
    });
}
pub async fn automatic(app: &AppHandle) -> Result<(), UpdateError> {
    let service = app.state::<UpdateService>();
    let release = {
        let mut inner = service.inner.lock().await;
        inner
            .state
            .refresh_capabilities(super::installer::supported());
        if !inner.state.automatic_updates_enabled || !inner.state.capabilities.can_download {
            return Ok(());
        }
        let Some(release) = inner.state.candidate.as_ref().map(|c| c.release_id.clone()) else {
            return Ok(());
        };
        if inner.blocked_release.as_ref() == Some(&release) {
            return Ok(());
        }
        release
    };
    prepare(app, &release, false).await.map(|_| ())
}
#[tauri::command]
pub async fn download_update(
    app: AppHandle,
    expected_release_id: String,
) -> Result<NativeUpdateState, UpdateError> {
    prepare(&app, &expected_release_id, true).await
}
#[tauri::command]
pub async fn retry_update(
    app: AppHandle,
    expected_release_id: String,
) -> Result<NativeUpdateState, UpdateError> {
    {
        let service = app.state::<UpdateService>();
        let mut inner = service.inner.lock().await;
        if inner
            .state
            .candidate
            .as_ref()
            .map(|c| c.release_id.as_str())
            != Some(expected_release_id.as_str())
        {
            return Err(Failure::CandidateChanged.into());
        }
        staging::clear_rejected(&expected_release_id)?;
        inner.blocked_release = None;
    }
    super::transaction::clear_failed(&expected_release_id)?;
    prepare(&app, &expected_release_id, true).await
}
async fn prepare(
    app: &AppHandle,
    expected: &str,
    manual: bool,
) -> Result<NativeUpdateState, UpdateError> {
    let service = app.state::<UpdateService>();
    let (candidate, generation) = {
        let mut inner = service.inner.lock().await;
        let candidate = inner
            .state
            .candidate
            .clone()
            .ok_or(Failure::CandidateMissing)?;
        if candidate.release_id != expected {
            return Err(Failure::CandidateChanged.into());
        }
        if inner.state.staged_release_id.as_deref() == Some(expected)
            || inner.state.installation.busy()
        {
            return Ok(inner.state.clone());
        }
        inner
            .state
            .refresh_capabilities(super::installer::supported());
        if !inner.state.capabilities.can_download {
            return Err(Failure::UnsupportedInstallation.into());
        }
        inner.manual_preparation = manual;
        inner.state.installation = Installation::Downloading;
        inner.state.downloaded_bytes = 0;
        inner.state.total_bytes = None;
        inner.state.last_error = None;
        emit(app, &mut inner.state);
        (
            candidate,
            service.preparation_generation.load(Ordering::Acquire),
        )
    };
    let mut wake = service.wake.subscribe();
    let result = tokio::select! {
        result = transfer(app, &candidate, generation) => result,
        _ = wake.wait_for(|_| service.preparation_generation.load(Ordering::Acquire) != generation) => Err(Failure::Cancelled.into()),
    };
    let mut inner = service.inner.lock().await;
    if service.preparation_generation.load(Ordering::Acquire) != generation {
        return Ok(inner.state.clone());
    }
    let confirmed_at = inner.state.offer_confirmed_at;
    match result.and_then(|(identity, permit)| {
        staging::publish(&candidate, identity, confirmed_at).map(|()| permit)
    }) {
        Ok(permit) => {
            inner.state.staged_release_id = Some(candidate.release_id.clone());
            inner.state.installation = Installation::Staged;
            inner.state.last_error = None;
            emit(app, &mut inner.state);
            drop(inner);
            let release = candidate.release_id;
            let keep_release = release.clone();
            let cleanup = tauri::async_runtime::spawn_blocking(move || {
                let _permit = permit;
                let mut keep = vec![keep_release];
                if let Some(id) = super::transaction::retained_release()? {
                    keep.push(id);
                }
                staging::cleanup(&keep)?;
                super::installer::cleanup_prepared(&super::installer::bundle()?, &keep)
            })
            .await
            .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))
            .and_then(|result| result);
            let mut inner = service.inner.lock().await;
            if inner.state.staged_release_id.as_deref() == Some(&release) {
                if let Err(error) = cleanup {
                    inner.state.last_error = Some(error);
                    emit(app, &mut inner.state);
                }
            }
            return Ok(inner.state.clone());
        }
        Err(mut error) => {
            if matches!(
                error.code,
                Failure::VerificationFailed
                    | Failure::UnsupportedInstallation
                    | Failure::InvalidRelease
            ) {
                if let Err(persist) = staging::reject(candidate.release_id.clone()) {
                    error = error.with_context(persist);
                }
                inner.blocked_release = Some(candidate.release_id);
            }
            inner.state.installation = Installation::Failed;
            inner.state.last_error = Some(error.clone());
            emit(app, &mut inner.state);
            Err(error)
        }
    }
}
async fn transfer(
    app: &AppHandle,
    candidate: &Candidate,
    generation: u64,
) -> Result<(super::installer::PreparedIdentity, PreparationPermit), UpdateError> {
    let service = app.state::<UpdateService>();
    let permit = service.preparation.clone().lock_owned().await;
    #[cfg(target_os = "macos")]
    let permit = {
        let lease = tauri::async_runtime::spawn_blocking(super::installer::acquire_preparation)
            .await
            .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))??;
        (permit, lease)
    };
    if service.preparation_generation.load(Ordering::Acquire) != generation {
        return Err(Failure::Cancelled.into());
    }
    super::installer::probe_destination()?;
    let dir = staging::root(candidate)?;
    crate::config_dir::ensure_private_dir(&dir)
        .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
    staging::clean_partials(candidate)?;
    if dir.join("artifact").is_file() {
        phase(app, generation, Installation::Preparing).await?;
        let release = candidate.clone();
        return tauri::async_runtime::spawn_blocking(move || {
            // Preparation verifies the cached archive before using any of its bytes.
            super::installer::prepare(&release).map(|identity| (identity, permit))
        })
        .await
        .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
    }
    let partial = dir.join(format!("{}.partial", uuid::Uuid::new_v4()));
    let cleanup = Partial(partial.clone());
    let url = tauri::Url::parse(&candidate.artifact_url)
        .map_err(|e| UpdateError::new(Failure::InvalidRelease, e))?;
    if url.origin().ascii_serialization() != DOWNLOAD_ORIGIN {
        return Err(Failure::InvalidRelease.into());
    }
    let client = reqwest::Client::builder()
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .timeout(DOWNLOAD_REQUEST_TIMEOUT)
        .connect_timeout(Duration::from_secs(30))
        .build()
        .map_err(|e| UpdateError::new(Failure::DownloadFailed, e))?;
    let response = client
        .get(url.as_str())
        .send()
        .await
        .and_then(|r| r.error_for_status())
        .map_err(|e| UpdateError::new(Failure::DownloadFailed, e))?;
    let total = response.content_length();
    if total.is_some_and(|n| n > super::verification::limits().max_download_bytes) {
        return Err(Failure::InvalidRelease.into());
    }
    super::installer::check_space(
        &dir,
        total.unwrap_or(super::verification::limits().max_download_bytes),
    )?;
    let mut file = tokio::fs::OpenOptions::new()
        .create_new(true)
        .write(true)
        .open(&partial)
        .await
        .map_err(|e| UpdateError::new(Failure::DownloadFailed, e))?;
    let mut stream = response.bytes_stream();
    let mut downloaded = 0u64;
    let mut last_progress = std::time::Instant::now();
    while let Some(chunk) = stream.next().await {
        let chunk = chunk.map_err(|e| UpdateError::new(Failure::DownloadFailed, e))?;
        downloaded += chunk.len() as u64;
        if downloaded > super::verification::limits().max_download_bytes {
            return Err(Failure::InvalidRelease.into());
        }
        file.write_all(&chunk)
            .await
            .map_err(|e| UpdateError::new(Failure::DownloadFailed, e))?;
        let service = app.state::<UpdateService>();
        if service.preparation_generation.load(Ordering::Acquire) != generation {
            return Err(Failure::Cancelled.into());
        }
        let mut inner = service.inner.lock().await;
        inner.state.downloaded_bytes = downloaded;
        inner.state.total_bytes = total;
        if last_progress.elapsed() >= Duration::from_millis(100) {
            emit(app, &mut inner.state);
            last_progress = std::time::Instant::now();
        }
    }
    file.sync_all()
        .await
        .map_err(|e| UpdateError::new(Failure::DownloadFailed, e))?;
    drop(file);
    phase(app, generation, Installation::Verifying).await?;
    crate::atomic_file::replace(&partial, &dir.join("artifact"), true)
        .map_err(|e| UpdateError::new(Failure::DownloadFailed, e))?;
    drop(cleanup);
    phase(app, generation, Installation::Preparing).await?;
    let release = candidate.clone();
    tauri::async_runtime::spawn_blocking(move || {
        super::installer::prepare(&release).map(|identity| (identity, permit))
    })
    .await
    .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?
}
async fn phase(app: &AppHandle, generation: u64, phase: Installation) -> Result<(), UpdateError> {
    let service = app.state::<UpdateService>();
    let mut inner = service.inner.lock().await;
    if service.preparation_generation.load(Ordering::Acquire) != generation {
        return Err(Failure::Cancelled.into());
    }
    inner.state.installation = phase;
    emit(app, &mut inner.state);
    Ok(())
}
struct Partial(std::path::PathBuf);
impl Drop for Partial {
    fn drop(&mut self) {
        let _ = std::fs::remove_file(&self.0);
    }
}
