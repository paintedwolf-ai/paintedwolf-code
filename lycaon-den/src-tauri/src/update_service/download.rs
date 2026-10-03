use super::{
    emit, staging, Candidate, Failure, Installation, NativeUpdateState, UpdateError, UpdateService,
    DOWNLOAD_REQUEST_TIMEOUT,
};
use futures_util::StreamExt;
use std::{sync::atomic::Ordering, time::Duration};
use tauri::{AppHandle, Manager};
use tokio::io::AsyncWriteExt;

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
        let release = inner.state.candidate.as_ref().unwrap().release_id.clone();
        if inner.blocked_release.as_ref() == Some(&release) {
            return Ok(());
        }
        release
    };
    prepare(app, &release).await.map(|_| ())
}
#[tauri::command]
pub async fn download_update(
    app: AppHandle,
    expected_release_id: String,
) -> Result<NativeUpdateState, UpdateError> {
    prepare(&app, &expected_release_id).await
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
    prepare(&app, &expected_release_id).await
}
async fn prepare(app: &AppHandle, expected: &str) -> Result<NativeUpdateState, UpdateError> {
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
        inner.state.installation = Installation::Downloading;
        inner.state.downloaded_bytes = 0;
        inner.state.total_bytes = None;
        inner.state.last_error = None;
        emit(app, &mut inner.state);
        (candidate, service.generation.load(Ordering::Acquire))
    };
    let result = tokio::select! {
        result = transfer(app, &candidate, generation) => result,
        _ = service.wake.notified() => Err(Failure::Cancelled.into()),
    };
    let mut inner = service.inner.lock().await;
    if !service.current(generation) {
        return Ok(inner.state.clone());
    }
    let mut failure = None;
    match result.and_then(|identity| staging::publish(&candidate, identity)) {
        Ok(()) => {
            inner.state.staged_release_id = Some(candidate.release_id.clone());
            inner.state.installation = Installation::Staged;
            inner.state.last_error = None;
            let mut keep = vec![candidate.release_id];
            let cleanup = super::transaction::retained_release().and_then(|retained| {
                if let Some(id) = retained {
                    keep.push(id);
                }
                staging::cleanup(&keep)?;
                super::installer::cleanup_prepared(&super::installer::bundle()?, &keep)
            });
            if let Err(error) = cleanup {
                inner.state.last_error = Some(error);
            }
        }
        Err(mut error) => {
            if matches!(
                error.code,
                Failure::VerificationFailed
                    | Failure::UnsupportedInstallation
                    | Failure::InstallFailed
                    | Failure::InvalidRelease
            ) {
                if let Err(persist) = staging::reject(candidate.release_id.clone()) {
                    error = error.with_context(persist);
                }
                inner.blocked_release = Some(candidate.release_id);
            }
            inner.state.installation = Installation::Failed;
            failure = Some(error.clone());
            inner.state.last_error = Some(error);
        }
    }
    emit(app, &mut inner.state);
    failure.map_or_else(|| Ok(inner.state.clone()), Err)
}
async fn transfer(
    app: &AppHandle,
    candidate: &Candidate,
    generation: u64,
) -> Result<super::installer::PreparedIdentity, UpdateError> {
    let service = app.state::<UpdateService>();
    let permit = service.preparation.clone().lock_owned().await;
    if !service.current(generation) {
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
            let _permit = permit;
            // Preparation verifies the cached archive before using any of its bytes.
            super::installer::prepare(&release)
        })
        .await
        .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
    }
    let partial = dir.join(format!("{}.partial", uuid::Uuid::new_v4()));
    let cleanup = Partial(partial.clone());
    let url = tauri::Url::parse(&candidate.artifact_url)
        .map_err(|e| UpdateError::new(Failure::InvalidRelease, e))?;
    if url.scheme() != "https" || url.host_str() != Some("downloads.paintedwolf.dev") {
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
        if !service.current(generation) {
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
    let artifact = partial.clone();
    let release = candidate.clone();
    let (permit, verified) = tauri::async_runtime::spawn_blocking(move || {
        let verified = super::verification::verify(
            &artifact,
            &release.artifact_signature,
            &super::check::embedded_key().1,
            &release.version,
        );
        (permit, verified)
    })
    .await
    .map_err(|e| UpdateError::new(Failure::VerificationFailed, e))?;
    verified.map_err(|e| UpdateError::new(Failure::VerificationFailed, e))?;
    phase(app, generation, Installation::Preparing).await?;
    crate::atomic_file::replace(&partial, &dir.join("artifact"), true)
        .map_err(|e| UpdateError::new(Failure::DownloadFailed, e))?;
    drop(cleanup);
    let release = candidate.clone();
    tauri::async_runtime::spawn_blocking(move || {
        let _permit = permit;
        super::installer::prepare(&release)
    })
    .await
    .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?
}
async fn phase(app: &AppHandle, generation: u64, phase: Installation) -> Result<(), UpdateError> {
    let service = app.state::<UpdateService>();
    let mut inner = service.inner.lock().await;
    if !service.current(generation) {
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
