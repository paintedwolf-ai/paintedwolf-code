//! Activation: a committed transaction survives the native parent; an uncommitted quit does not.
//!
//! The parent prepares a handoff and commits it only after every window has preserved its
//! work and the engine has stopped. The helper, or the next launch that wins the exclusive
//! lease, performs the exchange. A committed journal is honoured by whichever process gets
//! there first and is never refused as a conflict.
use super::{
    emit, feed, installer, persistence, staging, Candidate, Failure, Installation, UpdateError,
    UpdateService,
};
use std::{
    fs,
    path::{Path, PathBuf},
    time::Duration,
};
use tauri::{AppHandle, Manager};
mod journal;
use installer::{executable, hash};
use journal::{read, retire, write, Phase, Transaction};

/// A staged offer confirmed within this window still installs while the feed is unreachable.
const OFFER_FRESHNESS: Duration = Duration::from_secs(24 * 60 * 60);
const RECHECK_DEADLINE: Duration = Duration::from_secs(3);
/// How long the helper waits for the last running instance before deferring to a later launch.
const HELPER_ADMISSION: Duration = Duration::from_secs(10 * 60);
const HELPER_POLL: Duration = Duration::from_millis(250);

#[cfg(target_os = "macos")]
pub type Lease = installer::InstallationLease;
#[cfg(not(target_os = "macos"))]
pub type Lease = ();

/// What the launch guard established before the application started.
pub struct Launch {
    /// The shared lifetime lease; `None` when the lease could not be taken.
    pub lease: Option<Lease>,
    /// A recovery-relevant failure that did not prevent launching.
    pub error: Option<UpdateError>,
}

