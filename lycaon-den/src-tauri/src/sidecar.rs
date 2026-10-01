//! Engine process lifecycle and authenticated connection state.

use self::daemon::attach_existing_daemon_in;
use self::layout::resolve_lycaon_binary;
use self::process::{child_alive, kill_child, spawn_sidecar_process};
use self::startup::{await_child_ready, StartupProgress};
use base64::{engine::general_purpose::URL_SAFE_NO_PAD, Engine as _};
use ed25519_dalek::{Signer, SigningKey};
use rand::rngs::OsRng;
use rand::RngCore;
use serde::{Deserialize, Serialize};
use std::path::Path;
use std::process::{Child, Command, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;

pub(crate) mod commands;
pub(crate) mod daemon;
mod diagnostics;
pub(crate) mod layout;
mod logging;
mod process;
mod startup;
#[cfg(test)]
mod tests;

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub struct SidecarInfo {
    pub port: u16,
    pub api_token: String,
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

pub struct SidecarState {
    pub process: Mutex<Option<Child>>,
    pub port: Mutex<u16>,
    pub api_token: Mutex<String>,
    reveal_signing_key: SigningKey,
    pub(crate) reveal_gate: Mutex<()>,
    lifecycle: Mutex<()>,
    starting: AtomicBool,
    cancel_start: AtomicBool,
    vault_password: Mutex<Option<VaultPassword>>,
}

impl SidecarState {
    pub fn new() -> Self {
        Self {
            process: Mutex::new(None),
            port: Mutex::new(0),
            api_token: Mutex::new(String::new()),
            reveal_signing_key: SigningKey::generate(&mut OsRng),
            reveal_gate: Mutex::new(()),
            lifecycle: Mutex::new(()),
            starting: AtomicBool::new(false),
            cancel_start: AtomicBool::new(false),
            vault_password: Mutex::new(None),
        }
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
        })
    }

    fn clear(&self) {
        *self.port.lock().unwrap() = 0;
        *self.api_token.lock().unwrap() = String::new();
    }

    pub(crate) fn reveal_public_key(&self) -> String {
        URL_SAFE_NO_PAD.encode(self.reveal_signing_key.verifying_key().as_bytes())
    }

    pub(crate) fn sign_reveal(&self, proof_payload: &str, authenticator: &str) -> String {
        let message = format!(
            "painted-wolf-managed-secret-reveal-v1\n{proof_payload}\nauthenticator={authenticator}"
        );
        URL_SAFE_NO_PAD.encode(self.reveal_signing_key.sign(message.as_bytes()).to_bytes())
    }
}

pub fn generate_api_token() -> String {
    let mut bytes = [0u8; 32];
    rand::thread_rng().fill_bytes(&mut bytes);
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
        &state.reveal_public_key(),
        password_bytes.as_deref(),
    );
    if let Some(value) = password_bytes.as_mut() {
        value.fill(0);
    }
    let spawned = spawned?;
    let child_pid = spawned.child.id();
    *state.process.lock().unwrap() = Some(spawned.child);

    let port = match await_child_ready(state, child_pid, spawned.startup, emit) {
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

    Ok(SidecarInfo { port, api_token })
}

fn attach_or_explain(config_dir: &Path) -> Result<SidecarInfo, SidecarStartError> {
    match attach_existing_daemon_in(config_dir)? {
        Some(info) => Ok(info),
        None => Err(SidecarStartError::EngineNotStarted(
            "attach-only development mode is on, but no engine is running — start one with ./task den:sidecar in another terminal".into(),
        )),
    }
}

pub(crate) fn stop_sidecar(state: &SidecarState) {
    state.cancel_start.store(true, Ordering::Release);
    let _lifecycle = state.lifecycle.lock().unwrap();
    stop_sidecar_locked(state);
}

fn stop_sidecar_locked(state: &SidecarState) {
    kill_child(&mut *state.process.lock().unwrap());
    state.clear();
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
    stop_sidecar_locked(state);
    launch_sidecar_locked(state, None, &emit)
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
