use std::fs::File;
use std::io::{BufRead, BufReader, Read};
use std::path::{Path, PathBuf};
use std::process::{Child, ChildStdin, Command, Stdio};
use std::sync::mpsc;
use std::time::{Duration, Instant};

pub(crate) struct Retention {
    child: Child,
    stdin: Option<ChildStdin>,
    _lock: Option<File>,
}

/// A dedicated package helper holds the shared Go root policy for this shell.
/// Closing stdin releases it on orderly shutdown or an unexpected shell exit.
pub(crate) fn retain() -> Result<Option<Retention>, String> {
    let Some(generation) = super::running()? else {
        return Ok(None);
    };
    let image = generation.helper("pw")?;
    let mut child = Command::new(&image.path)
        .arg("internal-nix-retain")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|e| format!("Start Nix generation retention: {e}"))?;
    let stdin = child.stdin.take();
    let output = child
        .stdout
        .take()
        .ok_or("Nix retention readiness pipe unavailable")?;
    let mut guard = Retention {
        child,
        stdin,
        _lock: None,
    };
    let (sender, ready) = mpsc::channel();
    std::thread::spawn(move || {
        let mut line = String::new();
        let result = BufReader::new(output.take(8192))
            .read_line(&mut line)
            .ok()
            .filter(|n| *n > 0 && line.ends_with('\n'))
            .and_then(|_| serde_json::from_str::<Ready>(&line).ok())
            .filter(|ready| ready.format_version == 1);
        let _ = sender.send(result);
    });
    let receipt = ready.recv_timeout(Duration::from_secs(40)).ok().flatten();
    let Some(receipt) = receipt else {
        return Err("The running Nix generation could not be retained. Check that its Nix daemon and private cache directory are available, then reopen Painted Wolf Code. Saved credentials are preserved.".into());
    };
    guard._lock = Some(hold_lock(&receipt.lock_path, &generation.output)?);
    if guard.child.try_wait().map_err(|e| e.to_string())?.is_some() {
        return Err("Nix generation retention helper exited during startup".into());
    }
    Ok(Some(guard))
}

#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct Ready {
    format_version: u32,
    lock_path: PathBuf,
}

#[cfg(target_os = "linux")]
fn hold_lock(path: &Path, output: &Path) -> Result<File, String> {
    use std::os::fd::AsRawFd;
    use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
    let name = path
        .file_name()
        .and_then(|name| name.to_str())
        .ok_or("Invalid Nix root lock")?;
    let nonce = name
        .strip_prefix("session-")
        .and_then(|name| name.strip_suffix(".lock"))
        .filter(|name| name.len() == 32 && name.bytes().all(|b| b.is_ascii_hexdigit()))
        .ok_or("Invalid Nix root lock")?;
    let parent = path.parent().ok_or("Nix root lock has no directory")?;
    let directory = std::fs::symlink_metadata(parent).map_err(|e| e.to_string())?;
    // SAFETY: getuid has no preconditions; flock receives a live owned descriptor.
    let uid = unsafe { libc::getuid() };
    if !path.is_absolute()
        || !directory.is_dir()
        || directory.uid() != uid
        || directory.mode() & 0o077 != 0
    {
        return Err("Nix root lock directory is not private".into());
    }
    let file = std::fs::OpenOptions::new()
        .read(true)
        .write(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_CLOEXEC)
        .open(path)
        .map_err(|e| e.to_string())?;
    // SAFETY: file owns a live descriptor, and a shared lock preserves other holders.
    if unsafe { libc::flock(file.as_raw_fd(), libc::LOCK_SH | libc::LOCK_NB) } != 0 {
        return Err("Nix root lock is being reclaimed".into());
    }
    let facts = file.metadata().map_err(|e| e.to_string())?;
    let named = std::fs::symlink_metadata(path).map_err(|e| e.to_string())?;
    let link = parent.join(format!("session-{nonce}"));
    if !facts.is_file()
        || facts.uid() != uid
        || facts.mode() & 0o077 != 0
        || facts.nlink() != 1
        || facts.ino() != named.ino()
        || facts.dev() != named.dev()
        || std::fs::read_link(link).ok().as_deref() != Some(output)
    {
        return Err("Nix root lock changed before desktop retention".into());
    }
    Ok(file)
}

#[cfg(not(target_os = "linux"))]
fn hold_lock(_: &Path, _: &Path) -> Result<File, String> {
    Err("Nix generation retention requires Linux".into())
}

#[cfg(all(test, target_os = "linux"))]
mod tests {
    use super::*;
    use std::os::fd::AsRawFd;
    use std::os::unix::fs::{symlink, PermissionsExt};

    #[test]
    fn desktop_retains_its_own_lock_when_the_helper_exits() {
        let directory = tempfile::tempdir().unwrap();
        let path = directory
            .path()
            .join(format!("session-{}.lock", "a".repeat(32)));
        let helper = File::create(&path).unwrap();
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600)).unwrap();
        symlink("/selected-generation", path.with_extension("")).unwrap();
        // SAFETY: the fixture owns this descriptor and never exposes it to another thread.
        assert_eq!(
            unsafe { libc::flock(helper.as_raw_fd(), libc::LOCK_SH | libc::LOCK_NB) },
            0
        );
        let desktop = hold_lock(&path, Path::new("/selected-generation")).unwrap();
        drop(helper);
        let reclaimer = File::open(&path).unwrap();
        // SAFETY: both descriptors remain live for the exclusive reclamation attempts.
        assert_ne!(
            unsafe { libc::flock(reclaimer.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) },
            0
        );
        drop(desktop);
        // SAFETY: the reclaimer still owns its descriptor after the desktop closes its own.
        assert_eq!(
            unsafe { libc::flock(reclaimer.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) },
            0
        );
        drop(reclaimer);
        assert!(hold_lock(&path, Path::new("/different-generation")).is_err());
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o644)).unwrap();
        assert!(hold_lock(&path, Path::new("/selected-generation")).is_err());
    }
}

impl Drop for Retention {
    fn drop(&mut self) {
        self.stdin.take();
        let deadline = Instant::now() + Duration::from_secs(2);
        while Instant::now() < deadline {
            match self.child.try_wait() {
                Ok(Some(_)) => return,
                Err(_) => break,
                Ok(None) => std::thread::sleep(Duration::from_millis(20)),
            }
        }
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}
