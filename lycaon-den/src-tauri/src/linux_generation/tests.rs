use super::*;

fn fixture(output: &Path, desktop: &[u8], helper: &[u8]) -> PathBuf {
    std::fs::create_dir_all(output).unwrap();
    let output = std::fs::canonicalize(output).unwrap();
    let root = output.join(PRIVATE_ROOT);
    std::fs::create_dir_all(root.join("bin")).unwrap();
    std::fs::create_dir_all(root.join("engine-root/schemas/oar")).unwrap();
    std::fs::write(root.join("engine-root/schemas/oar/oar.schema.json"), b"{}").unwrap();
    std::fs::write(root.join("bin/painted-wolf-code"), desktop).unwrap();
    std::fs::write(root.join("bin/pw"), helper).unwrap();
    std::fs::write(
        root.join("nix-layout.json"),
        serde_json::to_vec(&serde_json::json!({
            "format_version": 1, "output": output, "nix_store": "/tools/nix/bin/nix-store"
        }))
        .unwrap(),
    )
    .unwrap();
    let entries: Vec<_> = [("painted-wolf-code", desktop), ("pw", helper)].iter().map(|(role, bytes)| {
        serde_json::json!({"path": root.join("bin").join(role), "sha256": hex::encode(Sha256::digest(bytes)), "size": bytes.len()})
    }).collect();
    std::fs::write(
        root.join("manifest.json"),
        serde_json::to_vec(&serde_json::json!({
            "format_version": 1, "package": "painted-wolf-code", "version": "1.0.2",
            "architecture": "x86_64", "install_root": root, "executables": entries
        }))
        .unwrap(),
    )
    .unwrap();
    root.join("bin/painted-wolf-code")
}

#[test]
fn generation_is_bound_to_the_running_descriptor_after_profile_switch() {
    let temporary = crate::test_support::TempDir::new("nix-generation");
    let first = fixture(&temporary.join("A"), b"desktop A", b"\x7fELFhelper A");
    let second = fixture(&temporary.join("B"), b"desktop B", b"\x7fELFhelper B");
    let image = File::open(&first).unwrap();
    let generation = capture(&first, image).unwrap().unwrap();
    assert_eq!(
        generation.root,
        std::fs::canonicalize(temporary.join("A"))
            .unwrap()
            .join(PRIVATE_ROOT)
    );
    assert_eq!(
        generation.engine_root().unwrap(),
        generation.root.join("engine-root")
    );
    assert!(capture(&second, File::open(&first).unwrap()).is_err());
    // Both generations continue to resolve only their own private helper.
    let next = capture(&second, File::open(&second).unwrap())
        .unwrap()
        .unwrap();
    assert_ne!(generation.root, next.root);
    assert!(generation.helper("pw").is_ok());
    assert!(next.helper("pw").is_ok());
}

#[test]
fn changed_missing_and_symlinked_helpers_are_refused() {
    let temporary = crate::test_support::TempDir::new("nix-helper");
    let desktop = fixture(&temporary, b"desktop", b"\x7fELFhelper");
    let generation = capture(&desktop, File::open(&desktop).unwrap())
        .unwrap()
        .unwrap();
    assert!(generation.helper("../../other").is_err());
    assert!(generation.helper("bialy").is_err());
    let helper = generation.root.join("bin/pw");
    std::fs::write(&helper, b"\x7fELFchanged").unwrap();
    assert!(generation.helper("pw").is_err());
    #[cfg(unix)]
    {
        std::fs::remove_file(&helper).unwrap();
        std::os::unix::fs::symlink(&desktop, &helper).unwrap();
        assert!(generation.helper("pw").is_err());
    }
    std::fs::remove_file(
        generation
            .root
            .join("engine-root/schemas/oar/oar.schema.json"),
    )
    .unwrap();
    assert!(generation.engine_root().is_err());
}

#[test]
fn explicit_receipt_is_required_without_inferring_store_trust() {
    let temporary = crate::test_support::TempDir::new("nix-receipt");
    let desktop = fixture(
        &temporary.join("custom-store/output"),
        b"desktop",
        b"\x7fELFhelper",
    );
    let receipt = desktop
        .parent()
        .unwrap()
        .parent()
        .unwrap()
        .join("nix-layout.json");
    std::fs::write(
        &receipt,
        b"{\"format_version\":2,\"output\":\"/wrong\",\"nix_store\":\"/tools/nix-store\"}",
    )
    .unwrap();
    assert!(capture(&desktop, File::open(&desktop).unwrap()).is_err());
    std::fs::remove_file(receipt).unwrap();
    assert!(capture(&desktop, File::open(&desktop).unwrap())
        .unwrap()
        .is_none());
}

#[cfg(target_os = "linux")]
#[test]
fn captured_elf_executes_after_its_path_is_replaced() {
    use std::os::unix::fs::PermissionsExt;
    let temporary = crate::test_support::TempDir::new("nix-open-exec");
    let elf = std::fs::read("/bin/true").unwrap();
    let desktop = fixture(&temporary, b"desktop", &elf);
    let generation = capture(&desktop, File::open(&desktop).unwrap())
        .unwrap()
        .unwrap();
    let path = generation.root.join("bin/pw");
    std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o555)).unwrap();
    let image = generation.helper("pw").unwrap();
    std::fs::remove_file(&path).unwrap();
    std::fs::write(&path, b"changed").unwrap();
    assert!(std::process::Command::new(&image.path)
        .status()
        .unwrap()
        .success());
    assert!(generation.helper("pw").is_err());
}
