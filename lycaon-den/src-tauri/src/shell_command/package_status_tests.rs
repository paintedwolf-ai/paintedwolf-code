use super::*;
use crate::installation_mode::InstallationMode;
use crate::test_support::TempDir;
use std::os::unix::fs::{symlink, PermissionsExt};

fn stage() -> (TempDir, InstallLocation) {
    let directory = TempDir::new("package-launcher-status");
    let root = directory.path().join("opt");
    let bin_dir = directory.path().join("usr-bin");
    fs::create_dir_all(root.join("bin")).unwrap();
    fs::create_dir_all(&bin_dir).unwrap();
    let mut staged_executables = Vec::new();
    for role in [ENGINE_NAME, LOGS_NAME] {
        let path = bin_dir.join(role);
        let helper = root.join("bin").join(role);
        let bytes = format!("#!/bin/sh\nexec '{}' \"$@\"\n", helper.display()).into_bytes();
        for target in [&path, &helper] {
            fs::write(target, &bytes).unwrap();
            fs::set_permissions(target, fs::Permissions::from_mode(0o755)).unwrap();
        }
        staged_executables.push((path, bytes.clone()));
        staged_executables.push((helper, bytes));
    }
    let refs: Vec<(&Path, &[u8])> = staged_executables
        .iter()
        .map(|(path, bytes)| (path.as_path(), bytes.as_slice()))
        .collect();
    let manifest = crate::sidecar::manifest::fixture_json(&root, "1.0.2", &refs);
    fs::write(root.join("manifest.json"), serde_json::to_vec(&manifest).unwrap()).unwrap();
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
    for condition in ["directory", "nonexecutable", "foreign-link", "dangling-link", "missing-helper", "changed-wrapper", "manifest", "legacy-v1-manifest", "future-v3-manifest"] {
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
            "legacy-v1-manifest" | "future-v3-manifest" => {
                let InstallationMode::Package { root } = &location.mode else { unreachable!() };
                let manifest_path = root.join("manifest.json");
                let mut raw: serde_json::Value = serde_json::from_slice(&fs::read(&manifest_path).unwrap()).unwrap();
                raw["format_version"] = serde_json::json!(if condition == "legacy-v1-manifest" { 1 } else { 3 });
                fs::write(&manifest_path, serde_json::to_vec(&raw).unwrap()).unwrap();
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
