use super::layout::{bundled_engine_layout, resolve_lycaon_binary};
use super::process::GRACEFUL_STOP_POLL;
use super::startup::API_TOKEN_MAX_BYTES;
use super::{attach_only_requested, SidecarInfo};
use serde::Deserialize;
use std::fs;
use std::io::Read;
use std::path::Path;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

pub(super) const HEALTH_POLL_INTERVAL: Duration = Duration::from_millis(100);
pub(super) const TOKEN_PRINT_TIMEOUT: Duration = Duration::from_secs(5);

#[derive(Debug, Deserialize)]
pub(super) struct DaemonManifest {
    pub(super) host: String,
    pub(super) port: u16,
    pub(super) pid: i32,
}

pub(super) fn token_print_command(bin: &Path, config_dir: &Path) -> Command {
    let mut command = Command::new(bin);
    command
        .args(["token", "print"])
        .env("LYCAON_CONFIG_DIR", config_dir)
        .env_remove("LYCAON_API_TOKEN")
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    if let Some(layout) = bundled_engine_layout() {
        command.env("LYCAON_ENGINE_ROOT", layout.engine_root);
    }
    command
}

pub(super) fn run_token_print_command(
    mut command: Command,
    timeout: Duration,
) -> Result<String, String> {
    let mut child = command
        .spawn()
        .map_err(|e| format!("run daemon token print: {e}"))?;
    let stdout = child
        .stdout
        .take()
        .ok_or_else(|| "daemon token print stdout unavailable".to_string())?;
    let stderr = child
        .stderr
        .take()
        .ok_or_else(|| "daemon token print stderr unavailable".to_string())?;
    let stdout_reader = std::thread::spawn(move || {
        let mut buf = Vec::new();
        let _ = stdout
            .take((API_TOKEN_MAX_BYTES + 64) as u64)
            .read_to_end(&mut buf);
        buf
    });
    let stderr_reader = std::thread::spawn(move || {
        let mut buf = Vec::new();
        let _ = stderr.take(512).read_to_end(&mut buf);
        buf
    });
    let deadline = Instant::now() + timeout;
    let status = loop {
        match child.try_wait() {
            Ok(Some(status)) => break status,
            Ok(None) => {
                if Instant::now() >= deadline {
                    let _ = child.kill();
                    let _ = child.wait();
                    let _ = stdout_reader.join();
                    let _ = stderr_reader.join();
                    return Err("daemon token print timed out".to_string());
                }
                std::thread::sleep(GRACEFUL_STOP_POLL);
            }
            Err(e) => {
                let _ = child.kill();
                let _ = child.wait();
                let _ = stdout_reader.join();
                let _ = stderr_reader.join();
                return Err(format!("wait daemon token print: {e}"));
            }
        }
    };
    let stdout_bytes = stdout_reader
        .join()
        .map_err(|_| "daemon token print stdout reader panicked".to_string())?;
    let stderr_bytes = stderr_reader
        .join()
        .map_err(|_| "daemon token print stderr reader panicked".to_string())?;
    if !status.success() {
        let code = status.code().unwrap_or(-1);
        let detail = String::from_utf8_lossy(&stderr_bytes).trim().to_string();
        if detail.is_empty() {
            return Err(format!("daemon token print failed (exit {code})"));
        }
        return Err(format!("daemon token print failed (exit {code}): {detail}"));
    }
    let raw = String::from_utf8(stdout_bytes)
        .map_err(|_| "daemon token print returned non-UTF-8 output".to_string())?;
    let token = raw.trim().to_string();
    if token.is_empty() {
        return Err("empty daemon token from token print".into());
    }
    if token.len() > API_TOKEN_MAX_BYTES || token.chars().any(char::is_whitespace) {
        return Err("invalid daemon token from token print".into());
    }
    Ok(token)
}

pub(super) fn resolve_external_daemon_token_with<F>(
    env_token: Option<&str>,
    fallback: F,
) -> Result<String, String>
where
    F: FnOnce() -> Result<String, String>,
{
    if let Some(raw) = env_token {
        let token = raw.trim();
        if !token.is_empty() {
            return Ok(token.to_string());
        }
    }
    fallback()
}

pub(super) fn resolve_external_daemon_token(dir: &Path) -> Result<String, String> {
    let env_token = std::env::var("LYCAON_API_TOKEN").ok();
    resolve_external_daemon_token_with(env_token.as_deref(), || {
        let bin = resolve_lycaon_binary()?;
        let image = crate::linux_generation::capture_helper(&bin)?;
        let command = token_print_command(&image.path, dir);
        run_token_print_command(command, TOKEN_PRINT_TIMEOUT)
    })
}

pub(super) fn is_pid_alive(pid: i32) -> bool {
    if pid <= 0 {
        return false;
    }
    #[cfg(unix)]
    {
        unsafe { libc::kill(pid, 0) == 0 }
    }
    #[cfg(not(unix))]
    {
        use windows_sys::Win32::Foundation::{CloseHandle, STILL_ACTIVE};
        use windows_sys::Win32::System::Threading::{
            GetExitCodeProcess, OpenProcess, PROCESS_QUERY_LIMITED_INFORMATION,
        };

        // SAFETY: the handle is queried and closed within this block.
        unsafe {
            let handle = OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, pid as u32);
            if handle.is_null() {
                return false;
            }
            let mut exit_code = 0;
            let queried = GetExitCodeProcess(handle, &mut exit_code) != 0;
            let _ = CloseHandle(handle);
            queried && exit_code == STILL_ACTIVE as u32
        }
    }
}

pub fn attach_existing_daemon() -> Result<Option<SidecarInfo>, String> {
    attach_existing_daemon_in(&crate::config_dir::host_config_dir()?)
}

pub(super) fn attach_existing_daemon_in(dir: &Path) -> Result<Option<SidecarInfo>, String> {
    let manifest_path = dir.join("daemon.json");
    let raw = match fs::read_to_string(&manifest_path) {
        Ok(v) => v,
        Err(_) => return Ok(None),
    };
    let manifest: DaemonManifest =
        serde_json::from_str(&raw).map_err(|e| format!("parse daemon.json: {e}"))?;
    if !is_pid_alive(manifest.pid) {
        if attach_only_requested() {
            let host = if manifest.host.trim().is_empty() {
                "127.0.0.1".to_string()
            } else {
                manifest.host.clone()
            };
            if poll_health_on_host(&host, manifest.port).is_ok() {
                let api_token = resolve_external_daemon_token(dir)?;
                return Ok(Some(SidecarInfo {
                    port: manifest.port,
                    api_token,
                    generation: 0,
                }));
            }
        }
        return Ok(None);
    }
    if manifest.port == 0 {
        return Ok(None);
    }
    let host = if manifest.host.trim().is_empty() {
        "127.0.0.1".to_string()
    } else {
        manifest.host
    };
    poll_health_on_host(&host, manifest.port)?;
    let api_token = resolve_external_daemon_token(dir)?;
    Ok(Some(SidecarInfo {
        port: manifest.port,
        api_token,
        generation: 0,
    }))
}

pub(super) fn poll_health_on_host(host: &str, port: u16) -> Result<(), String> {
    let deadline = Instant::now() + Duration::from_secs(2);
    let url = format!("http://{host}:{port}/health");
    while Instant::now() < deadline {
        if let Ok(resp) = ureq::get(&url).call() {
            if resp.status() == 200 {
                return Ok(());
            }
        }
        std::thread::sleep(HEALTH_POLL_INTERVAL);
    }
    Err(format!("daemon health failed for {host}:{port}"))
}
