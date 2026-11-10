use super::layout::{bundled_engine_layout, resolve_lycaon_binary};
use super::process::{API_TOKEN_MAX_BYTES, GRACEFUL_STOP_POLL};
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
        .stderr(Stdio::null());
    if let Some(layout) = bundled_engine_layout() {
        command.env("LYCAON_ENGINE_ROOT", layout.engine_root);
    }
    command
}

#[cfg(unix)]
fn terminate_helper_process_group(child: &mut std::process::Child) {
    let pgid = child.id() as i32;
    if pgid > 0 {
        // SAFETY: child was spawned with process_group(0), so pgid is the
        // helper's dedicated process group and never affects unrelated processes.
        unsafe {
            let _ = libc::kill(-pgid, libc::SIGKILL);
        }
    }
    let _ = child.kill();
    let _ = child.wait();
}

#[cfg(unix)]
fn drain_nonblocking_stdout(
    stdout: &mut std::process::ChildStdout,
    buf: &mut Vec<u8>,
    max_bytes: usize,
) -> Result<bool, String> {
    let mut chunk = [0u8; 256];
    loop {
        let remaining = max_bytes.saturating_sub(buf.len());
        if remaining == 0 {
            return Ok(true);
        }
        let limit = remaining.min(chunk.len());
        match stdout.read(&mut chunk[..limit]) {
            Ok(0) => return Ok(true),
            Ok(n) => buf.extend_from_slice(&chunk[..n]),
            Err(e) if e.kind() == std::io::ErrorKind::Interrupted => continue,
            Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => return Ok(false),
            Err(e) => return Err(format!("read daemon token print stdout: {e}")),
        }
    }
}

pub(super) fn run_token_print_command(
    mut command: Command,
    timeout: Duration,
) -> Result<String, String> {
    command
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null());
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        command.process_group(0);
    }
    let mut child = command
        .spawn()
        .map_err(|e| format!("run daemon token print: {e}"))?;
    let mut stdout = child
        .stdout
        .take()
        .ok_or_else(|| "daemon token print stdout unavailable".to_string())?;
    let max_capture = API_TOKEN_MAX_BYTES + 64;

    #[cfg(unix)]
    let (status, stdout_bytes) = {
        use std::os::fd::AsRawFd;
        // SAFETY: stdout is a valid open pipe file descriptor owned by this scope.
        unsafe {
            let fd = stdout.as_raw_fd();
            let flags = libc::fcntl(fd, libc::F_GETFL);
            if flags >= 0 {
                let _ = libc::fcntl(fd, libc::F_SETFL, flags | libc::O_NONBLOCK);
            }
        }
        let mut buf = Vec::with_capacity(128);
        let deadline = Instant::now() + timeout;
        let status = loop {
            let _ = drain_nonblocking_stdout(&mut stdout, &mut buf, max_capture)?;
            match child.try_wait() {
                Ok(Some(status)) => {
                    let _ = drain_nonblocking_stdout(&mut stdout, &mut buf, max_capture);
                    break status;
                }
                Ok(None) => {
                    if Instant::now() >= deadline {
                        drop(stdout);
                        terminate_helper_process_group(&mut child);
                        return Err("daemon token print timed out".to_string());
                    }
                    std::thread::sleep(GRACEFUL_STOP_POLL);
                }
                Err(e) => {
                    drop(stdout);
                    terminate_helper_process_group(&mut child);
                    return Err(format!("wait daemon token print: {e}"));
                }
            }
        };
        drop(stdout);
        terminate_helper_process_group(&mut child);
        (status, buf)
    };

    #[cfg(not(unix))]
    let (status, stdout_bytes) = {
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let mut buf = Vec::new();
            let _ = stdout.take(max_capture as u64).read_to_end(&mut buf);
            let _ = tx.send(buf);
        });
        let deadline = Instant::now() + timeout;
        let status = loop {
            match child.try_wait() {
                Ok(Some(status)) => break status,
                Ok(None) => {
                    if Instant::now() >= deadline {
                        let _ = child.kill();
                        let _ = child.wait();
                        return Err("daemon token print timed out".to_string());
                    }
                    std::thread::sleep(GRACEFUL_STOP_POLL);
                }
                Err(e) => {
                    let _ = child.kill();
                    let _ = child.wait();
                    return Err(format!("wait daemon token print: {e}"));
                }
            }
        };
        let remaining = deadline.saturating_duration_since(Instant::now());
        let buf = rx
            .recv_timeout(remaining.max(Duration::from_millis(50)))
            .unwrap_or_default();
        (status, buf)
    };

    if !status.success() {
        return Err(match status.code() {
            Some(code) => format!("daemon token print failed (exit {code})"),
            None => "daemon token print terminated by signal".to_string(),
        });
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

#[cfg(test)]
#[path = "daemon_tests.rs"]
mod daemon_tests;
