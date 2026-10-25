//! Reads the engine's protected credential-key custody report.

use super::layout::resolve_lycaon_binary;
use serde::{Deserialize, Serialize};
use std::process::{Command, Output, Stdio};

/// Who holds the vault identity, as `pw credentials custody` reports it.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub(crate) struct CustodyReport {
    pub mode: String,
    pub service_verified: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
}

fn engine_command(args: &[&str]) -> Result<(Command, crate::linux_generation::HelperImage), String> {
    super::installation::ensure_current().map_err(|reason| reason.guidance())?;
    let image = crate::linux_generation::capture_helper(&resolve_lycaon_binary()?)?;
    let mut cmd = Command::new(&image.path);
    cmd.arg("credentials")
        .args(args)
        .env(
            "LYCAON_CONFIG_DIR",
            crate::config_dir::ensure_host_config_dir()?,
        )
        .stderr(Stdio::null());
    Ok((cmd, image))
}

fn read_custody() -> Result<CustodyReport, String> {
    if let Err(reason) = super::installation::ensure_current() {
        return Ok(CustodyReport {
            mode: "restart_required".into(),
            service_verified: false,
            detail: Some(reason.guidance()),
        });
    }
    let (mut command, _image) = engine_command(&["custody"])?;
    let output = command
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

#[tauri::command(rename = "vault_custody")]
pub async fn ipc_vault_custody() -> Result<CustodyReport, String> {
    tauri::async_runtime::spawn_blocking(read_custody)
        .await
        .map_err(|err| format!("vault custody task join: {err}"))?
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
            r#"{"mode":"identity_service","service_verified":true}"#,
        ))
        .unwrap();
        assert_eq!(report.mode, "identity_service");
        assert!(report.service_verified);
        assert_eq!(report.detail, None);
        assert!(parse_report(&output(1, "")).is_err());
        assert!(parse_report(&output(0, "not json")).is_err());
    }

}
