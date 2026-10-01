//! Manages the bundled command symlinks in `/usr/local/bin`.

use std::env;
use std::fs;
use std::io;
use std::path::{Path, PathBuf};
use std::process::Command;

use serde::Serialize;

const ENGINE_NAME: &str = "pw";
const LOGS_NAME: &str = "pw-logs";
const INSTALL_BIN_DIR: &str = "/usr/local/bin";

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "snake_case")]
pub struct ShellCommandStatus {
    /// Symlinks for `pw` and `pw-logs` both point at the current bundle binaries.
    pub installed: bool,
    pub bin_dir: String,
    pub pw_link: String,
    pub pw_logs_link: String,
    pub pw_target: Option<String>,
    pub pw_logs_target: Option<String>,
    pub unavailable_reason: Option<String>,
}

#[derive(Debug, Clone)]
struct BundleBins {
    pw: PathBuf,
    pw_logs: PathBuf,
}

fn resolve_bundle_bins() -> Result<BundleBins, String> {
    if let Some(bins) = bundle_bins_beside_exe() {
        return Ok(bins);
    }
    #[cfg(debug_assertions)]
    {
        if let Ok(raw) = env::var("LYCAON_SIDECAR_BIN") {
            let pw = PathBuf::from(raw.trim());
            if pw.is_file() {
                let dir = pw
                    .parent()
                    .ok_or_else(|| "sidecar bin has no parent".to_string())?;
                let logs = dir.join(LOGS_NAME);
                if logs.is_file() {
                    return Ok(BundleBins { pw, pw_logs: logs });
                }
            }
        }
    }
    Err(
        "bundled pw binary not found in the app — install from a packaged build, or stage the engine first"
            .into(),
    )
}

fn bundle_bins_beside_exe() -> Option<BundleBins> {
    let exe = env::current_exe().ok()?;
    let macos = exe.parent()?;
    let pw = crate::sidecar::layout::bundled_sidecar_path(&exe, std::env::consts::OS)?;
    let pw_logs = macos.join(LOGS_NAME);
    if pw.is_file() && pw_logs.is_file() {
        return Some(BundleBins { pw, pw_logs });
    }
    None
}

// Match the stored target without resolving additional symlinks.
fn link_points_at(link: &Path, want: &Path) -> bool {
    fs::read_link(link).is_ok_and(|got| got.as_os_str() == want.as_os_str())
}

fn status_with(bin_dir: &Path, bins: Result<BundleBins, String>) -> ShellCommandStatus {
    let pw_link = bin_dir.join(ENGINE_NAME);
    let pw_logs_link = bin_dir.join(LOGS_NAME);

    let (pw_target, pw_logs_target, unavailable, installed) = match bins {
        Ok(b) => {
            let installed =
                link_points_at(&pw_link, &b.pw) && link_points_at(&pw_logs_link, &b.pw_logs);
            (
                Some(b.pw.display().to_string()),
                Some(b.pw_logs.display().to_string()),
                None,
                installed,
            )
        }
        Err(reason) => (None, None, Some(reason), false),
    };

    ShellCommandStatus {
        installed,
        bin_dir: bin_dir.display().to_string(),
        pw_link: pw_link.display().to_string(),
        pw_logs_link: pw_logs_link.display().to_string(),
        pw_target,
        pw_logs_target,
        unavailable_reason: unavailable,
    }
}

fn shell_single_quote(path: &Path) -> String {
    format!("'{}'", path.display().to_string().replace('\'', "'\\''"))
}

#[cfg(target_os = "macos")]
fn run_privileged_shell(script: &str) -> Result<(), String> {
    let escaped = script.replace('\\', "\\\\").replace('"', "\\\"");
    let apple = format!("do shell script \"{escaped}\" with administrator privileges");
    let output = Command::new("osascript")
        .arg("-e")
        .arg(apple)
        .output()
        .map_err(|e| format!("ask for administrator privileges: {e}"))?;
    if output.status.success() {
        return Ok(());
    }
    let stderr = String::from_utf8_lossy(&output.stderr);
    Err(format!(
        "could not update commands in {INSTALL_BIN_DIR} with administrator privileges: {}",
        stderr.trim()
    ))
}

