//! Moves the vault identity between the identity service and the app
//! password. The engine binary performs each move as a child of this shell,
//! the only parent the identity service releases an identity to, while the
//! engine is stopped.

use super::layout::resolve_lycaon_binary;
use super::process::write_vault_password;
use super::{attach_only_requested, stop_sidecar_locked, SidecarState, VaultPassword};
use serde::{Deserialize, Serialize};
use std::io::Write;
use std::process::{Command, Output, Stdio};
use tauri::Manager;

/// Who holds the vault identity, as `pw credentials custody` reports it.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub(crate) struct CustodyReport {
    pub mode: String,
    pub service_verified: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "snake_case")]
pub(crate) enum CustodyTarget {
    IdentityService,
    AppPassword,
}

impl CustodyTarget {
    fn command(self) -> &'static str {
        match self {
            CustodyTarget::IdentityService => "move-to-service",
            CustodyTarget::AppPassword => "move-to-app-password",
        }
    }
}

/// A failed move: `code` is the engine's stable outcome code.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub(crate) struct CustodyError {
    pub code: String,
    pub message: String,
}

impl CustodyError {
    fn failed(message: impl Into<String>) -> Self {
        Self {
            code: "failed".into(),
            message: message.into(),
        }
    }
}

#[derive(Deserialize)]
struct Outcome {
    code: String,
    #[serde(default)]
    message: String,
}

fn engine_command(args: &[&str]) -> Result<Command, String> {
    let mut cmd = Command::new(resolve_lycaon_binary()?);
    cmd.arg("credentials")
        .args(args)
        .env(
            "LYCAON_CONFIG_DIR",
            crate::config_dir::ensure_host_config_dir()?,
        )
        .stderr(Stdio::null());
    Ok(cmd)
}

fn read_custody() -> Result<CustodyReport, String> {
    let output = engine_command(&["custody"])?
        .stdin(Stdio::null())
        .output()
        .map_err(|err| format!("read vault custody: {err}"))?;
    parse_report(&output)
}

pub(super) fn parse_report(output: &Output) -> Result<CustodyReport, String> {
    if !output.status.success() {
        return Err(format!(
            "The vault custody could not be read (exit {}).",
            output.status.code().unwrap_or(-1)
        ));
    }
    serde_json::from_slice(&output.stdout).map_err(|err| format!("read vault custody: {err}"))
}

pub(super) fn parse_outcome(output: &Output) -> Result<(), CustodyError> {
    let outcome: Outcome = serde_json::from_slice(&output.stdout).map_err(|_| {
        CustodyError::failed(format!(
            "The vault identity could not be moved (exit {}).",
            output.status.code().unwrap_or(-1)
        ))
    })?;
    if outcome.code == "ok" && output.status.success() {
        return Ok(());
    }
    Err(CustodyError {
        code: outcome.code,
        message: outcome.message,
    })
}

fn run_move(target: CustodyTarget, password: &[u8]) -> Result<(), CustodyError> {
    let mut child = engine_command(&[target.command(), "--yes"])
        .map_err(CustodyError::failed)?
        .env("LYCAON_CREDENTIAL_UNLOCK_STDIN", "1")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .spawn()
        .map_err(|err| CustodyError::failed(format!("start the vault identity move: {err}")))?;
    let written = match child.stdin.take() {
        Some(mut stdin) => write_vault_password(&mut stdin, password).and_then(|()| {
            stdin
                .flush()
                .map_err(|err| format!("write credential vault password: {err}"))
        }),
        None => Err("credential vault password pipe unavailable".to_string()),
    };
    let output = child
        .wait_with_output()
        .map_err(|err| CustodyError::failed(format!("finish the vault identity move: {err}")))?;
    written.map_err(CustodyError::failed)?;
    parse_outcome(&output)
}

/// Stops the engine, moves the identity, and records the password this
/// shell now holds. The caller restarts the engine either way.
fn move_custody(
    state: &SidecarState,
    target: CustodyTarget,
    password: String,
) -> Result<(), CustodyError> {
    let _lifecycle = state.lifecycle.lock().unwrap();
    if attach_only_requested() {
        return Err(CustodyError::failed(
            "The vault identity cannot be moved while attached to a development engine.",
        ));
    }
    let password = VaultPassword(password.into_bytes());
    stop_sidecar_locked(state);
    run_move(target, &password.0)?;
    *state.vault_password.lock().unwrap() = match target {
        CustodyTarget::AppPassword => Some(password),
        CustodyTarget::IdentityService => None,
    };
    state.clear();
    Ok(())
}

#[tauri::command(rename = "vault_custody")]
pub async fn ipc_vault_custody() -> Result<CustodyReport, String> {
    tauri::async_runtime::spawn_blocking(read_custody)
        .await
        .map_err(|err| format!("vault custody task join: {err}"))?
}

#[tauri::command(rename = "move_vault_custody")]
pub async fn ipc_move_vault_custody(
    app: tauri::AppHandle,
    target: CustodyTarget,
    password: String,
) -> Result<(), CustodyError> {
    tauri::async_runtime::spawn_blocking(move || {
        let state = app.state::<SidecarState>();
        move_custody(&state, target, password)
    })
    .await
    .map_err(|err| CustodyError::failed(format!("vault custody task join: {err}")))?
}

#[cfg(all(test, unix))]
mod tests {
    use super::*;
    use std::os::unix::process::ExitStatusExt;
    use std::process::ExitStatus;

    fn output(code: i32, stdout: &str) -> Output {
        Output {
            status: ExitStatus::from_raw(code << 8),
            stdout: stdout.as_bytes().to_vec(),
            stderr: Vec::new(),
        }
    }

    #[test]
    fn reports_parse_from_the_engine_json() {
        let report = parse_report(&output(
            0,
            r#"{"mode":"app_password","service_verified":true}"#,
        ))
        .unwrap();
        assert_eq!(report.mode, "app_password");
        assert!(report.service_verified);
        assert_eq!(report.detail, None);
        assert!(parse_report(&output(1, "")).is_err());
        assert!(parse_report(&output(0, "not json")).is_err());
    }

    #[test]
    fn move_outcomes_keep_the_engine_code() {
        assert_eq!(parse_outcome(&output(0, "{\"code\":\"ok\"}\n")), Ok(()));
        assert_eq!(
            parse_outcome(&output(
                1,
                r#"{"code":"password_incorrect","message":"wrong"}"#
            )),
            Err(CustodyError {
                code: "password_incorrect".into(),
                message: "wrong".into()
            })
        );
        // An ok line from a failing process is not success.
        assert_eq!(
            parse_outcome(&output(1, r#"{"code":"ok"}"#))
                .unwrap_err()
                .code,
            "ok"
        );
        assert_eq!(parse_outcome(&output(2, "")).unwrap_err().code, "failed");
    }

    #[test]
    fn targets_name_the_engine_commands() {
        let service: CustodyTarget = serde_json::from_str("\"identity_service\"").unwrap();
        let password: CustodyTarget = serde_json::from_str("\"app_password\"").unwrap();
        assert_eq!(service.command(), "move-to-service");
        assert_eq!(password.command(), "move-to-app-password");
    }
}
