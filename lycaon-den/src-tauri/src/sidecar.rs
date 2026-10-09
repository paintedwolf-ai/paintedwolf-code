//! Engine process lifecycle and authenticated connection state.

use self::daemon::attach_existing_daemon_in;
use self::layout::resolve_lycaon_binary;
use self::process::{child_alive, kill_child, spawn_sidecar_process};
use self::startup::{await_child_ready, StartupProgress};
use self::supervisor::{EngineState, ExitWatch};
use base64::{engine::general_purpose::URL_SAFE_NO_PAD, Engine as _};
use ed25519_dalek::{Signer, SigningKey};
use rand::rngs::OsRng;
use rand::TryRngCore;
use rand::RngCore;
use serde::{Deserialize, Serialize};
use std::collections::VecDeque;
use std::path::Path;
use std::process::{Child, Command, Stdio};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{mpsc, Mutex};
use std::time::Instant;

pub(crate) mod commands;
pub(crate) mod daemon;
mod diagnostics;
pub(crate) mod layout;
mod logging;
mod process;
mod startup;
pub(crate) mod supervisor;
#[cfg(test)]
mod tests;

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub struct SidecarInfo {
    pub port: u16,
    pub api_token: String,
    /// The launch this connection belongs to; 0 for an engine this shell
    /// attached to rather than launched.
    pub generation: u64,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "snake_case", tag = "code", content = "message")]
pub enum SidecarStartError {
    StoreLocked(String),
    EngineNotStarted(String),
    Cancelled(String),
    CredentialVaultLocked(String),
    CredentialVaultUninitialized(String),
    CredentialVaultUnlockFailed(String),
    CredentialVaultCorrupt(String),
    Failed(String),
}

impl From<String> for SidecarStartError {
    fn from(message: String) -> Self {
        Self::Failed(message)
    }
}

impl SidecarStartError {
    pub fn message(&self) -> &str {
        match self {
            Self::StoreLocked(message)
            | Self::EngineNotStarted(message)
            | Self::Cancelled(message)
            | Self::CredentialVaultLocked(message)
            | Self::CredentialVaultUninitialized(message)
            | Self::CredentialVaultUnlockFailed(message)
            | Self::CredentialVaultCorrupt(message)
            | Self::Failed(message) => message,
        }
    }
}

enum ChildState {
    Running,
    Exited(Option<i32>),
}

struct VaultPassword(Vec<u8>);

impl Drop for VaultPassword {
    fn drop(&mut self) {
        self.0.fill(0);
    }
}

type EngineStateObserver = Box<dyn Fn(&EngineState) + Send>;

pub struct SidecarState {
    pub process: Mutex<Option<Child>>,
    pub port: Mutex<u16>,
    pub api_token: Mutex<String>,
    /// Ephemeral key for this launch; it signs only presence payloads the
    /// presence module has checked.
    presence_signing_key: SigningKey,
    pub(crate) presence_gate: Mutex<()>,
    lifecycle: Mutex<()>,
    starting: AtomicBool,
    cancel_start: AtomicBool,
    vault_password: Mutex<Option<VaultPassword>>,
    /// Advances on every launch and every stop, so a supervisor watching an
    /// older process knows its exit is already accounted for.
    generation: AtomicU64,
    engine_state: Mutex<EngineState>,
    observer: Mutex<Option<EngineStateObserver>>,
    crashes: Mutex<VecDeque<Instant>>,
    watches: mpsc::Sender<ExitWatch>,
    watch_queue: Mutex<Option<mpsc::Receiver<ExitWatch>>>,
}

impl SidecarState {
    pub fn new() -> Self {
        let (watches, watch_queue) = mpsc::channel();
        Self {
            process: Mutex::new(None),
            port: Mutex::new(0),
            api_token: Mutex::new(String::new()),
            presence_signing_key: generate_presence_signing_key(),
            presence_gate: Mutex::new(()),
            lifecycle: Mutex::new(()),
            starting: AtomicBool::new(false),
            cancel_start: AtomicBool::new(false),
            vault_password: Mutex::new(None),
            generation: AtomicU64::new(0),
            engine_state: Mutex::new(EngineState::Idle),
            observer: Mutex::new(None),
            crashes: Mutex::new(VecDeque::new()),
            watches,
            watch_queue: Mutex::new(Some(watch_queue)),
        }
    }

    pub fn engine_state(&self) -> EngineState {
        self.engine_state.lock().unwrap().clone()
    }

    /// Receives every engine state change from now on.
    pub(crate) fn observe(&self, observer: EngineStateObserver) {
        *self.observer.lock().unwrap() = Some(observer);
    }

    /// The exits of launched engines, in launch order; taken once by the supervisor.
    pub(crate) fn take_exit_watches(&self) -> Option<mpsc::Receiver<ExitWatch>> {
        self.watch_queue.lock().unwrap().take()
    }

