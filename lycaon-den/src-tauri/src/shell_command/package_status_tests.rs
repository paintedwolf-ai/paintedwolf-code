use super::*;
use crate::installation_mode::InstallationMode;
use crate::test_support::TempDir;
use sha2::{Digest, Sha256};
use std::os::unix::fs::{symlink, PermissionsExt};

fn stage() -> (TempDir, InstallLocation) {
    let directory = TempDir::new("package-launcher-status");
    let root = directory.path().join("opt");
    let bin_dir = directory.path().join("usr-bin");
    fs::create_dir_all(root.join("bin")).unwrap();
    fs::create_dir_all(&bin_dir).unwrap();
    let mut entries = Vec::new();
    for role in [ENGINE_NAME, LOGS_NAME] {
        let path = bin_dir.join(role);
        let bytes = format!("#!/bin/sh\nexec '{}' \"$@\"\n", root.join("bin").join(role).display());
        for target in [&path, &root.join("bin").join(role)] {
            fs::write(target, bytes.as_bytes()).unwrap();
            fs::set_permissions(target, fs::Permissions::from_mode(0o755)).unwrap();
        }
        entries.push(serde_json::json!({"path": path, "sha256": hex::encode(Sha256::digest(bytes.as_bytes())), "size": bytes.len()}));
    }
    fs::write(root.join("manifest.json"), serde_json::to_vec(&serde_json::json!({
        "format_version": 1, "install_root": root, "executables": entries
    })).unwrap()).unwrap();
    (directory, InstallLocation { bin_dir, managed_by_package: true, mode: InstallationMode::Package { root } })
}

#[test]
fn status_accepts_exact_package_wrappers_without_bundle_links() {
    let (_directory, location) = stage();
    let status = status_with(&location, Err("no bundle".into()));
    assert!(status.installed);
    assert!(status.managed_by_package);
    assert_eq!(status.installation_mode, "package");
    assert_eq!(status.unavailable_reason, None);
    assert_eq!(status.pw_target.as_deref(), fs::canonicalize(location.bin_dir.join(ENGINE_NAME)).unwrap().to_str());
}

#[test]
fn directories_nonexecutables_foreign_links_and_missing_helpers_are_not_installed() {
    for condition in ["directory", "nonexecutable", "foreign-link", "dangling-link", "missing-helper", "changed-wrapper", "manifest"] {
        let (directory, location) = stage();
        let path = location.bin_dir.join(ENGINE_NAME);
        match condition {
            "directory" => { fs::remove_file(&path).unwrap(); fs::create_dir(&path).unwrap(); }
            "nonexecutable" => fs::set_permissions(&path, fs::Permissions::from_mode(0o644)).unwrap(),
            "foreign-link" | "dangling-link" => {
                let target = directory.path().join("foreign");
                if condition == "foreign-link" {
                    fs::write(&target, b"#!/bin/sh\nexit 42\n").unwrap();
                    fs::set_permissions(&target, fs::Permissions::from_mode(0o755)).unwrap();
                }
                fs::remove_file(&path).unwrap(); symlink(target, &path).unwrap();
            }
            "missing-helper" => {
                let InstallationMode::Package { root } = &location.mode else { unreachable!() };
                fs::remove_file(root.join("bin/pw")).unwrap();
            }
            "changed-wrapper" => fs::write(&path, b"#!/bin/sh\nexit 42\n").unwrap(),
            "manifest" => {
                let InstallationMode::Package { root } = &location.mode else { unreachable!() };
                fs::write(root.join("manifest.json"), b"{}").unwrap();
            }
            _ => unreachable!(),
        }
        let before = fs::symlink_metadata(&path).unwrap().file_type();
        let status = status_with(&location, Err("no bundle".into()));
        assert!(!status.installed, "{condition}");
        assert!(status.managed_by_package);
        assert!(status.unavailable_reason.as_deref().unwrap().contains("package manager"));
        assert_eq!(fs::symlink_metadata(&path).unwrap().file_type(), before);
    }
}
