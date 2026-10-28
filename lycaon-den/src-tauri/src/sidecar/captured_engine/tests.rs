use super::*;
use std::os::unix::fs::PermissionsExt;

fn fixture() -> (crate::test_support::TempDir, std::path::PathBuf) {
    let root = crate::test_support::TempDir::new("captured-proxy-engine");
    std::fs::create_dir(root.join("bin")).unwrap();
    let desktop = root.join("bin/painted-wolf-code");
    std::fs::write(&desktop, b"running desktop image").unwrap();
    std::fs::set_permissions(&desktop, std::fs::Permissions::from_mode(0o755)).unwrap();
    let pw = root.join("bin/pw");
    std::fs::copy("/bin/echo", &pw).unwrap();
    std::fs::set_permissions(&pw, std::fs::Permissions::from_mode(0o755)).unwrap();
    let desktop_bytes = std::fs::read(&desktop).unwrap();
    let pw_bytes = std::fs::read(&pw).unwrap();
    let manifest = crate::sidecar::manifest::fixture_json(
        root.path(),
        std::env::consts::ARCH,
        &[(&desktop, &desktop_bytes), (&pw, &pw_bytes)],
        &[],
    );
    std::fs::write(
        root.join("manifest.json"),
        serde_json::to_vec(&manifest).unwrap(),
    )
    .unwrap();
    (root, desktop)
}

#[test]
fn requires_sealed_format_2_and_refuses_other_manifest_versions() {
    let (root, desktop) = fixture();
    let manifest_path = root.join("manifest.json");
    let base: serde_json::Value =
        serde_json::from_slice(&std::fs::read(&manifest_path).unwrap()).unwrap();
    let mut v1 = base.clone();
    v1["format_version"] = serde_json::json!(1);
    v1.as_object_mut().unwrap().remove("payload");
    std::fs::write(&manifest_path, serde_json::to_vec(&v1).unwrap()).unwrap();
    assert!(matches!(
        capture(&root, &desktop, false),
        Err(AdmissionError::ManifestUnavailable)
    ));

    for bad_version in [0, 1, 3, 99] {
        let mut manifest = base.clone();
        manifest["format_version"] = serde_json::json!(bad_version);
        std::fs::write(&manifest_path, serde_json::to_vec(&manifest).unwrap()).unwrap();
        assert!(
            matches!(
                capture(&root, &desktop, false),
                Err(AdmissionError::ManifestUnavailable)
            ),
            "format_version {bad_version} with payload must be refused"
        );
    }
    let mut v2_no_payload = base;
    v2_no_payload.as_object_mut().unwrap().remove("payload");
    std::fs::write(&manifest_path, serde_json::to_vec(&v2_no_payload).unwrap()).unwrap();
    assert!(matches!(
        capture(&root, &desktop, false),
        Err(AdmissionError::ManifestUnavailable)
    ));
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