pub fn clear_failed(release: &str) -> Result<(), UpdateError> {
    if let Some(t) = read()? {
        if t.phase == Phase::Failed && t.candidate.release_id == release {
            retire(&t)?;
        }
    }
    Ok(())
}
pub struct Activation {
    transaction: Transaction,
    helper: PathBuf,
    _permit: tokio::sync::OwnedMutexGuard<()>,
}
impl Activation {
    pub fn commit(mut self) -> Result<(), UpdateError> {
        self.transaction.phase = Phase::Committed;
        write(&self.transaction)?;
        let result = std::process::Command::new(&self.helper)
            .arg("--apply-update")
            .arg(&self.transaction.id)
            .arg(persistence::installation_id(&self.transaction.target))
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null())
            .spawn();
        if let Err(error) = result {
            return Err(record_spawn_failure(&mut self.transaction, error, write));
        }
        Ok(())
    }
}
fn record_spawn_failure(
    t: &mut Transaction,
    error: std::io::Error,
    record: impl FnOnce(&Transaction) -> Result<(), UpdateError>,
) -> UpdateError {
    t.phase = Phase::Failed;
    t.error = Some(error.to_string());
    match record(t) {
        Ok(()) => UpdateError::new(Failure::ActivationFailed, error),
        Err(record) => UpdateError::new(Failure::RecoveryRequired, error).with_context(record),
    }
}
/// A private copy of the running executable that applies the update after the parent exits.
fn stage_helper(candidate: &Candidate) -> Result<PathBuf, UpdateError> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        let dir = staging::root(candidate)?;
        crate::config_dir::ensure_private_dir(&dir)
            .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        let helper = dir.join("update-helper");
        match fs::symlink_metadata(&helper) {
            Ok(_) => fs::remove_file(&helper)
                .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => return Err(UpdateError::new(Failure::InstallFailed, e)),
        }
        let mut source = fs::File::open(
            std::env::current_exe().map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?,
        )
        .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        let mut copy = fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o700)
            .open(&helper)
            .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
        std::io::copy(&mut source, &mut copy)
            .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
        copy.sync_all()
            .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
        Ok(helper)
    }
    #[cfg(not(unix))]
    {
        let _ = candidate;
        Err(Failure::UnsupportedInstallation.into())
    }
}
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
/// Whether a committed journal may be carried forward by this process.
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
}
async fn recheck(candidate: &Candidate, running: &str) -> Recheck {
    match tokio::time::timeout(RECHECK_DEADLINE, feed::fetch(candidate.channel, running)).await {
        Err(_) => Recheck::Unavailable(UpdateError::new(
            Failure::CheckFailed,
            "The final offer check did not finish in time",
        )),
        Ok(Err(error)) => Recheck::Unavailable(error),
        Ok(Ok(Some(offer))) if offer.candidate.release_id == candidate.release_id => {
            Recheck::Confirmed
        }
        Ok(Ok(_)) => Recheck::Withdrawn,
    }
}
/// Whether an unreachable feed may be tolerated because the offer was confirmed recently.
fn offer_is_fresh(confirmed_at: Option<u64>, now: u64) -> bool {
    confirmed_at.is_some_and(|at| now.saturating_sub(at) <= OFFER_FRESHNESS.as_secs())
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
    // A committed handoff is honoured before any new preparation is considered.
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
        Recheck::Unavailable(_) => {}
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
        let prepared = installer::prepared_path(&target, &candidate)?;
        let ready = staging::read_ready()?.ok_or(Failure::CandidateMissing)?;
        if ready.candidate.release_id != candidate.release_id
            || hash(&executable(&prepared))? != ready.executable_hash
        {
            return Err(Failure::VerificationFailed.into());
        }
        let t = Transaction {
            format_version: 1,
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
pub async fn resume(app: &AppHandle, error: Option<UpdateError>) {
    let service = app.state::<UpdateService>();
    let mut inner = service.inner.lock().await;
    let committed = read().ok().flatten().is_some_and(|t| t.phase == Phase::Committed);
    resume_state(&mut inner.state, error.clone(), committed);
    if let (Some(error), Some(candidate)) = (error, inner.state.candidate.clone()) {
        if error.code == Failure::ActivationFailed {
            invalidate_preparation(&mut inner, &candidate, error);
        }
    }
    emit(app, &mut inner.state);
}
fn resume_state(
    state: &mut super::NativeUpdateState,
    error: Option<UpdateError>,
    committed: bool,
) {
    match error.as_ref().map(|error| error.code) {
        Some(Failure::RecoveryRequired) => {
            state.staged_release_id = None;
            state.installation = Installation::RecoveryRequired;
        }
        Some(Failure::ActivationFailed) => {
            state.staged_release_id = None;
            state.installation = Installation::Failed;
        }
        _ if committed => state.installation = Installation::Committed,
        _ if state.staged_release_id.is_some() => state.installation = Installation::Staged,
        _ => {}
    }
    state.last_error = error;
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

#[cfg(target_os = "macos")]
fn activate(t: &mut Transaction) -> Result<(), UpdateError> {
    let expected = t.next_hash.clone();
    let expected_bundle = t.next_bundle_hash.clone();
    activate_using(
        t,
        |target, candidate| {
            let prepared = installer::prepared_path(target, candidate)?;
            // The preparation receipt binds every file, link, and permission.
            if hash(&executable(&prepared)).is_ok_and(|actual| actual == expected)
                && installer::bundle_hash(&prepared).is_ok_and(|actual| actual == expected_bundle)
                && installer::verify_bundle(&prepared).is_ok()
            {
                return Ok(());
            }
            let rebuilt = installer::prepare_at(target, candidate)?;
            if rebuilt.executable_hash != expected || rebuilt.bundle_hash != expected_bundle {
                return Err(Failure::VerificationFailed.into());
            }
            Ok(())
        },
        installer::exchange,
        write,
    )
}
#[cfg(any(target_os = "macos", all(test, unix)))]
fn activate_using(
    t: &mut Transaction,
    prepare: impl FnOnce(&Path, &Candidate) -> Result<(), UpdateError>,
    exchange: impl FnOnce(&Path, &Path) -> Result<(), UpdateError>,
    record: impl FnOnce(&Transaction) -> Result<(), UpdateError>,
) -> Result<(), UpdateError> {
    let installed = hash(&executable(&t.target))?;
    if installed != t.next_hash {
        if installed != t.previous_hash {
            return Err(Failure::CandidateChanged.into());
        }
        prepare(&t.target, &t.candidate)?;
        let prepared = installer::prepared_path(&t.target, &t.candidate)?;
        if hash(&executable(&prepared))? != t.next_hash {
            return Err(Failure::VerificationFailed.into());
        }
        exchange(&t.target, &prepared)?;
    } else if installer::bundle_hash(&t.target)? != t.next_bundle_hash {
        return Err(Failure::VerificationFailed.into());
    }
    t.phase = Phase::Activated;
    record(t)
}
/// Records how activation ended; the phase follows the bytes actually installed.
#[cfg(target_os = "macos")]
fn record_activation_failure(t: &mut Transaction, error: &UpdateError) -> Result<(), UpdateError> {
    t.phase = if hash(&executable(&t.target)).is_ok_and(|installed| installed == t.next_hash) {
        Phase::Activated
    } else {
        Phase::Failed
    };
    t.error = Some(error.to_string());
    write(t)
}
#[cfg(target_os = "macos")]
fn installed_is_known(t: &Transaction) -> bool {
    hash(&executable(&t.target)).is_ok_and(|hash| hash == t.previous_hash || hash == t.next_hash)
}
pub fn run_helper(id: &str) -> Result<(), UpdateError> {
    #[cfg(target_os = "macos")]
    {
        let mut t = read()?.ok_or(Failure::JournalUnavailable)?;
        if t.id != id || t.phase != Phase::Committed {
            return Err(Failure::InvalidTransition.into());
        }
        if persistence::update_dir()?
            .file_name()
            .and_then(|name| name.to_str())
            != Some(persistence::installation_id(&t.target).as_str())
        {
            return Err(Failure::InvalidTransition.into());
        }
        let Some((_gate, _lease)) = helper_admission(&t.target)? else {
            // Another instance stays open; its quit or a later launch applies the commitment.
            if t.relaunch {
                reopen(&t.target)?;
            }
            return Ok(());
        };
        // Another startup may have completed the committed transaction while this helper waited.
        t = read()?.ok_or(Failure::JournalUnavailable)?;
        if t.id != id {
            return Err(Failure::InvalidTransition.into());
        }
        if t.phase == Phase::Committed {
            if let Err(error) = activate(&mut t) {
                let recorded = record_activation_failure(&mut t, &error);
                if t.relaunch && installed_is_known(&t) {
                    reopen(&t.target)?;
                }
                return Err(match recorded {
                    Ok(()) => error,
                    Err(journal) => error.with_context(journal),
                });
            }
        }
        if t.phase == Phase::Activated && t.relaunch {
            t.relaunch = false;
            // The application reopens even when its receipt cannot be updated.
            let recorded = write(&t);
            reopen(&t.target)?;
            recorded?;
        }
        Ok(())
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = id;
        Err(Failure::UnsupportedInstallation.into())
    }
}
#[cfg(target_os = "macos")]
fn reopen(target: &Path) -> Result<(), UpdateError> {
    installer::verify_bundle(target)?;
    let status = std::process::Command::new("/usr/bin/open")
        .arg(target)
        .status()
        .map_err(|e| UpdateError::new(Failure::ActivationFailed, e))?;
    if !status.success() {
        return Err(Failure::ActivationFailed.into());
    }
    Ok(())
}
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
        let previous = installer::prepared_path(&t.target, &t.candidate)?;
        if staging::read_ready()?
            .is_some_and(|ready| ready.candidate.release_id == t.candidate.release_id)
        {
            staging::forget_ready()?;
        }
        if inner.state.installation == Installation::AwaitingStartup {
            inner.state.installation = Installation::None;
            emit(app, &mut inner.state);
        }
        drop(inner);
        tauri::async_runtime::spawn_blocking(move || match fs::remove_dir_all(previous) {
            Ok(()) => Ok(()),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
            Err(error) => Err(UpdateError::new(Failure::StateUnavailable, error)),
        })
        .await
        .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))??;
    }
    Ok(())
}
/// Takes the installation lease before any window exists and completes a committed handoff.
///
/// An error aborts the launch: this process is the wrong binary for the installed bundle, the
/// journal is unreadable, or the installation holds mixed files. A lease that cannot be taken
/// launches without update coordination and reports why.
#[cfg(target_os = "macos")]
pub fn launch() -> Result<Launch, UpdateError> {
    let Ok(target) = installer::bundle() else {
        return Ok(Launch {
            lease: None,
            error: None,
        });
    };
    let uncoordinated = |error: UpdateError| -> Result<Launch, UpdateError> {
        Ok(Launch {
            lease: None,
            error: Some(error),
        })
    };
    let _gate = match installer::acquire_gate(&target) {
        Ok(gate) => gate,
        Err(error) if error.code == Failure::StateUnavailable => return uncoordinated(error),
        Err(error) => return Err(error),
    };
    let lease = match installer::acquire_lease(&target, true, true) {
        Ok(lease) => lease,
        Err(error) if error.code == Failure::InvalidTransition => {
            // An existing process already protects the installed files.
            let shared = installer::acquire_lease(&target, false, false)?;
            require_running_version(&target)?;
            return Ok(Launch {
                lease: Some(shared),
                error: None,
            });
        }
        Err(error) if error.code == Failure::StateUnavailable => return uncoordinated(error),
        Err(error) => return Err(error),
    };
    let mut error = None;
    if let Some(mut t) = read()?.filter(|t| t.target == target) {
        if superseded(&t)? {
            require_running_version(&target)?;
            installer::verify_bundle(&target)?;
            retire(&t)?;
            installer::downgrade(&lease)?;
            return Ok(Launch {
                lease: Some(lease),
                error: None,
            });
        }
        if t.phase == Phase::Committed {
            if let Err(failure) = activate(&mut t) {
                let recorded = record_activation_failure(&mut t, &failure);
                if !installed_is_known(&t) {
                    return Err(failure);
                }
                error = Some(match recorded {
                    Ok(()) => failure,
                    Err(journal) => failure.with_context(journal),
                });
            }
        }
        if t.phase == Phase::Activated && t.candidate.version != env!("PAINTED_WOLF_VERSION") {
            // The exchange completed beneath this older binary; the installed version takes over.
            if !t.recovery_relaunch_attempted {
                t.recovery_relaunch_attempted = true;
                let _ = write(&t);
                std::process::Command::new("/usr/bin/open")
                    .arg("-n")
                    .arg(&target)
                    .spawn()
                    .map_err(|e| UpdateError::new(Failure::ActivationFailed, e))?;
                std::process::exit(0);
            }
        }
    }
    require_running_version(&target)?;
    installer::downgrade(&lease)?;
    Ok(Launch {
        lease: Some(lease),
        error,
    })
}
#[cfg(not(target_os = "macos"))]
pub fn launch() -> Result<Launch, UpdateError> {
    Ok(Launch {
        lease: None,
        error: None,
    })
}
/// Polls for the exclusive lease without holding the launch gate across the wait.
#[cfg(target_os = "macos")]
fn helper_admission(
    target: &Path,
) -> Result<Option<(installer::InstallationLease, installer::InstallationLease)>, UpdateError> {
    let deadline = std::time::Instant::now() + HELPER_ADMISSION;
    loop {
        if let Some(leases) = installer::try_activation(target)? {
            return Ok(Some(leases));
        }
        if std::time::Instant::now() >= deadline {
            return Ok(None);
        }
        std::thread::sleep(HELPER_POLL);
    }
}
/// The process must be the binary of the bundle it launched from.
#[cfg(target_os = "macos")]
fn require_running_version(target: &Path) -> Result<(), UpdateError> {
    if installer::product_version(target, Failure::StateUnavailable)? != env!("PAINTED_WOLF_VERSION") {
        return Err(UpdateError::new(
            Failure::CandidateChanged,
            "The installed application changed while this process was launching. Open the installed application again.",
        ));
    }
    Ok(())
}
/// The installed bundle matches neither side of an unconfirmed transaction: it was reinstalled.
#[cfg(any(target_os = "macos", all(test, unix)))]
fn superseded(t: &Transaction) -> Result<bool, UpdateError> {
    let installed = hash(&executable(&t.target))?;
    Ok(installed != t.next_hash && (t.phase == Phase::Activated || installed != t.previous_hash))
}