    fn publish(&self, next: EngineState) {
        {
            let mut current = self.engine_state.lock().unwrap();
            if *current == next {
                return;
            }
            *current = next.clone();
        }
        if let Some(observer) = self.observer.lock().unwrap().as_ref() {
            observer(&next);
        }
    }

    /// Forgets the current engine: its credentials and its supervision.
    fn retire(&self) {
        self.clear();
        self.generation.fetch_add(1, Ordering::AcqRel);
    }

    /// Counts unexpected exits in the crash window, including this one.
    fn record_crash(&self, at: Instant, window: std::time::Duration) -> u32 {
        let mut crashes = self.crashes.lock().unwrap();
        while crashes
            .front()
            .is_some_and(|first| at.duration_since(*first) > window)
        {
            crashes.pop_front();
        }
        crashes.push_back(at);
        crashes.len() as u32
    }

    pub fn cached_info(&self) -> Option<SidecarInfo> {
        let port = *self.port.lock().unwrap();
        let token = self.api_token.lock().unwrap().clone();
        if port == 0 || token.is_empty() {
            return None;
        }
        if !child_alive(self.process.lock().unwrap().as_mut()) {
            return None;
        }
        Some(SidecarInfo {
            port,
            api_token: token,
            generation: self.generation.load(Ordering::Acquire),
        })
    }

    fn clear(&self) {
        *self.port.lock().unwrap() = 0;
        *self.api_token.lock().unwrap() = String::new();
    }

    pub(crate) fn presence_public_key(&self) -> String {
        URL_SAFE_NO_PAD.encode(self.presence_signing_key.verifying_key().as_bytes())
    }

    pub(crate) fn sign_presence(&self, proof_payload: &str, authenticator: &str) -> String {
        let message =
            format!("painted-wolf-presence-v1\n{proof_payload}\nauthenticator={authenticator}");
        URL_SAFE_NO_PAD.encode(
            self.presence_signing_key
                .sign(message.as_bytes())
                .to_bytes(),
        )
    }
}

fn generate_presence_signing_key() -> SigningKey {
    let mut secret = [0u8; 32];
    OsRng
        .try_fill_bytes(&mut secret)
        .expect("operating system random source is unavailable");
    SigningKey::from_bytes(&secret)
}

pub fn generate_api_token() -> String {
    let mut bytes = [0u8; 32];
    rand::rng().fill_bytes(&mut bytes);
    URL_SAFE_NO_PAD.encode(bytes)
}

pub(crate) fn attach_only_requested() -> bool {
    #[cfg(debug_assertions)]
    {
        matches!(
            std::env::var("LYCAON_ATTACH_ONLY").ok().as_deref(),
            Some("1") | Some("true") | Some("yes")
        )
    }
    #[cfg(not(debug_assertions))]
    {
        false
    }
}

fn start_sidecar_with_progress<F>(
    state: &SidecarState,
    password: Option<String>,
    emit: F,
) -> Result<SidecarInfo, SidecarStartError>
where
    F: Fn(StartupProgress),
{
    let _lifecycle = state.lifecycle.lock().unwrap();
    // A person asking for the engine starts a fresh crash window.
    state.crashes.lock().unwrap().clear();
    start_sidecar_locked(state, password, &emit)
}

struct StartingGuard<'a>(&'a AtomicBool);

impl Drop for StartingGuard<'_> {
    fn drop(&mut self) {
        self.0.store(false, Ordering::Release);
    }
}

fn start_sidecar_locked<F>(
    state: &SidecarState,
    password: Option<String>,
    emit: &F,
) -> Result<SidecarInfo, SidecarStartError>
where
    F: Fn(StartupProgress),
{
    if attach_only_requested() {
        return attach_or_explain(&crate::config_dir::host_config_dir()?);
    }
    if let Some(info) = state.cached_info() {
        return Ok(info);
    }
    stop_sidecar_locked(state);

    launch_sidecar_locked(state, password, emit)
}