#[cfg(not(target_os = "macos"))]
fn run_privileged_shell(_script: &str) -> Result<(), String> {
    Err(format!(
        "could not write to {INSTALL_BIN_DIR} — create the symlinks as root, or make that directory writable"
    ))
}

fn validate_install_link(link: &Path, target: &Path) -> Result<(), String> {
    match fs::symlink_metadata(link) {
        Err(err) if err.kind() == io::ErrorKind::NotFound => Ok(()),
        Err(err) => Err(format!("inspect {}: {err}", link.display())),
        Ok(_) if link_points_at(link, target) => Ok(()),
        Ok(_) => Err(format!(
            "{} already exists and does not point at this app — remove it before installing the command",
            link.display()
        )),
    }
}

fn install_link(link: &Path, target: &Path) -> io::Result<()> {
    if link_points_at(link, target) {
        return Ok(());
    }
    #[cfg(unix)]
    {
        std::os::unix::fs::symlink(target, link)
    }
    #[cfg(not(unix))]
    {
        let _ = (link, target);
        Err(io::Error::new(
            io::ErrorKind::Unsupported,
            "shell command installation requires Unix",
        ))
    }
}

fn validate_uninstall_link(link: &Path, target: &Path) -> io::Result<()> {
    match fs::symlink_metadata(link) {
        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(()),
        Err(error) => Err(error),
        Ok(_) if link_points_at(link, target) => Ok(()),
        Ok(_) => Err(io::Error::other(format!(
            "{} does not point at the current app binary — refusing to remove it",
            link.display()
        ))),
    }
}

fn uninstall_link(link: &Path, target: &Path) -> io::Result<()> {
    validate_uninstall_link(link, target)?;
    match fs::remove_file(link) {
        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(()),
        result => result,
    }
}

fn install_with(bin_dir: &Path, bins: &BundleBins) -> Result<(), String> {
    let pw_link = bin_dir.join(ENGINE_NAME);
    let logs_link = bin_dir.join(LOGS_NAME);
    validate_install_link(&pw_link, &bins.pw)?;
    validate_install_link(&logs_link, &bins.pw_logs)?;
    let installed = fs::create_dir_all(bin_dir)
        .and_then(|_| install_link(&pw_link, &bins.pw))
        .and_then(|_| install_link(&logs_link, &bins.pw_logs));
    match installed {
        Ok(()) => Ok(()),
        Err(err) if err.kind() == io::ErrorKind::PermissionDenied => {
            run_privileged_shell(&privileged_install_script(bin_dir, bins))
        }
        Err(err) => Err(format!("install shell commands: {err}")),
    }
}

fn privileged_install_script(bin_dir: &Path, bins: &BundleBins) -> String {
    let mut script = format!("set -eu; mkdir -p {}", shell_single_quote(bin_dir));
    // Elevated execution rechecks both destinations before creating links.
    for (name, target) in [(ENGINE_NAME, &bins.pw), (LOGS_NAME, &bins.pw_logs)] {
        let link = shell_single_quote(&bin_dir.join(name));
        let target = shell_single_quote(target);
        script.push_str(&format!(
            "; if [ -e {link} ] || [ -L {link} ]; then [ -L {link} ] && [ \"$(readlink {link})\" = {target} ] || {{ echo 'Command destination already exists' >&2; exit 1; }}; fi"
        ));
    }
    for (name, target) in [(ENGINE_NAME, &bins.pw), (LOGS_NAME, &bins.pw_logs)] {
        let link = shell_single_quote(&bin_dir.join(name));
        // Atomic creation refuses destinations created after validation.
        script.push_str(&format!(
            "; if [ ! -L {link} ]; then {}; fi; [ -L {link} ] && [ \"$(readlink {link})\" = {} ]",
            privileged_create_link(&bin_dir.join(name), target),
            shell_single_quote(target)
        ));
    }
    script
}

