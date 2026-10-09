//! Exit preparation binds a confirmed offer to a durable install transaction.
use super::{
    emit, feed, installer, persistence, staging, Candidate, Failure, Installation, UpdateError,
    UpdateService,
};
use std::{path::Path, time::Duration};
use tauri::{AppHandle, Manager};
#[cfg(any(target_os = "macos", all(test, unix)))]
mod activation;
mod admission;
mod confirmation;
mod handoff;
mod journal;
mod state;
#[cfg(all(test, unix))]
mod tests;

pub use admission::{launch, run_helper, Launch, Lease};
pub use confirmation::confirm_startup;
pub use handoff::Activation;
pub use state::{clear_failed, recovery_guidance, resume};
pub(super) use state::{restore, retained_release};
use handoff::stage_helper;
use installer::{executable, hash};
use journal::{read, write, Phase, Transaction};

const OFFER_FRESHNESS: Duration = Duration::from_secs(24 * 60 * 60);
const RECHECK_DEADLINE: Duration = Duration::from_secs(3);

fn require_direct_download() -> Result<(), UpdateError> {
    let (source, _, error) = persistence::detect_install_source();
    if let Some(error) = error {
        return Err(error);
    }
    if source != super::InstallSource::DirectDownload {
        return Err(Failure::PackageManaged.into());
    }
    Ok(())
}
fn committed_reuse(
    t: &Transaction,
    target: &Path,
    expected: Option<&str>,
) -> Result<(), UpdateError> {
    if t.phase != Phase::Committed || t.target != target {
        return Err(Failure::InvalidTransition.into());
    }
    if expected.is_some_and(|id| id != t.candidate.release_id) {
        return Err(Failure::CandidateChanged.into());
    }
    Ok(())
}
enum Recheck {
    Confirmed,
    Withdrawn,
    Unavailable(UpdateError),
    Refused(UpdateError),
}
async fn recheck(candidate: &Candidate, running: &str) -> Recheck {
    match feed::fetch_with_deadline(candidate.channel, running, RECHECK_DEADLINE).await {
        Err(feed::FeedFailure::Transient(error)) => Recheck::Unavailable(error),
        Err(error) => Recheck::Refused(error.error()),
        Ok(Some(offer)) if offer.candidate.release_id == candidate.release_id => Recheck::Confirmed,
        Ok(_) => Recheck::Withdrawn,
    }
}
/// Offline installation requires a confirmation within the freshness window.
fn offer_is_fresh(confirmed_at: Option<u64>, now: u64) -> bool {
    confirmed_at
        .and_then(|at| now.checked_sub(at))
        .is_some_and(|age| age <= OFFER_FRESHNESS.as_secs())
}
pub async fn prepare_exit(
    app: &AppHandle,
    expected: Option<&str>,
    relaunch: bool,
) -> Result<Option<Activation>, UpdateError> {
    let service = app.state::<UpdateService>();
    let permit = service
        .activation
        .clone()
        .try_lock_owned()
        .map_err(|_| UpdateError::from(Failure::InvalidTransition))?;
    // Committed intent takes precedence over new offers.
    if let Some(committed) = read()?.filter(|t| t.phase == Phase::Committed) {
        return resume_committed(app, committed, expected, relaunch, permit)
            .await
            .map(Some);
    }
    let explicit = expected.is_some();
    let (candidate, running, confirmed_at) = {
        let mut inner = service.inner.lock().await;
        inner.state.refresh_capabilities(installer::supported());
        if !(if explicit {
            inner.state.capabilities.can_restart_to_update
        } else {
            inner.state.capabilities.can_install_automatically
        }) {
            return Ok(None);
        }
        let candidate = inner
            .state
            .candidate
            .clone()
            .ok_or(Failure::CandidateMissing)?;
        (
            candidate,
            inner.state.running_version.clone(),
            inner.state.offer_confirmed_at,
        )
    };
    if expected.is_some_and(|id| id != candidate.release_id) {
        return Err(Failure::CandidateChanged.into());
    }
    require_direct_download()?;
    let now = super::now();
    let mut offline = false;
    match recheck(&candidate, &running).await {
        Recheck::Confirmed => {
            service.inner.lock().await.state.offer_confirmed_at = Some(now);
            staging::confirm_offer(&candidate.release_id, now)?;
        }
        Recheck::Withdrawn => {
            withdraw(app).await?;
            return Err(Failure::ReleaseWithdrawn.into());
        }
        // A recently confirmed offer installs offline; otherwise automatic paths defer quietly.
        Recheck::Unavailable(error) if !offer_is_fresh(confirmed_at, now) => {
            return if explicit { Err(error) } else { Ok(None) };
        }
        Recheck::Unavailable(_) => {
            offline = true;
        }
        Recheck::Refused(error) => {
            if error.code == Failure::FeedRejected {
                service.inner.lock().await.state.offer_confirmed_at = None;
                staging::invalidate_confirmation()?;
            }
            return Err(error);
        }
    }
    let inner = service.inner.lock().await;
    if inner.state.staged_release_id.as_ref() != Some(&candidate.release_id)
        || (!explicit && !inner.state.automatic_updates_enabled)
    {
        return Ok(None);
    }
    let generation = service
        .generation
        .load(std::sync::atomic::Ordering::Acquire);
    drop(inner);
    let release = candidate.clone();
    let prepared_result = tauri::async_runtime::spawn_blocking(move || {
        let candidate = release;
        let target = installer::bundle()?;
        let ready = staging::read_ready()?.ok_or(Failure::CandidateMissing)?;
        if offline && !offer_is_fresh(ready.offer_confirmed_at, super::now()) {
            return Err(Failure::CheckFailed.into());
        }
        if ready.candidate.release_id != candidate.release_id
            || hash(&executable(&ready.prepared_bundle))? != ready.executable_hash
        {
            return Err(Failure::VerificationFailed.into());
        }
        let t = Transaction {
            format_version: 2,
            prepared_bundle: ready.prepared_bundle,
            id: uuid::Uuid::new_v4().to_string(),
            candidate: candidate.clone(),
            previous_hash: hash(&executable(&target))?,
            next_hash: ready.executable_hash,
            next_bundle_hash: ready.bundle_hash,
            target,
            phase: Phase::Prepared,
            relaunch,
            recovery_relaunch_attempted: false,
            error: None,
        };
        let helper = stage_helper(&candidate)?;
        Ok::<_, UpdateError>((t, helper))
    })
    .await
    .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
    let mut inner = service.inner.lock().await;
    if !service.current(generation)
        || inner.state.staged_release_id.as_ref() != Some(&candidate.release_id)
        || (!explicit && !inner.state.automatic_updates_enabled)
    {
        return Ok(None);
    }
    let (t, helper) = match prepared_result {
        Ok(result) => result,
        Err(error) => {
            let error = invalidate_preparation(&mut inner, &candidate, error);
            emit(app, &mut inner.state);
            return Err(error);
        }
    };
    if let Some(previous) = read()? {
        if previous.phase == Phase::Activated
            && (previous.target != t.target || t.previous_hash != previous.next_hash)
        {
            return Err(Failure::InvalidTransition.into());
        }
        journal::archive_receipt(&previous)?;
    }
    write(&t)?;
    service.cancel(&mut inner.state);
    inner.state.installation = Installation::AwaitingExit;
    emit(app, &mut inner.state);
    Ok(Some(Activation {
        transaction: t,
        helper,
        _permit: permit,
    }))
}
async fn resume_committed(
    app: &AppHandle,
    mut t: Transaction,
    expected: Option<&str>,
    relaunch: bool,
    permit: tokio::sync::OwnedMutexGuard<()>,
) -> Result<Activation, UpdateError> {
    let service = app.state::<UpdateService>();
    committed_reuse(&t, &installer::bundle()?, expected)?;
    require_direct_download()?;
    if t.relaunch != relaunch {
        t.relaunch = relaunch;
        write(&t)?;
    }
    let candidate = t.candidate.clone();
    let helper = tauri::async_runtime::spawn_blocking(move || stage_helper(&candidate))
        .await
        .map_err(|e| UpdateError::new(Failure::InstallFailed, e))??;
    let mut inner = service.inner.lock().await;
    service.cancel(&mut inner.state);
    inner.state.staged_release_id = Some(t.candidate.release_id.clone());
    inner.state.candidate = Some(t.candidate.clone());
    inner.state.installation = Installation::AwaitingExit;
    emit(app, &mut inner.state);
    Ok(Activation {
        transaction: t,
        helper,
        _permit: permit,
    })
}
async fn withdraw(app: &AppHandle) -> Result<(), UpdateError> {
    let service = app.state::<UpdateService>();
    let mut inner = service.inner.lock().await;
    let error = staging::forget_ready().err();
    inner.state.candidate = None;
    inner.state.installation = Installation::None;
    inner.state.staged_release_id = None;
    inner.state.offer_confirmed_at = None;
    inner.state.discovery = super::Discovery::Idle;
    inner.state.last_error = Some(
        error
            .clone()
            .unwrap_or_else(|| Failure::ReleaseWithdrawn.into()),
    );
    emit(app, &mut inner.state);
    error.map_or(Ok(()), Err)
}
fn invalidate_preparation(
    inner: &mut super::Inner,
    candidate: &Candidate,
    mut error: UpdateError,
) -> UpdateError {
    let permanent = matches!(
        error.code,
        Failure::VerificationFailed | Failure::InvalidRelease
    );
    let invalidated = staging::forget_ready().and_then(|()| {
        if permanent {
            staging::reject(candidate.release_id.clone())
        } else {
            Ok(())
        }
    });
    if let Err(failure) = invalidated {
        error = error.with_context(failure);
    }
    inner.state.staged_release_id = None;
    inner.state.installation = Installation::Failed;
    inner.state.last_error = Some(error.clone());
    inner.blocked_release = permanent.then(|| candidate.release_id.clone());
    error
}
#[tauri::command]
pub async fn restart_to_update(
    app: AppHandle,
    expected_release_id: String,
) -> Result<(), UpdateError> {
    {
        let service = app.state::<UpdateService>();
        let inner = service.inner.lock().await;
        if inner.state.staged_release_id.as_deref() != Some(&expected_release_id) {
            return Err(Failure::CandidateChanged.into());
        }
    }
    match crate::app_exit::install_update(&app, Some(&expected_release_id), false).await {
        Ok(true) => Ok(()),
        Ok(false) => Err(Failure::InvalidTransition.into()),
        Err(error) => {
            resume(&app, Some(error.clone())).await;
            app.state::<UpdateService>().finish_startup(&app).await;
            Err(error)
        }
    }
}
