//! Exact-document grants for an explicitly selected external Flatpak editor.
//!
//! The document portal identifies its caller through `/proc/<pid>/root`, which
//! the non-dumpable release shell does not expose, so the shell never calls it.
//! The launcher's own `flatpak document-export` runs as a fresh executable,
//! which the portal can identify, and grants the app exactly one file or
//! folder for the session.

use std::ffi::OsString;
use std::io::Read;
use std::os::unix::ffi::OsStringExt;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::time::{Duration, Instant};

const TIMEOUT: Duration = Duration::from_secs(5);
const OUTPUT_LIMIT: usize = 8192;

/// A unique, session-only export. A launch failure removes the unused grant;
/// a successful launch retains it.
pub(crate) struct Export {
    pub(crate) path: PathBuf,
    flatpak: PathBuf,
    id: String,
    retained: bool,
}

impl Export {
    pub(crate) fn retain(mut self) {
        self.retained = true;
    }
}

impl Drop for Export {
    fn drop(&mut self) {
        if !self.retained {
            let mut command = Command::new(&self.flatpak);
            command.args(["document-unexport", "--doc-id", &self.id]);
            let _ = run(command, TIMEOUT);
        }
    }
}

/// Exports `path` to `app` with the same `flatpak` program the desktop entry runs.
pub(crate) fn export(flatpak: &Path, path: &Path, app: &str) -> Result<Export, String> {
    export_within(flatpak, path, app, TIMEOUT)
}

fn export_within(flatpak: &Path, path: &Path, app: &str, timeout: Duration) -> Result<Export, String> {
    if !path.is_absolute() || path.file_name().is_none() {
        return Err("The editor file has no absolute name.".into());
    }
    let mut command = Command::new(flatpak);
    command
        .args(["document-export", "--unique", "--transient", "--allow-read", "--allow-write"])
        .arg(format!("--app={app}"))
        .arg("--")
        .arg(path);
    let output = run(command, timeout)?;
    if !output.success {
        return Err(format!(
            "The document portal refused editor access: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }
    let (exported, id) = exported_path(output.stdout, path)?;
    Ok(Export { path: exported, flatpak: flatpak.to_path_buf(), id, retained: false })
}

struct Output {
    success: bool,
    stdout: Vec<u8>,
    stderr: Vec<u8>,
}

/// Runs `command` to completion, stopping the child it started past `timeout`.
fn run(mut command: Command, timeout: Duration) -> Result<Output, String> {
    let mut child = command
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|error| format!("Flatpak is unavailable: {error}"))?;
    let deadline = Instant::now() + timeout;
    let status = loop {
        if let Some(status) = child.try_wait().map_err(|error| error.to_string())? {
            break status;
        }
        if Instant::now() >= deadline {
            stop(&mut child);
            return Err("The document portal did not answer in time.".into());
        }
        std::thread::sleep(Duration::from_millis(10));
    };
    Ok(Output {
        success: status.success(),
        stdout: bounded(child.stdout.take()),
        stderr: bounded(child.stderr.take()),
    })
}

fn stop(child: &mut Child) {
    let _ = child.kill();
    let _ = child.wait();
}

fn bounded(stream: Option<impl Read>) -> Vec<u8> {
    let mut bytes = Vec::new();
    if let Some(stream) = stream {
        let _ = stream.take(OUTPUT_LIMIT as u64).read_to_end(&mut bytes);
    }
    bytes
}

/// The export is `<mount>/<id>/<original name>`; anything else is refused.
fn exported_path(mut stdout: Vec<u8>, original: &Path) -> Result<(PathBuf, String), String> {
    if stdout.last() == Some(&b'\n') {
        stdout.pop();
    }
    if stdout.is_empty() || stdout.len() >= OUTPUT_LIMIT || stdout.contains(&b'\n') || stdout.contains(&0) {
        return Err("Invalid document portal export".into());
    }
    let exported = PathBuf::from(OsString::from_vec(stdout));
    let id = exported
        .parent()
        .and_then(Path::file_name)
        .and_then(|id| id.to_str())
        .ok_or("Invalid document portal identifier")?;
    if !exported.is_absolute()
        || exported.file_name() != original.file_name()
        || id.is_empty()
        || id.len() > 128
        || !id.bytes().all(|byte| byte.is_ascii_alphanumeric() || byte == b'_' || byte == b'-')
        || exported.parent().and_then(Path::parent).is_none_or(|mount| mount == Path::new("/"))
    {
        return Err("Invalid document portal export".into());
    }
    let id = id.to_string();
    Ok((exported, id))
}

#[cfg(test)]
mod tests;
