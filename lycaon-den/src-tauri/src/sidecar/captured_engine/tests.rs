use super::*;
use std::os::unix::fs::PermissionsExt;

fn fixture() -> (crate::test_support::TempDir, std::path::PathBuf) {
    let root = crate::test_support::TempDir::new("captured-proxy-engine");
    std::fs::create_dir(root.join("bin")).unwrap();
    let desktop = root.join("running-desktop");
    std::fs::write(&desktop, b"running desktop image").unwrap();
    std::fs::copy("/bin/echo", root.join("bin/pw")).unwrap();
    std::fs::set_permissions(root.join("bin/pw"), std::fs::Permissions::from_mode(0o755)).unwrap();
    let row = |path: std::path::PathBuf, contents: Vec<u8>| {
        serde_json::json!({
            "path": path, "size": contents.len(), "sha256": hex::encode(Sha256::digest(contents))
        })
    };
    let manifest = serde_json::json!({
        "format_version": 1, "package": "painted-wolf-code", "version": "1.0.2",
        "architecture": std::env::consts::ARCH, "install_root": root.path(),
        "executables": [row(root.join("bin/painted-wolf-code"), std::fs::read(&desktop).unwrap()),
            row(root.join("bin/pw"), std::fs::read(root.join("bin/pw")).unwrap())]
    });
    std::fs::write(
        root.join("manifest.json"),
        serde_json::to_vec(&manifest).unwrap(),
    )
    .unwrap();
    (root, desktop)
}

#[tokio::test]
async fn nix_layout_captures_its_running_generation_without_claiming_custody_authority() {
    let (root, desktop) = fixture();
    let mode = crate::installation_mode::InstallationMode::Nix {
        root: root.path().to_path_buf(),
        output: root.path().to_path_buf(),
    };
    let captured = capture_installation(&mode, &desktop).unwrap();
    std::fs::rename(root.join("bin/pw"), root.join("old-engine")).unwrap();
    std::fs::write(root.join("bin/pw"), b"another profile generation").unwrap();
    let output = captured
        .command()
        .unwrap()
        .arg("nix-generation")
        .output()
        .await
        .unwrap();
    assert!(output.status.success());
    assert_eq!(output.stdout, b"nix-generation\n");
    assert!(matches!(
        capture_installation(&crate::installation_mode::InstallationMode::Developer, &desktop),
        Err(AdmissionError::UnqualifiedInstallation)
    ));
}

#[tokio::test]
async fn retained_engine_executes_after_its_installed_path_is_replaced() {
    let (root, desktop) = fixture();
    let captured = capture(&root, &desktop, false).unwrap();
    std::fs::rename(root.join("bin/pw"), root.join("old-engine")).unwrap();
    std::fs::write(root.join("bin/pw"), b"different installed generation").unwrap();
    let output = captured
        .command()
        .unwrap()
        .arg("retained-generation")
        .output()
        .await
        .unwrap();
    assert!(output.status.success());
    assert_eq!(output.stdout, b"retained-generation\n");
}

#[test]
fn mismatched_running_desktop_or_engine_is_refused() {
    let (root, desktop) = fixture();
    std::fs::write(&desktop, b"replacement desktop").unwrap();
    assert!(matches!(
        capture(&root, &desktop, false),
        Err(AdmissionError::GenerationMismatch)
    ));
    let (root, desktop) = fixture();
    std::fs::write(root.join("bin/pw"), b"different engine").unwrap();
    assert!(matches!(
        capture(&root, &desktop, false),
        Err(AdmissionError::GenerationMismatch)
    ));
}

#[test]
fn changed_open_engine_and_invalid_manifest_are_refused() {
    let (root, desktop) = fixture();
    let captured = capture(&root, &desktop, false).unwrap();
    std::fs::write(root.join("bin/pw"), b"in-place replacement").unwrap();
    assert!(matches!(
        captured.command(),
        Err(AdmissionError::GenerationMismatch)
    ));
    std::fs::write(root.join("manifest.json"), b"{}").unwrap();
    assert!(matches!(
        capture(&root, &desktop, false),
        Err(AdmissionError::ManifestUnavailable)
    ));
    std::fs::write(
        root.join("manifest.json"),
        vec![b' '; MANIFEST_LIMIT as usize + 1],
    )
    .unwrap();
    assert!(matches!(
        capture(&root, &desktop, false),
        Err(AdmissionError::ManifestUnavailable)
    ));
}

#[test]
fn duplicate_identity_rows_and_unsafe_permissions_are_refused() {
    let (root, desktop) = fixture();
    let mut manifest: serde_json::Value =
        serde_json::from_slice(&std::fs::read(root.join("manifest.json")).unwrap()).unwrap();
    let rows = manifest["executables"].as_array_mut().unwrap();
    rows.push(rows[0].clone());
    std::fs::write(
        root.join("manifest.json"),
        serde_json::to_vec(&manifest).unwrap(),
    )
    .unwrap();
    assert!(matches!(
        capture(&root, &desktop, false),
        Err(AdmissionError::ManifestUnavailable)
    ));
    std::fs::set_permissions(
        root.join("manifest.json"),
        std::fs::Permissions::from_mode(0o666),
    )
    .unwrap();
    assert!(matches!(
        capture(&root, &desktop, true),
        Err(AdmissionError::ManifestUnavailable)
    ));
}
