//! Discovery: one check reads the signed feed and projects the offer into native state.
use super::{
    emit, feed, Discovery, Installation, NativeUpdateState, RolloutEligibility, UpdateError,
    UpdateService,
};
use std::sync::atomic::Ordering;
use tauri::{AppHandle, Manager};
use UpdateError as Error;

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

/// A manifest without a usable publication date is treated as brand new.
pub(super) fn rollout_admits(bucket: u8, manifest_age_secs: Option<u64>) -> bool {
    bucket < rollout_percent(manifest_age_secs.unwrap_or(0))
}

pub(super) fn draw_rollout_bucket() -> u8 {
    use rand::Rng;
    rand::thread_rng().gen_range(0..ROLLOUT_BUCKETS)
}

pub(super) fn apply_offer(
    state: &mut NativeUpdateState,
    checked: Result<Option<feed::Offer>, Error>,
    automatic: bool,
    bucket: u8,
    now: u64,
) {
    state.last_check_at = Some(now);
    match checked {
        Ok(Some(feed::Offer {
            candidate,
            published_age_secs,
        })) => {
            let retained = state.candidate.as_ref().is_some_and(|old| {
                old.release_id == candidate.release_id
                    && old.rollout_eligibility == RolloutEligibility::Eligible
            });
            if automatic && !retained && !rollout_admits(bucket, published_age_secs) {
                state.discovery = Discovery::HeldBack;
                if state.candidate.is_none() {
                    let mut held = candidate;
                    held.rollout_eligibility = RolloutEligibility::HeldBack;
                    state.candidate = Some(held);
                    state.offer_confirmed_at = Some(now);
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
            state.offer_confirmed_at = Some(now);
            state.discovery = Discovery::Available;
            if state.installation != Installation::Failed {
                state.last_error = None;
            }
        }
        Ok(None) => {
            state.discovery = Discovery::UpToDate;
            // An installed or committed release is not withdrawn by its own feed catching up.
            if !matches!(
                state.installation,
                Installation::AwaitingStartup | Installation::Committed
            ) {
                state.candidate = None;
                state.staged_release_id = None;
                state.installation = Installation::None;
                state.offer_confirmed_at = None;
            }
            state.last_error = None;
        }
        Err(error) => {
            if error.code == super::Failure::FeedRejected {
                state.offer_confirmed_at = None;
            }
            state.discovery = Discovery::Failed;
            state.last_error = Some(error);
        }
    }
}
/// A check either ran against the feed or stepped aside for work already in progress.
pub(super) enum Check {
    Skipped(NativeUpdateState),
    Completed(NativeUpdateState),
}
impl Check {
    pub(super) fn state(self) -> NativeUpdateState {
        match self {
            Self::Skipped(state) | Self::Completed(state) => state,
        }
    }
}
pub(super) async fn run_check(app: &AppHandle, automatic: bool) -> Result<Check, Error> {
    let service = app.state::<UpdateService>();
    let mut wake = service.wake.subscribe();
    let (generation, version, channel) = {
        let mut inner = service.inner.lock().await;
        if (automatic && !inner.state.automatic_updates_enabled)
            || inner.state.installation.busy()
            || inner.state.discovery == Discovery::Checking
        {
            return Ok(Check::Skipped(inner.state.clone()));
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
        result = feed::fetch(channel, &version) => result.map_err(feed::FeedFailure::error),
        _ = wake.wait_for(|_| !service.current(generation)) => Err(super::Failure::Cancelled.into()),
    };
    let failure = checked.as_ref().err().cloned();
    let mut inner = service.inner.lock().await;
    if !service.current(generation) {
        // Cancellation reset Checking before another check could start.
        return Ok(Check::Skipped(inner.state.clone()));
    }
    let bucket = inner.preferences.rollout_bucket;
    let (source, _, source_error) = super::persistence::detect_install_source();
    inner.state.install_source = source;
    let now = super::now();
    apply_offer(&mut inner.state, checked, automatic, bucket, now);
    if source_error.is_some() {
        inner.state.last_error = source_error;
    }
    if failure
        .as_ref()
        .is_some_and(|e| e.code == super::Failure::FeedRejected)
    {
        if let Err(error) = super::staging::invalidate_confirmation() {
            inner.state.last_error = Some(error.clone());
            emit(app, &mut inner.state);
            return Err(error);
        }
    }
    let staged_confirmed = inner.state.staged_release_id.clone().filter(|staged| {
        inner
            .state
            .candidate
            .as_ref()
            .is_some_and(|candidate| &candidate.release_id == staged)
            && inner.state.offer_confirmed_at == Some(now)
    });
    let staging = match (&staged_confirmed, inner.state.staged_release_id.is_none()) {
        (Some(release), _) => super::staging::confirm_offer(release, now),
        (None, true) if inner.state.installation != Installation::Committed => {
            super::staging::forget_ready()
        }
        _ => Ok(()),
    };
    if let Err(error) = staging {
        inner.state.last_error = Some(error);
    }
    emit(app, &mut inner.state);
    match failure {
        Some(error) => Err(error),
        None => Ok(Check::Completed(inner.state.clone())),
    }
}
#[tauri::command]
pub async fn check_update(app: AppHandle) -> Result<NativeUpdateState, Error> {
    let state = run_check(&app, false).await?.state();
    if state.automatic_updates_enabled {
        super::download::start_automatic(app.clone());
    }
    Ok(state)
}
