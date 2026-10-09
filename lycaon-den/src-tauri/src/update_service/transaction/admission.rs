//! Launches and helpers share admission to one installed bundle.
use crate::update_service::{Failure, UpdateError};
#[cfg(any(target_os = "macos", all(test, unix)))]
use super::journal::{Phase, Transaction};
#[cfg(target_os = "macos")]
use super::{
    activation::{activate, installed_is_known, record_activation_failure},
    journal::{read, retire, write},
};
#[cfg(any(target_os = "macos", all(test, unix)))]
use crate::update_service::installer::{executable, hash};
#[cfg(target_os = "macos")]
use crate::update_service::{installer, persistence};
#[cfg(target_os = "macos")]
use std::{path::Path, time::Duration};

#[cfg(target_os = "macos")]
const HELPER_ADMISSION: Duration = Duration::from_secs(10 * 60);
#[cfg(target_os = "macos")]
const HELPER_POLL: Duration = Duration::from_millis(250);

#[cfg(target_os = "macos")]
pub type Lease = installer::InstallationLease;
#[cfg(not(target_os = "macos"))]
pub type Lease = ();

pub struct Launch {
    /// False when another running copy delays activation.
    pub install_at_startup: bool,
    /// The shared lifetime lease; absent outside an installed macOS bundle.
    pub lease: Option<Lease>,
    /// A recovery-relevant failure that did not prevent launching.
    pub error: Option<UpdateError>,
}

#[cfg(any(target_os = "macos", all(test, unix)))]
pub(super) fn should_reopen_after_wait(t: &Transaction, id: &str) -> bool {
    t.id == id && t.phase == Phase::Committed && t.relaunch
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
        let Some(mut lease) = helper_admission(&t.target)? else {
            // A later quit can revoke the restart while this helper waits.
            if let Some(current) = read()?.filter(|current| should_reopen_after_wait(current, id)) {
                reopen(&current.target)?;
            }
            return Ok(());
        };
        // A launch may have completed activation during the wait.
        t = read()?.ok_or(Failure::JournalUnavailable)?;
        if t.id != id {
            return Err(Failure::InvalidTransition.into());
        }
        if t.phase == Phase::Committed {
            if let Err(error) = activate(&mut t, &mut lease) {
                let recorded = record_activation_failure(&mut t, &error);
                drop(lease);
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
            drop(lease);
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
/// Coordinates recovery before any window opens; unsafe admission aborts launch.
#[cfg(target_os = "macos")]
pub fn launch() -> Result<Launch, UpdateError> {
    let Ok(target) = installer::bundle() else {
        return Ok(Launch {
            install_at_startup: false,
            lease: None,
            error: None,
        });
    };
    let mut lease = loop {
        let gate = installer::acquire_gate(&target)?;
        match installer::acquire_lease(&target, true, true) {
            Ok(lease) => {
                drop(gate);
                break lease;
            }
            Err(error) if error.code == Failure::InvalidTransition => {
                match installer::acquire_lease(&target, false, true) {
                    Ok(shared) => {
                        require_running_version(&target)?;
                        return Ok(Launch {
                            install_at_startup: false,
                            lease: Some(shared),
                            error: None,
                        });
                    }
                    Err(error) if error.code == Failure::InvalidTransition => {
                        drop(gate);
                        std::thread::sleep(HELPER_POLL);
                    }
                    Err(error) => return Err(error),
                }
            }
            Err(error) => return Err(error),
        }
    };
    let mut error = None;
    if let Some(mut t) = read()?.filter(|t| t.target == target) {
        if superseded(&t)? {
            require_running_version(&target)?;
            installer::verify_bundle(&target)?;
            retire(&t)?;
            let _gate = installer::acquire_gate(&target)?;
            installer::downgrade(&lease)?;
            return Ok(Launch {
                install_at_startup: true,
                lease: Some(lease),
                error: None,
            });
        }
        if t.phase == Phase::Committed {
            if let Err(failure) = activate(&mut t, &mut lease) {
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
            // The installed version takes over after an exchange beneath this process.
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
    let _gate = installer::acquire_gate(&target)?;
    require_running_version(&target)?;
    installer::downgrade(&lease)?;
    Ok(Launch {
        install_at_startup: true,
        lease: Some(lease),
        error,
    })
}
#[cfg(not(target_os = "macos"))]
pub fn launch() -> Result<Launch, UpdateError> {
    Ok(Launch {
        install_at_startup: false,
        lease: None,
        error: None,
    })
}
/// Polls for the exclusive lease without holding the launch gate across the wait.
#[cfg(target_os = "macos")]
fn helper_admission(target: &Path) -> Result<Option<installer::InstallationLease>, UpdateError> {
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
pub(super) fn require_running_version(target: &Path) -> Result<(), UpdateError> {
    if installer::product_version(target, Failure::StateUnavailable)?
        != env!("PAINTED_WOLF_VERSION")
    {
        return Err(UpdateError::new(
            Failure::CandidateChanged,
            "The installed application changed while this process was launching. Open the installed application again.",
        ));
    }
    Ok(())
}
/// An activated transaction is superseded when its installed bytes are replaced.
#[cfg(any(target_os = "macos", all(test, unix)))]
pub(super) fn superseded(t: &Transaction) -> Result<bool, UpdateError> {
    let installed = hash(&executable(&t.target))?;
    Ok(installed != t.next_hash && (t.phase == Phase::Activated || installed != t.previous_hash))
}