fn privileged_create_link(link: &Path, target: &Path) -> String {
    let link = serde_json::to_string(&link.to_string_lossy()).expect("serialize link path");
    let target = serde_json::to_string(&target.to_string_lossy()).expect("serialize target path");
    let source = format!(
        "ObjC.import('Foundation'); var error = Ref(); if (!$.NSFileManager.defaultManager.createSymbolicLinkAtPathWithDestinationPathError({link}, {target}, error)) {{ throw Error(ObjC.unwrap(error[0].localizedDescription)); }}"
    );
    format!(
        "/usr/bin/osascript -l JavaScript -e '{}'",
        source.replace('\'', "'\\''")
    )
}

fn privileged_uninstall_script(bin_dir: &Path, bins: &BundleBins) -> String {
    let mut script = String::from("set -eu");
    for (name, target) in [(ENGINE_NAME, &bins.pw), (LOGS_NAME, &bins.pw_logs)] {
        let link = shell_single_quote(&bin_dir.join(name));
        let target = shell_single_quote(target);
        script.push_str(&format!(
            "; if [ -e {link} ] || [ -L {link} ]; then [ -L {link} ] && [ \"$(readlink {link})\" = {target} ] || {{ echo 'Command destination does not point at this app' >&2; exit 1; }}; fi"
        ));
    }
    script.push_str(&format!(
        "; rm -f {} {}",
        shell_single_quote(&bin_dir.join(ENGINE_NAME)),
        shell_single_quote(&bin_dir.join(LOGS_NAME))
    ));
    script
}

fn uninstall_with(bin_dir: &Path, bins: &BundleBins) -> Result<(), String> {
    let pw_link = bin_dir.join(ENGINE_NAME);
    let logs_link = bin_dir.join(LOGS_NAME);
    let removed = validate_uninstall_link(&pw_link, &bins.pw)
        .and_then(|_| validate_uninstall_link(&logs_link, &bins.pw_logs))
        .and_then(|_| uninstall_link(&pw_link, &bins.pw))
        .and_then(|_| uninstall_link(&logs_link, &bins.pw_logs));
    match removed {
        Ok(()) => Ok(()),
        Err(error) if error.kind() == io::ErrorKind::PermissionDenied => {
            run_privileged_shell(&privileged_uninstall_script(bin_dir, bins))
        }
        Err(error) => Err(format!("remove shell commands: {error}")),
    }
}

#[tauri::command]
pub fn shell_command_status() -> Result<ShellCommandStatus, String> {
    Ok(status_with(
        Path::new(INSTALL_BIN_DIR),
        resolve_bundle_bins(),
    ))
}

#[tauri::command]
pub fn install_shell_command() -> Result<ShellCommandStatus, String> {
    let bins = resolve_bundle_bins()?;
    install_with(Path::new(INSTALL_BIN_DIR), &bins)?;
    Ok(status_with(Path::new(INSTALL_BIN_DIR), Ok(bins)))
}