fn launch_sidecar_locked<F>(
    state: &SidecarState,
    password: Option<String>,
    emit: &F,
) -> Result<SidecarInfo, SidecarStartError>
where
    F: Fn(StartupProgress),
{
    state.cancel_start.store(false, Ordering::Release);
    state.starting.store(true, Ordering::Release);
    let _starting = StartingGuard(&state.starting);
    let api_token = generate_api_token();
    let supplied = password.map(|value| VaultPassword(value.into_bytes()));
    let mut password_bytes = supplied.as_ref().map(|value| value.0.clone()).or_else(|| {
        state
            .vault_password
            .lock()
            .unwrap()
            .as_ref()
            .map(|value| value.0.clone())
    });
    let spawned = spawn_sidecar_process(
        &api_token,
        &state.presence_public_key(),
        password_bytes.as_deref(),
    );
    if let Some(value) = password_bytes.as_mut() {
        value.fill(0);
    }
    let spawned = spawned?;
    let child_pid = spawned.child.id();
    *state.process.lock().unwrap() = Some(spawned.child);

    let (closed, exit_watch) = mpsc::channel();
    let port = match await_child_ready(state, child_pid, spawned.startup, closed, emit) {
        Ok(p) => p,
        Err(start_err) => {
            stop_sidecar_locked(state);
            return Err(start_err);
        }
    };

    *state.port.lock().unwrap() = port;
    *state.api_token.lock().unwrap() = api_token.clone();
    if let Some(value) = supplied {
        *state.vault_password.lock().unwrap() = Some(value);
    }
    let generation = state.generation.fetch_add(1, Ordering::AcqRel) + 1;
    let _ = state.watches.send(ExitWatch {
        generation,
        closed: exit_watch,
    });
    state.publish(EngineState::Running { generation });

    Ok(SidecarInfo {
        port,
        api_token,
        generation,
    })
}

fn attach_or_explain(config_dir: &Path) -> Result<SidecarInfo, SidecarStartError> {
    match attach_existing_daemon_in(config_dir)? {
        Some(info) => Ok(info),
        None => Err(SidecarStartError::EngineNotStarted(
            "Attach-only development mode is on, but no engine is running — start one with ./task den:sidecar in another terminal".into(),
        )),
    }
}

/// Publishes engine state to every window and answers unexpected exits for
/// the life of the app.
pub(crate) fn setup_supervision(app: &tauri::AppHandle) {
    use tauri::{Emitter, Manager};
    let state = app.state::<SidecarState>();
    let events = app.clone();
    state.observe(Box::new(move |engine| {
        if let (Some(line), Some(path)) = (supervisor::log_line(engine), logging::engine_log_path())
        {
            let _ = logging::append_engine_log(&path, line.as_bytes());
        }
        let _ = events.emit(supervisor::ENGINE_STATE_EVENT, engine);
    }));
    let Some(watches) = state.take_exit_watches() else {
        return;
    };
    let handle = app.clone();
    std::thread::spawn(move || {
        let state = handle.state::<SidecarState>();
        supervisor::supervise(
            &state,
            watches,
            &supervisor::RESTART_POLICY,
            relaunch_sidecar,
        );
    });
}

pub(crate) fn stop_sidecar(state: &SidecarState) {
    state.cancel_start.store(true, Ordering::Release);
    let _lifecycle = state.lifecycle.lock().unwrap();
    stop_sidecar_locked(state);
}

fn stop_sidecar_locked(state: &SidecarState) {
    kill_child(&mut *state.process.lock().unwrap());
    state.retire();
    state.publish(EngineState::Idle);
}

fn restart_sidecar_with_progress<F>(
    state: &SidecarState,
    emit: F,
) -> Result<SidecarInfo, SidecarStartError>
where
    F: Fn(StartupProgress),
{
    let _lifecycle = state.lifecycle.lock().unwrap();
    if attach_only_requested() {
        return attach_or_explain(&crate::config_dir::host_config_dir()?);
    }
    state.crashes.lock().unwrap().clear();
    stop_sidecar_locked(state);
    launch_sidecar_locked(state, None, &emit)
}

/// Starts a replacement for an engine that exited; the supervisor holds the
/// lifecycle lock.
pub(crate) fn relaunch_sidecar(state: &SidecarState) -> Result<SidecarInfo, SidecarStartError> {
    launch_sidecar_locked(state, None, &|_| {})
}

fn reset_credential_vault(state: &SidecarState) -> Result<(), String> {
    let _lifecycle = state.lifecycle.lock().unwrap();
    if child_alive(state.process.lock().unwrap().as_mut()) {
        return Err("The credential vault cannot be reset while the engine is running.".into());
    }
    let bin = resolve_lycaon_binary()?;
    let config_dir = crate::config_dir::host_config_dir()?;
    let status = Command::new(&bin)
        .args(["credentials", "purge", "--yes"])
        .env("LYCAON_CONFIG_DIR", config_dir)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .map_err(|err| format!("reset credential vault: {err}"))?;
    if !status.success() {
        return Err(format!(
            "Credential vault reset failed (exit {}).",
            status.code().unwrap_or(-1)
        ));
    }
    *state.vault_password.lock().unwrap() = None;
    state.clear();
    Ok(())
}

/// Avoids waiting on the lifecycle lock held by startup.
pub fn cancel_sidecar_start(state: &SidecarState) -> bool {
    if !state.starting.load(Ordering::Acquire) {
        return false;
    }
    state.cancel_start.store(true, Ordering::Release);
    true
}

/// Stops the engine this process started before an exchange; an attached daemon is left running.
pub(crate) fn stop_for_update(state: &SidecarState) -> bool {
    if attach_only_requested() {
        return false;
    }
    stop_sidecar(state);
    true
}
