use super::layout::{bundled_engine_layout, resolve_lycaon_binary};
use super::logging::{
    drain_engine_stderr, engine_log_path, write_engine_log_header,
};
use super::startup::STARTUP_PROTOCOL_VERSION;
use super::ChildState;
use std::io::Write;
use std::path::Path;
use std::process::{Child, ChildStdout, Command, Stdio};
use std::time::{Duration, Instant};

const STARTUP_PROTOCOL_ENV: &str = "LYCAON_STARTUP_PROTOCOL";
const CONTROL_STDIN_ENV: &str = "LYCAON_CONTROL_STDIN";
/// The engine reads its API bearer as the first stdin frame. An initial
/// environment stays readable through the process filesystem until the engine
/// disables dumps, and to same-user processes on macOS for its whole life.
const API_TOKEN_STDIN_ENV: &str = "LYCAON_API_TOKEN_STDIN";
pub(super) const API_TOKEN_MAX_BYTES: usize = 512;
const CONTROL_SHUTDOWN_FRAME: &[u8] = b"shutdown\n";
pub(super) const VAULT_PASSWORD_MAX_BYTES: usize = 16 * 1024;
/// Exceeds the engine's ordered shutdown budget.
pub(super) const GRACEFUL_STOP_TIMEOUT: Duration = Duration::from_secs(20);
pub(super) const GRACEFUL_STOP_POLL: Duration = Duration::from_millis(50);

pub(super) struct SpawnedSidecar {
    pub(super) child: Child,
    pub(super) startup: ChildStdout,
}

pub(super) fn child_state(child: Option<&mut Child>) -> ChildState {
    match child {
        Some(c) => match c.try_wait() {
            Ok(Some(status)) => ChildState::Exited(status.code()),
            Ok(None) => ChildState::Running,
            Err(_) => ChildState::Exited(None),
        },
        None => ChildState::Exited(None),
    }
}

pub(super) fn child_alive(child: Option<&mut Child>) -> bool {
    matches!(child_state(child), ChildState::Running)
}

/// Slow shutdowns remain visible after the window closes.
const GRACEFUL_STOP_NOTE_AFTER: Duration = Duration::from_secs(2);

/// Stops the engine gracefully, then kills it after the timeout.
pub(super) fn kill_child(child: &mut Option<Child>) {
    let Some(mut proc) = child.take() else {
        return;
    };
    let started = Instant::now();
    if request_graceful_stop(&mut proc) && wait_for_exit(&mut proc, GRACEFUL_STOP_TIMEOUT) {
        note_slow_graceful_stop(started.elapsed(), false);
        return;
    }
    let _ = proc.kill();
    let _ = proc.wait();
    note_slow_graceful_stop(started.elapsed(), true);
}

fn note_slow_graceful_stop(waited: Duration, killed: bool) {
    if waited < GRACEFUL_STOP_NOTE_AFTER {
        return;
    }
    if let Some(path) = engine_log_path() {
        let outcome = if killed {
            "hard-killed after the graceful wait ran out"
        } else {
            "exited on its own"
        };
        let _ = crate::diagnostic_log::append(
            &path,
            format!(
                "--- engine shutdown took {}ms; {outcome} ---\n",
                waited.as_millis()
            )
            .as_bytes(),
        );
    }
}

pub(super) fn request_graceful_stop(proc: &mut Child) -> bool {
    let Some(mut control) = proc.stdin.take() else {
        return false;
    };
    control.write_all(CONTROL_SHUTDOWN_FRAME).is_ok()
}

pub(super) fn wait_for_exit(proc: &mut Child, timeout: Duration) -> bool {
    let deadline = Instant::now() + timeout;
    loop {
        match proc.try_wait() {
            Ok(Some(_)) => return true,
            Err(_) => return false,
            Ok(None) => {}
        }
        if Instant::now() >= deadline {
            return false;
        }
        std::thread::sleep(GRACEFUL_STOP_POLL);
    }
}

