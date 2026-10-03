//! Feed discovery and exact-offer revalidation.
use super::{
    emit, Candidate, Discovery, Failure, Installation, NativeUpdateState, RolloutEligibility,
    UpdateChannel, UpdateError, UpdateService, CHECK_REQUEST_TIMEOUT,
};
use std::sync::atomic::Ordering;
use tauri::{AppHandle, Manager};
use tauri_plugin_updater::UpdaterExt;
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
) -> Result<Option<(Candidate, Option<u64>)>, UpdateError> {
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
    Ok(checked.map(|candidate| {
        let mut release = Candidate {
            release_id: String::new(),
            version: candidate.version.clone(),
            channel,
            platform: candidate.target.clone(),
            signing_generation: embedded_key().0,
            artifact_url: candidate.download_url.to_string(),
            artifact_signature: candidate.signature.clone(),
            notes: candidate.body.clone(),
            rollout_eligibility: RolloutEligibility::Eligible,
        };
        release.release_id = release.identity();
        (
            release,
            manifest_age_secs(candidate.date.map(|date| date.unix_timestamp())),
        )
    }))
}

pub(super) fn apply_offer(
    state: &mut NativeUpdateState,
    checked: Result<Option<(Candidate, Option<u64>)>, UpdateError>,
    automatic: bool,
    bucket: u8,
) {
    state.last_check_at = Some(super::now());
    match checked {
        Ok(Some((candidate, age))) => {
            let retained = state.candidate.as_ref().is_some_and(|old| {
                old.release_id == candidate.release_id
                    && old.rollout_eligibility == RolloutEligibility::Eligible
            });
            if automatic && !retained && !rollout_admits(bucket, age) {
                state.discovery = Discovery::HeldBack;
                if state.candidate.is_none() {
                    let mut held = candidate;
                    held.rollout_eligibility = RolloutEligibility::HeldBack;
                    state.candidate = Some(held);
                }
                return;
            }
            let changed =
                state.candidate.as_ref().map(|old| &old.release_id) != Some(&candidate.release_id);
            if changed {
                state.installation = Installation::None;
                state.staged_release_id = None;
            }
            state.candidate = Some(candidate);
            state.discovery = Discovery::Available;
            if state.installation != Installation::Failed {
                state.last_error = None;
            }
        }
        Ok(None) => {
            state.discovery = Discovery::UpToDate;
            state.candidate = None;
            state.staged_release_id = None;
            state.installation = Installation::None;
            state.last_error = None;
        }
        Err(error) => {
            state.discovery = Discovery::Failed;
            state.last_error = Some(error);
        }
    }
}
pub(super) async fn run_check(
    app: &AppHandle,
    automatic: bool,
) -> Result<NativeUpdateState, UpdateError> {
    let service = app.state::<UpdateService>();
    let (generation, version, channel) = {
        let mut inner = service.inner.lock().await;
        if (automatic && !inner.state.automatic_updates_enabled)
            || inner.state.installation.busy()
            || inner.state.discovery == Discovery::Checking
        {
            return Ok(inner.state.clone());
        }
        inner.state.discovery = Discovery::Checking;
        emit(app, &mut inner.state);
        (
            service.generation.load(Ordering::Acquire),
            inner.state.running_version.clone(),
            inner.state.channel,
        )
    };
    let checked = tokio::select! {
        result = fetch_candidate(app, version, channel) => result,
        _ = service.wake.notified() => Err(Failure::Cancelled.into()),
    };
    let failure = checked.as_ref().err().cloned();
    let mut inner = service.inner.lock().await;
    if !service.current(generation) {
        return Ok(inner.state.clone());
    }
    let bucket = inner.preferences.rollout_bucket;
    let (source, _, source_error) = super::persistence::detect_install_source();
    inner.state.install_source = source;
    apply_offer(&mut inner.state, checked, automatic, bucket);
    if source_error.is_some() {
        inner.state.last_error = source_error;
    }
    if inner.state.staged_release_id.is_none() {
        if let Err(error) = super::staging::forget_ready() {
            inner.state.last_error = Some(error);
        }
    }
    emit(app, &mut inner.state);
    failure.map_or_else(|| Ok(inner.state.clone()), Err)
}
#[tauri::command]
pub async fn check_update(app: AppHandle) -> Result<NativeUpdateState, UpdateError> {
    let state = run_check(&app, false).await?;
    if state.automatic_updates_enabled {
        super::download::start_automatic(app.clone());
    }
    Ok(state)
}
pub use super::scheduler::start_update_scheduler;
