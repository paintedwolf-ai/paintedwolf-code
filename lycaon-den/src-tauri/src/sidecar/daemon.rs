use super::{attach_only_requested, SidecarInfo};
use serde::Deserialize;
use std::fs;
use std::path::Path;
use std::time::{Duration, Instant};

pub(super) const HEALTH_POLL_INTERVAL: Duration = Duration::from_millis(100);

#[derive(Debug, Deserialize)]
pub(super) struct DaemonManifest {
    pub(super) host: String,
    pub(super) port: u16,
    pub(super) pid: i32,
}

pub(super) fn read_api_token_file(dir: &Path) -> Result<String, String> {
    let raw = fs::read_to_string(dir.join("api.token")).map_err(|e| e.to_string())?;
    let token = raw.trim().to_string();
    if token.is_empty() {
        return Err("empty api.token".into());
    }
    Ok(token)
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
                let api_token = read_api_token_file(dir)?;
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
    let api_token = read_api_token_file(dir)?;
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
