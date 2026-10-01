use super::layout::{bundled_engine_layout, resolve_lycaon_binary};
use super::process::GRACEFUL_STOP_POLL;
use std::io::Read;
use std::path::Path;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

pub(super) const DIAGNOSTICS_TIMEOUT: Duration = Duration::from_secs(60);

pub(super) fn startup_diagnostics_command(bin: &Path, config_dir: &Path) -> Command {
    let mut command = Command::new(bin);
    command
        .args(["diagnostics", "startup-diagnostics"])
        .env("LYCAON_CONFIG_DIR", config_dir)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null());
    if let Some(layout) = bundled_engine_layout() {
        command.env("LYCAON_ENGINE_ROOT", layout.engine_root);
    }
    command
}

pub fn export_startup_diagnostics() -> Result<Vec<u8>, String> {
    let bin = resolve_lycaon_binary()?;
    let config_dir = crate::config_dir::host_config_dir()?;
    let mut command = startup_diagnostics_command(&bin, &config_dir);
    let mut child = command
        .spawn()
        .map_err(|e| format!("run startup diagnostics: {e}"))?;
    let mut stdout = child
        .stdout
        .take()
        .ok_or_else(|| "startup diagnostics stdout unavailable".to_string())?;
    let reader = std::thread::spawn(move || {
        let mut buf = Vec::new();
        let _ = stdout.read_to_end(&mut buf);
        buf
    });
    let deadline = Instant::now() + DIAGNOSTICS_TIMEOUT;
    let status = loop {
        match child.try_wait() {
            Ok(Some(status)) => break status,
            Ok(None) => {
                if Instant::now() >= deadline {
                    let _ = child.kill();
                    let _ = child.wait();
                    let _ = reader.join();
                    return Err("startup diagnostics timed out".to_string());
                }
                std::thread::sleep(GRACEFUL_STOP_POLL);
            }
            Err(e) => {
                let _ = child.kill();
                let _ = child.wait();
                let _ = reader.join();
                return Err(format!("wait startup diagnostics: {e}"));
            }
        }
    };
    let bundle = reader
        .join()
        .map_err(|_| "startup diagnostics reader panicked".to_string())?;
    if !status.success() {
        return Err("startup diagnostics command failed".to_string());
    }
    if bundle.is_empty() {
        return Err("startup diagnostics command returned no bundle".to_string());
    }
    Ok(bundle)
}