pub(super) fn spawn_sidecar_process(
    api_token: &str,
    presence_public_key: &str,
    password: Option<&[u8]>,
) -> Result<SpawnedSidecar, String> {
    let bin = resolve_lycaon_binary()?;
    let config_dir = crate::config_dir::ensure_host_config_dir()?;
    let mut cmd = engine_command(&bin, presence_public_key, &config_dir);
    if password.is_some() {
        cmd.env("LYCAON_CREDENTIAL_UNLOCK_STDIN", "1");
    }
    write_engine_log_header(&bin);
    cmd.stderr(Stdio::piped());
    if let Some(layout) = bundled_engine_layout() {
        cmd.env("LYCAON_ENGINE_ROOT", layout.engine_root);
    }
    if full_debug_logging_enabled() {
        apply_full_debug_logging_env(&mut cmd);
    }
    let mut child = cmd
        .spawn()
        .map_err(|e| format!("spawn {}: {e}", bin.display()))?;
    if let Some(stderr) = child.stderr.take() {
        let path = engine_log_path();
        std::thread::spawn(move || drain_engine_stderr(stderr, path));
    }
    let Some(stdin) = child.stdin.as_mut() else {
        let _ = child.kill();
        let _ = child.wait();
        return Err("engine startup pipe unavailable".to_string());
    };
    let delivered = write_api_token(&mut *stdin, api_token).and_then(|()| match password {
        Some(password) => write_vault_password(&mut *stdin, password),
        None => Ok(()),
    });
    if let Err(err) = delivered {
        let _ = child.kill();
        let _ = child.wait();
        return Err(err);
    }
    let startup = child
        .stdout
        .take()
        .ok_or_else(|| "engine startup protocol pipe unavailable".to_string())?;
    Ok(SpawnedSidecar { child, startup })
}

/// The engine's command and environment. Secrets travel on its stdin.
pub(super) fn engine_command(bin: &Path, presence_public_key: &str, config_dir: &Path) -> Command {
    let mut cmd = Command::new(bin);
    cmd.arg("serve")
        .env_remove("LYCAON_API_TOKEN")
        .env("LYCAON_ADDR", "127.0.0.1:0")
        .env(API_TOKEN_STDIN_ENV, "1")
        .env("LYCAON_PRESENCE_PUBLIC_KEY", presence_public_key)
        .env("LYCAON_CONFIG_DIR", config_dir)
        .env(STARTUP_PROTOCOL_ENV, STARTUP_PROTOCOL_VERSION.to_string())
        .env(CONTROL_STDIN_ENV, "1")
        .env("LYCAON_PARENT_PID", std::process::id().to_string())
        .stdin(Stdio::piped())
        .stdout(Stdio::piped());
    cmd
}

/// Writes the engine's API bearer as a length-prefixed frame.
pub(super) fn write_api_token<W: Write>(mut writer: W, token: &str) -> Result<(), String> {
    if token.is_empty()
        || token.len() > API_TOKEN_MAX_BYTES
        || !token.bytes().all(|b| b.is_ascii_graphic())
    {
        return Err("engine api token must be 1 to 512 printable ASCII bytes".to_string());
    }
    let size =
        u32::try_from(token.len()).map_err(|_| "engine api token is too large".to_string())?;
    writer
        .write_all(&size.to_be_bytes())
        .and_then(|_| writer.write_all(token.as_bytes()))
        .map_err(|err| format!("write engine api token: {err}"))
}

pub(super) fn write_vault_password<W: Write>(mut writer: W, password: &[u8]) -> Result<(), String> {
    if password.is_empty() || password.len() > VAULT_PASSWORD_MAX_BYTES {
        return Err(format!(
            "credential vault password must be between 1 and {VAULT_PASSWORD_MAX_BYTES} bytes"
        ));
    }
    let size = u32::try_from(password.len())
        .map_err(|_| "credential vault password is too large".to_string())?;
    writer
        .write_all(&size.to_be_bytes())
        .and_then(|_| writer.write_all(password))
        .map_err(|err| format!("write credential vault password: {err}"))
}

pub(super) fn apply_full_debug_logging_env(cmd: &mut Command) {
    cmd.env("LYCAON_DEBUG_ALL", "1");
}

fn full_debug_logging_enabled() -> bool {
    let Ok(dir) = crate::config_dir::host_config_dir() else {
        return false;
    };
    full_debug_logging_enabled_in(&dir)
}

pub(super) fn full_debug_logging_enabled_in(config_dir: &Path) -> bool {
    let Ok(state) = crate::app_state::read_app_state_doc(config_dir) else {
        return false;
    };
    state
        .get("debug")
        .and_then(|debug| debug.get("fullDebugLogging"))
        .and_then(serde_json::Value::as_bool)
        .unwrap_or(false)
}