#[tauri::command]
pub fn uninstall_shell_command() -> Result<ShellCommandStatus, String> {
    let bins = resolve_bundle_bins()?;
    uninstall_with(Path::new(INSTALL_BIN_DIR), &bins)?;
    Ok(status_with(Path::new(INSTALL_BIN_DIR), Ok(bins)))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_support::TempDir;

    fn write_exe(path: &Path) {
        if let Some(parent) = path.parent() {
            fs::create_dir_all(parent).unwrap();
        }
        fs::write(path, b"#!/bin/sh\n").unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mut perms = fs::metadata(path).unwrap().permissions();
            perms.set_mode(0o755);
            fs::set_permissions(path, perms).unwrap();
        }
    }

    #[test]
    fn install_and_uninstall_symlinks() {
        let root = TempDir::new("shell-cmd");
        let bin_dir = root.path().join("bin");
        let bundle = root.path().join("MacOS");
        let pw = bundle.join(ENGINE_NAME);
        let logs = bundle.join(LOGS_NAME);
        write_exe(&pw);
        write_exe(&logs);
        let bins = BundleBins {
            pw: pw.clone(),
            pw_logs: logs.clone(),
        };

        install_with(&bin_dir, &bins).unwrap();
        let status = status_with(&bin_dir, Ok(bins.clone()));
        assert!(status.installed);
        assert!(status.pw_link.ends_with("/pw"));

        uninstall_with(&bin_dir, &bins).unwrap();
        let status = status_with(&bin_dir, Ok(bins));
        assert!(!status.installed);
    }

    #[test]
    fn refuses_foreign_file_at_link_path() {
        let root = TempDir::new("shell-cmd-foreign");
        let bin_dir = root.path().join("bin");
        let bundle = root.path().join("MacOS");
        let pw = bundle.join(ENGINE_NAME);
        let logs = bundle.join(LOGS_NAME);
        write_exe(&pw);
        write_exe(&logs);
        fs::create_dir_all(&bin_dir).unwrap();
        fs::write(bin_dir.join(ENGINE_NAME), b"foreign").unwrap();
        let bins = BundleBins { pw, pw_logs: logs };
        let err = install_with(&bin_dir, &bins).unwrap_err();
        assert!(err.contains("does not point at this app"), "{err}");
    }

    #[cfg(unix)]
    #[test]
    fn both_install_paths_preserve_foreign_destinations() {
        for privileged in [false, true] {
            for foreign_symlink in [false, true] {
                let root = TempDir::new("shell-command-collision");
                let bin = root.path().join("bin");
                fs::create_dir_all(&bin).unwrap();
                let bins = BundleBins {
                    pw: root.path().join("app-pw"),
                    pw_logs: root.path().join("app-logs"),
                };
                write_exe(&bins.pw);
                write_exe(&bins.pw_logs);
                let foreign = root.path().join("foreign");
                fs::write(&foreign, b"preserved").unwrap();
                let collision = bin.join(LOGS_NAME);
                if foreign_symlink {
                    std::os::unix::fs::symlink(&foreign, &collision).unwrap();
                } else {
                    fs::write(&collision, b"preserved").unwrap();
                }
                if privileged {
                    let result = Command::new("/bin/sh")
                        .arg("-c")
                        .arg(privileged_install_script(&bin, &bins))
                        .output()
                        .unwrap();
                    assert!(!result.status.success());
                } else {
                    assert!(install_with(&bin, &bins).is_err());
                }
                assert_eq!(fs::read(&collision).unwrap(), b"preserved");
                assert!(!bin.join(ENGINE_NAME).exists());
                assert_eq!(
                    fs::symlink_metadata(&collision)
                        .unwrap()
                        .file_type()
                        .is_symlink(),
                    foreign_symlink
                );
            }
        }
    }

    #[cfg(target_os = "macos")]
    #[test]
    fn elevated_creation_is_exact_and_never_writes_inside_existing_directories() {
        let root = TempDir::new("shell-command-elevated-create");
        let target = root.path().join("app's binary");
        write_exe(&target);
        let destination = root.path().join("pw");
        let create = || {
            Command::new("/bin/sh")
                .arg("-c")
                .arg(privileged_create_link(&destination, &target))
                .output()
                .unwrap()
        };
        assert!(create().status.success());
        assert_eq!(fs::read_link(&destination).unwrap(), target);
        assert!(!create().status.success());
        fs::remove_file(&destination).unwrap();
        fs::create_dir(&destination).unwrap();
        assert!(!create().status.success());
        assert_eq!(fs::read_dir(&destination).unwrap().count(), 0);
        fs::remove_dir(&destination).unwrap();
        let foreign = root.path().join("foreign-directory");
        fs::create_dir(&foreign).unwrap();
        std::os::unix::fs::symlink(&foreign, &destination).unwrap();
        assert!(!create().status.success());
        assert_eq!(fs::read_dir(&foreign).unwrap().count(), 0);
        assert_eq!(fs::read_link(&destination).unwrap(), foreign);
    }

    #[cfg(unix)]
    #[test]
    fn both_uninstall_paths_validate_all_targets_before_removing_links() {
        for privileged in [false, true] {
            for foreign_symlink in [false, true] {
                let root = TempDir::new("shell-command-uninstall-collision");
                let bin = root.path().join("bin");
                let bins = BundleBins {
                    pw: root.path().join("app-pw"),
                    pw_logs: root.path().join("app-logs"),
                };
                write_exe(&bins.pw);
                write_exe(&bins.pw_logs);
                install_with(&bin, &bins).unwrap();
                let collision = bin.join(LOGS_NAME);
                fs::remove_file(&collision).unwrap();
                let foreign = root.path().join("foreign");
                fs::write(&foreign, b"preserved").unwrap();
                if foreign_symlink {
                    std::os::unix::fs::symlink(&foreign, &collision).unwrap();
                } else {
                    fs::write(&collision, b"preserved").unwrap();
                }
                if privileged {
                    let result = Command::new("/bin/sh")
                        .arg("-c")
                        .arg(privileged_uninstall_script(&bin, &bins))
                        .output()
                        .unwrap();
                    assert!(!result.status.success());
                } else {
                    assert!(uninstall_with(&bin, &bins).is_err());
                }
                assert!(link_points_at(&bin.join(ENGINE_NAME), &bins.pw));
                assert_eq!(fs::read(&collision).unwrap(), b"preserved");
                assert_eq!(fs::read(&foreign).unwrap(), b"preserved");
            }
        }
    }

    #[cfg(unix)]
    #[test]
    fn both_uninstall_paths_reject_indirect_bundle_targets() {
        for privileged in [false, true] {
            for relative in [false, true] {
                let root = TempDir::new("shell-command-indirect-target");
                let bin = root.path().join("bin");
                let bins = BundleBins {
                    pw: root.path().join("app-pw"),
                    pw_logs: root.path().join("app-logs"),
                };
                write_exe(&bins.pw);
                write_exe(&bins.pw_logs);
                install_with(&bin, &bins).unwrap();
                let indirect = root.path().join("indirect");
                std::os::unix::fs::symlink(&bins.pw_logs, &indirect).unwrap();
                let target = if relative { PathBuf::from("../app-logs") } else { indirect };
                let link = bin.join(LOGS_NAME);
                fs::remove_file(&link).unwrap();
                std::os::unix::fs::symlink(&target, &link).unwrap();
                assert_eq!(
                    fs::canonicalize(&link).unwrap(),
                    fs::canonicalize(&bins.pw_logs).unwrap()
                );
                if privileged {
                    let result = Command::new("/bin/sh")
                        .arg("-c")
                        .arg(privileged_uninstall_script(&bin, &bins))
                        .output()
                        .unwrap();
                    assert!(!result.status.success());
                } else {
                    assert!(uninstall_with(&bin, &bins).is_err());
                }
                assert!(link_points_at(&bin.join(ENGINE_NAME), &bins.pw));
                assert_eq!(fs::read_link(&link).unwrap(), target);
            }
        }
    }

    #[cfg(unix)]
    #[test]
    fn privileged_uninstall_removes_only_verified_bundle_links() {
        let root = TempDir::new("shell-command-uninstall");
        let bin = root.path().join("bin");
        let bins = BundleBins {
            pw: root.path().join("app-pw"),
            pw_logs: root.path().join("app-logs"),
        };
        write_exe(&bins.pw);
        write_exe(&bins.pw_logs);
        install_with(&bin, &bins).unwrap();
        for _ in 0..2 {
            let result = Command::new("/bin/sh")
                .arg("-c")
                .arg(privileged_uninstall_script(&bin, &bins))
                .output()
                .unwrap();
            assert!(result.status.success());
        }
        assert_eq!(fs::read_dir(&bin).unwrap().count(), 0);
        assert!(bins.pw.exists());
        assert!(bins.pw_logs.exists());
    }

    #[test]
    fn shell_single_quote_escapes_apostrophe() {
        let path = Path::new("O'Brien/bin");
        assert_eq!(shell_single_quote(path), "'O'\\''Brien/bin'");
    }
}
