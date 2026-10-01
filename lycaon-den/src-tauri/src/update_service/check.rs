//! Release discovery: signed manifests, local rollout, and the automatic schedule.

use super::persistence::detect_install_source;
use super::{
    emit_update_state, CheckedCandidate, Failure, NativeUpdateState, UpdateChannel, UpdateError,
    UpdatePhase, UpdateService, CHECK_INTERVAL, CHECK_REQUEST_TIMEOUT, DISABLED_POLL_INTERVAL,
    DOWNLOAD_REQUEST_TIMEOUT, INITIAL_CHECK_DELAY, RETRY_INTERVAL,
};
use std::time::Duration;
use tauri::{AppHandle, Manager};
use tauri_plugin_updater::{Update, UpdaterExt};

pub(super) fn embedded_key() -> (u64, String) {
    let registry: serde_json::Value =
        serde_json::from_str(include_str!("../../../../packaging/update-keys.json"))
            .expect("compiled updater key registry");
    let number = registry["embedded_generation"]
        .as_u64()
        .expect("embedded key generation");
    let key = registry["generations"]
        .as_array()
        .expect("key generations")
        .iter()
        .find(|row| row["generation"].as_u64() == Some(number))
        .expect("embedded public key");
    (
        number,
        key["public_key"]
            .as_str()
            .expect("public key string")
            .into(),
    )
}

// Automatic checks use an age-based local rollout bucket.
pub(super) const ROLLOUT_STAGES: [(u64, u8); 2] = [(24 * 60 * 60, 10), (48 * 60 * 60, 50)];
pub(super) const ROLLOUT_BUCKETS: u8 = 100;

pub(super) fn rollout_percent(age_secs: u64) -> u8 {
    for (below_secs, percent) in ROLLOUT_STAGES {
        if age_secs < below_secs {
            return percent;
        }
    }
    100
}

pub(super) fn rollout_admits(bucket: u8, manifest_age_secs: Option<u64>) -> bool {
    match manifest_age_secs {
        None => true,
        Some(age) => bucket < rollout_percent(age),
    }
}

pub(super) fn manifest_age_secs(published_unix: Option<i64>) -> Option<u64> {
    let published = published_unix?;
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .ok()?
        .as_secs() as i64;
    Some(now.saturating_sub(published).max(0) as u64)
}

pub(super) fn draw_rollout_bucket() -> u8 {
    use rand::Rng;
    rand::thread_rng().gen_range(0..ROLLOUT_BUCKETS)
}

pub(super) fn manifest_offers_update(
    raw: &serde_json::Value,
    generation: u64,
) -> Result<bool, UpdateError> {
    if raw
        .pointer("/update_keys/signing_generation")
        .and_then(serde_json::Value::as_u64)
        != Some(generation)
    {
        return Err(UpdateError::from(Failure::InvalidRelease));
    }
    match raw.get("withdrawn") {
        Some(serde_json::Value::Bool(true)) => Ok(false),
        Some(_) => Err(UpdateError::from(Failure::InvalidRelease)),
        None => Ok(true),
    }
}

pub(super) async fn fetch_candidate(
    app: &AppHandle,
    current_version: String,
    channel: UpdateChannel,
) -> Result<Option<CheckedCandidate<Update>>, UpdateError> {
    let endpoint = channel.endpoint();
    let endpoint = tauri::Url::parse(&endpoint)
        .map_err(|err| UpdateError::new(Failure::InvalidRelease, err))?;
    let compared_version = semver::Version::parse(&current_version)
        .map_err(|err| UpdateError::new(Failure::InvalidVersion, err))?;
    let builder = app
        .updater_builder()
        .pubkey(embedded_key().1)
        .endpoints(vec![endpoint])
        .map_err(UpdateError::check)?
        .version_comparator(move |_native_version, release| release.version > compared_version)
        .timeout(CHECK_REQUEST_TIMEOUT)
        .header("Cache-Control", "no-cache, no-store")
        .map_err(UpdateError::check)?;
    let checked = match builder.build() {
        Ok(updater) => updater.check().await,
        Err(err) => Err(err),
    };
    let checked = checked.map_err(UpdateError::check)?;
    if let Some(candidate) = &checked {
        if !manifest_offers_update(&candidate.raw_json, embedded_key().0)? {
            return Ok(None);
        }
    }
    checked
        .map(|mut candidate| {
            candidate.timeout = Some(DOWNLOAD_REQUEST_TIMEOUT);
            Ok(CheckedCandidate {
                current_version,
                version: candidate.version.clone(),
                notes: candidate.body.clone(),
                age_secs: manifest_age_secs(candidate.date.map(|date| date.unix_timestamp())),
                candidate,
            })
        })
        .transpose()
}

pub(super) async fn check_update_inner(
    app: &AppHandle,
    service: &UpdateService,
    automatic: bool,
) -> Result<NativeUpdateState, UpdateError> {
    let _operation = service.operation.lock().await;
    let ticket = {
        let mut inner = service.inner.lock().await;
        let Some(ticket) = inner.begin_check(automatic)? else {
            return Ok(inner.state.clone());
        };
        emit_update_state(app, &mut inner.state);
        ticket
    };

    let checked = fetch_candidate(
        app,
        ticket.previous.current_version.clone(),
        ticket.previous.channel,
    )
    .await;
    let mut inner = service.inner.lock().await;
    let (install_source, _, install_source_error) = detect_install_source();
    if !inner.finish_check(ticket, checked, install_source, install_source_error) {
        return Ok(inner.state.clone());
    }
    emit_update_state(app, &mut inner.state);
    let state = inner.state.clone();
    drop(inner);
    Ok(state)
}

#[tauri::command]
pub async fn check_update(
    app: AppHandle,
    service: tauri::State<'_, UpdateService>,
) -> Result<NativeUpdateState, UpdateError> {
    check_update_inner(&app, service.inner(), false).await
}

pub(super) async fn run_automatic_check(app: AppHandle) -> Result<NativeUpdateState, UpdateError> {
    let service = app.state::<UpdateService>();
    check_update_inner(&app, service.inner(), true).await
}

pub(super) fn next_automatic_delay(state: &NativeUpdateState) -> Duration {
    if !state.checks_enabled {
        DISABLED_POLL_INTERVAL
    } else if state.phase == UpdatePhase::Unavailable || state.error.is_some() {
        RETRY_INTERVAL
    } else {
        CHECK_INTERVAL
    }
}

pub fn start_update_scheduler(app: AppHandle) {
    tauri::async_runtime::spawn(async move {
        tokio::time::sleep(INITIAL_CHECK_DELAY).await;
        loop {
            let delay = match run_automatic_check(app.clone()).await {
                Ok(state) => next_automatic_delay(&state),
                Err(_) => RETRY_INTERVAL,
            };
            tokio::time::sleep(delay).await;
        }
    });
}
