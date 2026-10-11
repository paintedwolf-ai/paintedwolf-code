//! The native host runs update discovery, preparation, and lifecycle activation.
pub(crate) mod check;
pub(crate) mod download;
mod error;
pub(crate) mod feed;
mod feed_state;
mod installer;
mod model;
mod persistence;
pub(crate) mod preferences;
mod record_lock;
mod scheduler;
mod staging;
pub(crate) mod startup;
#[cfg(test)]
mod tests;
pub(crate) mod transaction;
pub mod verification;
pub use error::{UpdateError, UpdateErrorCode};
pub use model::*;
use std::{
    sync::atomic::{AtomicU64, Ordering},
    time::Duration,
};
use tauri::{AppHandle, Emitter};
use UpdateErrorCode as Failure;
const CHECK_REQUEST_TIMEOUT: Duration = Duration::from_secs(30);
const DOWNLOAD_REQUEST_TIMEOUT: Duration = Duration::from_secs(30 * 60);
const UPDATE_STATE_EVENT: &str = "update-state-changed";

pub(super) struct Inner {
    state: NativeUpdateState,
    preferences: persistence::Preferences,
    blocked_release: Option<String>,
    preferences_writable: bool,
    manual_preparation: bool,
}
pub struct UpdateService {
    inner: tokio::sync::Mutex<Inner>,
    generation: AtomicU64,
    wake: tokio::sync::watch::Sender<u64>,
    preparation_generation: AtomicU64,
    preparation: std::sync::Arc<tokio::sync::Mutex<()>>,
    activation: std::sync::Arc<tokio::sync::Mutex<()>>,
    startup_ready: tokio::sync::watch::Sender<bool>,
    install_at_startup: bool,
    engine_admission: std::sync::Arc<tokio::sync::RwLock<()>>,
    /// Held for the life of the process so no exchange can happen beneath it.
    _lease: Option<transaction::Lease>,
}
fn default_channel(version: &str) -> Result<UpdateChannel, UpdateError> {
    let version = semver::Version::parse(version)
        .map_err(|e| UpdateError::new(Failure::InvalidVersion, e))?;
    Ok(if version.pre.is_empty() {
        UpdateChannel::Stable
    } else {
        UpdateChannel::Preview
    })
}
impl UpdateService {
    pub fn new(version: String, launch: transaction::Launch) -> Self {
        let (source, receipt_channel, error) = persistence::detect_install_source();
        let channel = receipt_channel
            .unwrap_or_else(|| default_channel(&version).expect("compiled product version"));
        let mut state = NativeUpdateState::new(version, channel, source);
        state.last_error = error;
        // A process outside an installed bundle has nothing to coordinate.
        state.coordinated = launch.lease.is_some() || installer::bundle().is_err();
        let mut preferences = persistence::Preferences {
            format_version: 2,
            automatic_updates_enabled: false,
            channel,
            rollout_bucket: check::draw_rollout_bucket(),
        };
        let mut preferences_writable = false;
        match persistence::preferences_dir().and_then(|dir| {
            persistence::load_preferences(&dir, channel, preferences.rollout_bucket)
        }) {
            Ok(p) => {
                preferences_writable = true;
                state.automatic_updates_enabled = p.automatic_updates_enabled;
                state.channel = p.channel;
                preferences = p;
            }
            Err(e) => {
                state.automatic_updates_enabled = false;
                state.last_error = Some(e);
            }
        }
        if let Err(e) = persistence::preferences_dir()
            .and_then(|dir| persistence::reconcile_legacy(&dir, &state.running_version))
        {
            state.last_error = Some(e);
        }
        if let Err(e) = staging::restore(&mut state) {
            state.last_error = Some(e);
        }
        let blocked_release = match transaction::restore(&mut state) {
            Ok(id) => id,
            Err(error) => {
                state.last_error = Some(error);
                None
            }
        };
        let blocked_release = match staging::rejected() {
            Ok(rejected) => blocked_release.or(rejected),
            Err(error) => {
                state.last_error = Some(error);
                blocked_release
            }
        };
        if let Some(error) = launch.error {
            state.last_error = Some(error);
        }
        state.refresh_capabilities(installer::supported());
        Self {
            inner: tokio::sync::Mutex::new(Inner {
                state,
                preferences,
                blocked_release,
                preferences_writable,
                manual_preparation: false,
            }),
            generation: AtomicU64::new(0),
            wake: tokio::sync::watch::channel(0).0,
            preparation_generation: AtomicU64::new(0),
            startup_ready: tokio::sync::watch::channel(false).0,
            install_at_startup: launch.install_at_startup,
            engine_admission: std::sync::Arc::new(tokio::sync::RwLock::new(())),
            preparation: std::sync::Arc::new(tokio::sync::Mutex::new(())),
            activation: std::sync::Arc::new(tokio::sync::Mutex::new(())),
            _lease: launch.lease,
        }
    }
    pub async fn automatic_install_ready(&self) -> bool {
        let mut inner = self.inner.lock().await;
        inner.state.refresh_capabilities(installer::supported());
        inner.state.capabilities.can_install_automatically
    }
    /// Drains engine starts in flight and refuses new ones until startup finishes again.
    pub async fn close_startup(&self) {
        let _admission = self.engine_admission.write().await;
        self.startup_ready.send_replace(false);
    }
    pub async fn startup_admission(&self) -> tokio::sync::OwnedRwLockReadGuard<()> {
        loop {
            self.wait_for_startup().await;
            let admission = self.engine_admission.clone().read_owned().await;
            if *self.startup_ready.borrow() {
                return admission;
            }
        }
    }
    pub async fn wait_for_startup(&self) {
        let mut ready = self.startup_ready.subscribe();
        let _ = ready.wait_for(|ready| *ready).await;
    }
    pub async fn finish_startup(&self, app: &AppHandle) {
        let mut inner = self.inner.lock().await;
        inner.state.startup_pending = false;
        emit(app, &mut inner.state);
        self.startup_ready.send_replace(true);
    }
    fn current(&self, generation: u64) -> bool {
        self.generation.load(Ordering::Acquire) == generation
    }
    fn wake_scheduler(&self) {
        self.wake.send_modify(|revision| *revision += 1);
    }
    /// Invalidates checks in flight; preparation has its own generation.
    fn cancel(&self, state: &mut NativeUpdateState) {
        if state.discovery == Discovery::Checking {
            state.discovery = Discovery::Idle;
        }
        self.generation.fetch_add(1, Ordering::AcqRel);
        self.wake.send_modify(|revision| *revision += 1);
    }
}
fn emit(app: &AppHandle, state: &mut NativeUpdateState) {
    state.refresh_capabilities(installer::supported());
    state.revision += 1;
    let _ = app.emit(UPDATE_STATE_EVENT, &*state);
}

pub(crate) fn initialize_helper(installation: &str) -> Result<(), String> {
    persistence::helper_installation(installation).map_err(|error| error.to_string())
}

#[cfg(test)]
mod https_transport_tests;