pub(super) fn retained_release() -> Result<Option<String>, UpdateError> {
    Ok(read()?
        .filter(|t| {
            matches!(
                t.phase,
                Phase::Prepared | Phase::Committed | Phase::Activated
            )
        })
        .map(|t| t.candidate.release_id))
}
/// Projects the journal into startup state and names a release blocked by a recorded failure.
pub(super) fn restore(
    state: &mut super::NativeUpdateState,
) -> Result<Option<String>, UpdateError> {
    let Some(t) = read()? else {
        return Ok(None);
    };
    if !installer::bundle().is_ok_and(|bundle| bundle == t.target) {
        return Ok(None);
    }
    match t.phase {
        Phase::Committed => {
            state.staged_release_id = Some(t.candidate.release_id.clone());
            state.candidate = Some(t.candidate);
            state.discovery = super::Discovery::Available;
            state.installation = Installation::Committed;
            Ok(None)
        }
        Phase::Activated if state.installation != Installation::Staged => {
            state.installation = Installation::AwaitingStartup;
            state.staged_release_id = None;
            state.candidate = None;
            Ok(None)
        }
        Phase::Failed => {
            state.candidate = Some(t.candidate.clone());
            state.staged_release_id = None;
            state.installation = Installation::Failed;
            state.last_error = Some(UpdateError::new(
                Failure::ActivationFailed,
                t.error.unwrap_or_else(|| "Interrupted activation".into()),
            ));
            Ok(Some(t.candidate.release_id))
        }
        _ => Ok(None),
    }
}

