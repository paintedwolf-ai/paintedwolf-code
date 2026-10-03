//! A committed transaction survives the native parent; an uncommitted quit does not.
use super::{
    check, emit, installer, persistence, staging, Candidate, Failure, Installation, UpdateError,
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
use journal::{active_path, archive_receipt, read, write, Phase, Transaction};
pub fn clear_failed(release: &str) -> Result<(), UpdateError> {
    if let Some(t) = read()? {
        if t.phase == Phase::Failed && t.candidate.release_id == release {
            archive_receipt(&t)?;
            fs::remove_file(active_path()?)
                .map_err(|e| UpdateError::new(Failure::JournalUnavailable, e))?;
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
    pub async fn revalidate(&self, app: &AppHandle) -> Result<(), UpdateError> {
        let t = &self.transaction;
        let offer = tokio::time::timeout(
            Duration::from_secs(3),
            check::fetch_candidate(
                app,
                env!("PAINTED_WOLF_VERSION").into(),
                t.candidate.channel,
            ),
        )
        .await
        .map_err(|_| UpdateError::from(Failure::CheckFailed))??;
        if !offer.is_some_and(|(candidate, _)| candidate.release_id == t.candidate.release_id) {
            withdraw(app).await?;
            return Err(Failure::ReleaseWithdrawn.into());
        }
        Ok(())
    }

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
    let explicit = expected.is_some();
    let candidate = {
        let mut inner = service.inner.lock().await;
        inner.state.refresh_capabilities(installer::supported());
        if !(if explicit {
            inner.state.capabilities.can_restart_to_update
        } else {
            inner.state.capabilities.can_install_automatically
        }) {
            return Ok(None);
        }
        inner
            .state
            .candidate
            .clone()
            .ok_or(Failure::CandidateMissing)?
    };
    if expected.is_some_and(|id| id != candidate.release_id) {
        return Err(Failure::CandidateChanged.into());
    }
    let (source, _, error) = persistence::detect_install_source();
    if let Some(error) = error {
        return Err(error);
    }
    if source != super::InstallSource::DirectDownload {
        return Err(Failure::PackageManaged.into());
    }
    let current = service.inner.lock().await.state.running_version.clone();
    let offer = tokio::time::timeout(
        Duration::from_secs(3),
        check::fetch_candidate(app, current, candidate.channel),
    )
    .await
    .map_err(|_| UpdateError::from(Failure::CheckFailed))??;
    if !offer.is_some_and(|(fresh, _)| fresh.release_id == candidate.release_id) {
        withdraw(app).await?;
        return Err(Failure::ReleaseWithdrawn.into());
    }
    let inner = service.inner.lock().await;
    if inner.state.staged_release_id.as_ref() != Some(&candidate.release_id)
        || (!explicit && !inner.state.automatic_updates_enabled)
    {
        return Ok(None);
    }
    let (source, _, error) = persistence::detect_install_source();
    if let Some(error) = error {
        return Err(error);
    }
    if source != super::InstallSource::DirectDownload {
        return Err(Failure::PackageManaged.into());
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
        let helper = staging::root(&candidate)?.join("update-helper");
        fs::copy(
            std::env::current_exe().map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?,
            &helper,
        )
        .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
        fs::File::open(&helper)
            .and_then(|f| f.sync_all())
            .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
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
        if previous.phase == Phase::Committed
            || (previous.phase == Phase::Activated
                && (previous.target != t.target || t.previous_hash != previous.next_hash))
        {
            return Err(Failure::InvalidTransition.into());
        }
        archive_receipt(&previous)?;
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
async fn withdraw(app: &AppHandle) -> Result<(), UpdateError> {
    let service = app.state::<UpdateService>();
    let mut inner = service.inner.lock().await;
    let error = staging::forget_ready().err();
    inner.state.candidate = None;
    inner.state.installation = Installation::None;
    inner.state.staged_release_id = None;
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
    resume_state(&mut inner.state, error.clone());
    if let (Some(error), Some(candidate)) = (error, inner.state.candidate.clone()) {
        if error.code == Failure::ActivationFailed {
            invalidate_preparation(&mut inner, &candidate, error);
        }
    }
    emit(app, &mut inner.state);
}
fn resume_state(state: &mut super::NativeUpdateState, error: Option<UpdateError>) {
    if error
        .as_ref()
        .is_some_and(|error| error.code == Failure::RecoveryRequired)
    {
        state.staged_release_id = None;
        state.installation = Installation::RecoveryRequired;
    } else if error
        .as_ref()
        .is_some_and(|error| error.code == Failure::ActivationFailed)
    {
        state.staged_release_id = None;
        state.installation = Installation::Failed;
    } else if state.staged_release_id.is_some() {
        state.installation = Installation::Staged;
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
#[cfg(target_os = "macos")]
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
        let (_gate, _lease) = helper_admission(&t.target)?;
        // Another startup may have completed the committed transaction while this helper waited.
        t = read()?.ok_or(Failure::JournalUnavailable)?;
        if t.id != id {
            return Err(Failure::InvalidTransition.into());
        }
        if t.phase == Phase::Committed {
            if let Err(error) = activate(&mut t) {
                record_activation_failure(&mut t, &error)?;
                if t.relaunch
                    && hash(&executable(&t.target))
                        .is_ok_and(|hash| hash == t.previous_hash || hash == t.next_hash)
                {
                    reopen(&t.target)?;
                }
                return Err(error);
            }
        }
        if t.phase == Phase::Activated && t.relaunch {
            t.relaunch = false;
            write(&t)?;
            reopen(&t.target)?;
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
#[cfg(target_os = "macos")]
pub fn startup_lease() -> Result<Option<installer::InstallationLease>, UpdateError> {
    let Ok(target) = installer::bundle() else {
        return Ok(None);
    };
    let _gate = installer::acquire_gate(&target)?;
    let lease = match installer::acquire_lease(&target, true, true) {
        Ok(lease) => lease,
        Err(error) if error.code == Failure::InvalidTransition => {
            // An existing process already protects the installed files.
            let shared = installer::acquire_lease(&target, false, false)?;
            require_running_version(&target)?;
            return Ok(Some(shared));
        }
        Err(error) => return Err(error),
    };
    if let Some(mut t) = read()? {
        if t.target == target && superseded(&t)? {
            require_running_version(&target)?;
            installer::verify_bundle(&target)?;
            archive_receipt(&t)?;
            fs::remove_file(active_path()?)
                .map_err(|e| UpdateError::new(Failure::JournalUnavailable, e))?;
            return downgrade(lease).map(Some);
        }
        if t.target == target && t.phase == Phase::Committed {
            if let Err(error) = activate(&mut t) {
                record_activation_failure(&mut t, &error)?;
                if !hash(&executable(&target))
                    .is_ok_and(|hash| hash == t.previous_hash || hash == t.next_hash)
                {
                    return Err(error);
                }
            }
        }
        if t.target == target
            && t.phase == Phase::Activated
            && t.candidate.version != env!("PAINTED_WOLF_VERSION")
        {
            if t.recovery_relaunch_attempted {
                return Err(Failure::ActivationFailed.into());
            }
            t.recovery_relaunch_attempted = true;
            write(&t)?;
            std::process::Command::new("/usr/bin/open")
                .arg("-n")
                .arg(&target)
                .spawn()
                .map_err(|e| UpdateError::new(Failure::ActivationFailed, e))?;
            std::process::exit(0);
        }
    }
    require_running_version(&target)?;
    downgrade(lease).map(Some)
}
#[cfg(target_os = "macos")]
fn helper_admission(
    target: &Path,
) -> Result<(installer::InstallationLease, installer::InstallationLease), UpdateError> {
    let deadline = std::time::Instant::now() + Duration::from_secs(30);
    loop {
        match installer::try_activation(target)? {
            Some(leases) => return Ok(leases),
            None => {
                if std::time::Instant::now() >= deadline {
                    return Err(Failure::InvalidTransition.into());
                }
                std::thread::sleep(Duration::from_millis(100));
            }
        }
    }
}
#[cfg(target_os = "macos")]
fn require_running_version(target: &Path) -> Result<(), UpdateError> {
    let installed = fs::read_to_string(target.join("Contents/Resources/release-version"))
        .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
    if installed.trim() != env!("PAINTED_WOLF_VERSION") {
        return Err(UpdateError::new(Failure::CandidateChanged, "The installed application changed while this process was launching. Open the installed application again."));
    }
    Ok(())
}
fn superseded(t: &Transaction) -> Result<bool, UpdateError> {
    let installed = hash(&executable(&t.target))?;
    Ok(installed != t.next_hash && (t.phase == Phase::Activated || installed != t.previous_hash))
}
#[cfg(target_os = "macos")]
fn downgrade(
    lease: installer::InstallationLease,
) -> Result<installer::InstallationLease, UpdateError> {
    use std::os::fd::AsRawFd;
    // The gate excludes helpers while BSD releases and reacquires this lock.
    if unsafe { libc::flock(lease.0.as_raw_fd(), libc::LOCK_SH) } != 0 {
        return Err(Failure::StateUnavailable.into());
    }
    Ok(lease)
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
pub(super) fn restore_failure(
    state: &mut super::NativeUpdateState,
) -> Result<Option<String>, UpdateError> {
    let Some(t) = read()? else {
        return Ok(None);
    };
    if !installer::bundle().is_ok_and(|bundle| bundle == t.target) {
        return Ok(None);
    }
    if t.phase == Phase::Activated && state.installation != Installation::Staged {
        state.installation = Installation::AwaitingStartup;
        state.staged_release_id = None;
        state.candidate = None;
        return Ok(None);
    }
    if t.phase != Phase::Failed {
        return Ok(None);
    }
    state.candidate = Some(t.candidate.clone());
    state.staged_release_id = None;
    state.installation = Installation::Failed;
    state.last_error = Some(UpdateError::new(
        Failure::ActivationFailed,
        t.error.unwrap_or_else(|| "Interrupted activation".into()),
    ));
    Ok(Some(t.candidate.release_id))
}

#[cfg(all(test, target_os = "macos"))]
mod tests {
    use super::*;
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
    fn launch_refuses_a_binary_whose_installed_bundle_has_changed() {
        let root = crate::test_support::TempDir::new("update-launch-version");
        fs::create_dir_all(root.join("Contents/Resources")).unwrap();
        let version = root.join("Contents/Resources/release-version");
        fs::write(&version, env!("PAINTED_WOLF_VERSION")).unwrap();
        require_running_version(&root).unwrap();
        fs::write(&version, "99.0.0").unwrap();
        assert_eq!(
            require_running_version(&root).unwrap_err().code,
            Failure::CandidateChanged
        );
    }
    #[test]
    fn failed_spawn_with_failed_record_requires_recovery_instead_of_retry() {
        let root = crate::test_support::TempDir::new("update-spawn-record-failure");
        let mut transaction = fixture(&root);
        transaction.phase = Phase::Committed;
        let error = record_spawn_failure(
            &mut transaction,
            std::io::Error::other("spawn failed"),
            |attempt| {
                assert_eq!(attempt.phase, Phase::Failed);
                Err(Failure::JournalUnavailable.into())
            },
        );
        assert_eq!(error.code, Failure::RecoveryRequired);
        let mut state = super::super::NativeUpdateState::new(
            "1.0.0".into(),
            super::super::UpdateChannel::Stable,
            super::super::InstallSource::DirectDownload,
        );
        state.candidate = Some(transaction.candidate.clone());
        state.staged_release_id = Some(transaction.candidate.release_id);
        state.installation = Installation::AwaitingExit;
        resume_state(&mut state, Some(error));
        state.refresh_capabilities(true);
        assert_eq!(state.installation, Installation::RecoveryRequired);
        assert!(!state.capabilities.can_check);
        assert!(!state.capabilities.can_download);
        assert!(!state.capabilities.can_restart_to_update);
        assert!(!state.capabilities.can_install_automatically);
    }
    #[test]
    fn failed_helper_launch_offers_retry_while_deferred_shutdown_keeps_restart() {
        for (failure, restart, download) in [
            (Failure::ActivationFailed, false, true),
            (Failure::EngineStopFailed, true, false),
            (Failure::Cancelled, true, false),
        ] {
            let mut state = super::super::NativeUpdateState::new(
                "1.0.0".into(),
                super::super::UpdateChannel::Stable,
                super::super::InstallSource::DirectDownload,
            );
            let candidate = super::super::tests::candidate("1.1.0");
            state.staged_release_id = Some(candidate.release_id.clone());
            state.candidate = Some(candidate);
            state.installation = Installation::AwaitingExit;
            resume_state(&mut state, Some(failure.into()));
            state.refresh_capabilities(true);
            assert_eq!(state.capabilities.can_restart_to_update, restart);
            assert_eq!(state.capabilities.can_download, download);
            assert_eq!(state.last_error.unwrap().code, failure);
        }
    }
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
    #[test]
    fn recovery_after_exchange_never_exchanges_the_old_bundle_back() {
        let root = crate::test_support::TempDir::new("update-crash-after-swap");
        let mut t = fixture(&root);
        let before = t.clone();
        assert!(activate_using(
            &mut t,
            |_, _| Ok(()),
            installer::exchange,
            |_| Err(Failure::JournalUnavailable.into())
        )
        .is_err());
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
}

pub fn recovery_guidance(error: &UpdateError) -> String {
    if error.code == Failure::CandidateChanged {
        return "Painted Wolf Code was updated while this copy was opening. Close this copy and open the installed application again. Your saved work is unchanged.".into();
    }
    if error.code == Failure::StateUnavailable {
        return format!("Painted Wolf Code could not coordinate safe startup with the updater. Close other copies and reopen the application. If the problem continues, check access to the application's configuration directory.\n\n{error}");
    }
    let journal = journal::active_path()
        .map(|path| path.display().to_string())
        .unwrap_or_else(|_| {
            "the transaction.json file in the application's updates directory".into()
        });
    format!("Painted Wolf Code could not safely recover an application update. The update record has been preserved.\n\nRecord: {journal}\n\nQuit every instance of Painted Wolf Code. Keep a copy of this record for support. If the record is unreadable or from an incompatible beta, move it out of the updates directory, then reinstall the same or a newer signed release at the same application location before launching. Reinstalling alone does not replace the update record. Do not remove your saved-work or database files.\n\n{error}")
}