pub fn recovery_guidance(error: &UpdateError) -> String {
    if error.code == Failure::CandidateChanged {
        return "Painted Wolf Code was updated while this copy was opening. Close this copy and open the installed application again. Your saved work is unchanged.".into();
    }
    let journal = journal::active_path()
        .map(|path| path.display().to_string())
        .unwrap_or_else(|_| {
            "the transaction.json file in the application's updates directory".into()
        });
    format!("Painted Wolf Code could not safely recover an application update. The update record has been preserved.\n\nRecord: {journal}\n\nQuit every instance of Painted Wolf Code. Keep a copy of this record for support. If the record is unreadable or from an incompatible beta, move it out of the updates directory, then reinstall the same or a newer signed release at the same application location before launching. Reinstalling alone does not replace the update record. Do not remove your saved-work or database files.\n\n{error}")
}

#[cfg(all(test, unix))]
mod tests {
    use super::*;
    fn fixture(root: &Path) -> Transaction {
        let candidate = super::super::tests::candidate("1.1.0");
        let target = root.join("Painted Wolf Code.app");
        let staged = installer::prepared_path(&target, &candidate).unwrap();
        for (bundle, contents) in [(&target, "old engine"), (&staged, "new engine")] {
            let exe = executable(bundle);
            fs::create_dir_all(exe.parent().unwrap()).unwrap();
            fs::write(exe, contents).unwrap();
        }
        Transaction {
            format_version: 1,
            id: uuid::Uuid::new_v4().to_string(),
            candidate,
            previous_hash: hash(&executable(&target)).unwrap(),
            next_hash: hash(&executable(&staged)).unwrap(),
            next_bundle_hash: installer::bundle_hash(&staged).unwrap(),
            target,
            phase: Phase::Committed,
            relaunch: false,
            recovery_relaunch_attempted: false,
            error: None,
        }
    }
    fn fresh_state() -> super::super::NativeUpdateState {
        super::super::NativeUpdateState::new(
            "1.0.0".into(),
            super::super::UpdateChannel::Stable,
            super::super::InstallSource::DirectDownload,
        )
    }
    #[test]
    fn external_reinstall_retires_unconfirmed_activation_even_when_returning_to_previous_version() {
        let root = crate::test_support::TempDir::new("update-reinstall");
        let mut t = fixture(&root);
        assert!(!superseded(&t).unwrap());
        t.phase = Phase::Activated;
        assert!(superseded(&t).unwrap());
        fs::write(executable(&t.target), "new engine").unwrap();
        assert!(!superseded(&t).unwrap());
        fs::write(executable(&t.target), "external signed replacement").unwrap();
        assert!(superseded(&t).unwrap());
    }
    #[test]
    fn a_committed_journal_is_reused_only_for_its_own_installation_and_release() {
        let root = crate::test_support::TempDir::new("update-committed-reuse");
        let t = fixture(&root);
        committed_reuse(&t, &t.target, None).unwrap();
        committed_reuse(&t, &t.target, Some(&t.candidate.release_id)).unwrap();
        assert_eq!(
            committed_reuse(&t, &t.target, Some("other"))
                .unwrap_err()
                .code,
            Failure::CandidateChanged
        );
        assert_eq!(
            committed_reuse(&t, &root.join("Elsewhere.app"), None)
                .unwrap_err()
                .code,
            Failure::InvalidTransition
        );
        let mut prepared = t.clone();
        prepared.phase = Phase::Prepared;
        assert!(committed_reuse(&prepared, &t.target, None).is_err());
    }
    #[test]
    fn a_recently_confirmed_offer_survives_an_unreachable_feed() {
        let now = 1_000_000;
        assert!(offer_is_fresh(Some(now - 3_600), now));
        assert!(offer_is_fresh(Some(now - OFFER_FRESHNESS.as_secs()), now));
        assert!(!offer_is_fresh(Some(now - OFFER_FRESHNESS.as_secs() - 1), now));
        assert!(!offer_is_fresh(None, now));
    }
    #[test]
    fn failed_spawn_with_failed_record_requires_recovery_instead_of_retry() {
        let root = crate::test_support::TempDir::new("update-spawn-record-failure");
        let mut transaction = fixture(&root);
        let error = record_spawn_failure(
            &mut transaction,
            std::io::Error::other("spawn failed"),
            |attempt| {
                assert_eq!(attempt.phase, Phase::Failed);
                Err(Failure::JournalUnavailable.into())
            },
        );
        assert_eq!(error.code, Failure::RecoveryRequired);
        let mut state = fresh_state();
        state.candidate = Some(transaction.candidate.clone());
        state.staged_release_id = Some(transaction.candidate.release_id);
        state.installation = Installation::AwaitingExit;
        resume_state(&mut state, Some(error), true);
        state.refresh_capabilities(true);
        assert_eq!(state.installation, Installation::RecoveryRequired);
        assert!(!state.capabilities.can_check);
        assert!(!state.capabilities.can_download);
        assert!(!state.capabilities.can_restart_to_update);
        assert!(!state.capabilities.can_install_automatically);
    }
    #[test]
    fn resumed_state_follows_the_journal_and_the_failure() {
        let candidate = super::super::tests::candidate("1.1.0");
        for (failure, committed, installation, restart, download) in [
            (Some(Failure::ActivationFailed), false, Installation::Failed, false, true),
            (Some(Failure::EngineStopFailed), false, Installation::Staged, true, false),
            (Some(Failure::Cancelled), false, Installation::Staged, true, false),
            (Some(Failure::Cancelled), true, Installation::Committed, true, false),
            (None, true, Installation::Committed, true, false),
        ] {
            let mut state = fresh_state();
            state.staged_release_id = Some(candidate.release_id.clone());
            state.candidate = Some(candidate.clone());
            state.installation = Installation::AwaitingExit;
            resume_state(&mut state, failure.map(UpdateError::from), committed);
            state.refresh_capabilities(true);
            assert_eq!(state.installation, installation);
            assert_eq!(state.capabilities.can_restart_to_update, restart);
            assert_eq!(state.capabilities.can_download, download);
            assert_eq!(state.last_error.map(|error| error.code), failure);
        }
    }
    #[test]
    fn recovery_after_exchange_never_exchanges_the_old_bundle_back() {
        let root = crate::test_support::TempDir::new("update-crash-after-swap");
        let mut t = fixture(&root);
        let before = t.clone();
        assert!(activate_using(
            &mut t,
            |_, _| Ok(()),
            swap_directories,
            |_| Err(Failure::JournalUnavailable.into())
        )
        .is_err());
        assert_eq!(t.phase, Phase::Activated);
        let mut recovered = before;
        activate_using(
            &mut recovered,
            |_, _| panic!("already activated"),
            |_, _| panic!("must never roll back"),
            |_| Ok(()),
        )
        .unwrap();
        assert_eq!(recovered.phase, Phase::Activated);
        assert_eq!(
            hash(&executable(&recovered.target)).unwrap(),
            recovered.next_hash
        );
        assert_eq!(
            hash(&executable(
                &installer::prepared_path(&recovered.target, &recovered.candidate).unwrap()
            ))
            .unwrap(),
            recovered.previous_hash
        );
    }
    #[test]
    fn failed_exchange_leaves_current_bundle_and_committed_intent() {
        let root = crate::test_support::TempDir::new("update-failed-swap");
        let mut t = fixture(&root);
        assert!(activate_using(
            &mut t,
            |_, _| Ok(()),
            |_, _| Err(Failure::ActivationFailed.into()),
            |_| panic!("not activated")
        )
        .is_err());
        assert_eq!(t.phase, Phase::Committed);
        assert_eq!(hash(&executable(&t.target)).unwrap(), t.previous_hash);
    }
    #[test]
    fn externally_replaced_installation_is_never_overwritten() {
        let root = crate::test_support::TempDir::new("update-external-replacement");
        let mut t = fixture(&root);
        fs::write(executable(&t.target), "different installation").unwrap();
        let error = activate_using(
            &mut t,
            |_, _| panic!("unknown installation"),
            |_, _| panic!("no exchange"),
            |_| panic!("no receipt"),
        )
        .unwrap_err();
        assert_eq!(error.code, Failure::CandidateChanged);
    }
    /// A portable stand-in for the atomic exchange, for tests that run on every unix host.
    fn swap_directories(left: &Path, right: &Path) -> Result<(), UpdateError> {
        let parked = left.with_extension("parked");
        fs::rename(left, &parked)
            .and_then(|_| fs::rename(right, left))
            .and_then(|_| fs::rename(&parked, right))
            .map_err(|e| UpdateError::new(Failure::ActivationFailed, e))
    }
    #[cfg(target_os = "macos")]
    #[test]
    fn launch_refuses_a_binary_whose_installed_bundle_has_changed() {
        let root = crate::test_support::TempDir::new("update-launch-version");
        fs::create_dir_all(root.join("Contents/Resources")).unwrap();
        let version = root.join(installer::PRODUCT_VERSION_RESOURCE);
        fs::write(&version, env!("PAINTED_WOLF_VERSION")).unwrap();
        require_running_version(&root).unwrap();
        fs::write(&version, "99.0.0").unwrap();
        assert_eq!(
            require_running_version(&root).unwrap_err().code,
            Failure::CandidateChanged
        );
    }
}
