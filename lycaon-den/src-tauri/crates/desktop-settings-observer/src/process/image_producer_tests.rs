use super::*;
use serde_json::Value;
use std::{
    fs,
    os::unix::fs::PermissionsExt,
    path::PathBuf,
    sync::atomic::{AtomicU64, Ordering},
};

struct Directory(PathBuf);
impl Directory {
    fn new() -> Self {
        static NEXT: AtomicU64 = AtomicU64::new(0);
        let base = std::env::temp_dir().join(format!(
            "observer-producer-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir(&base).unwrap();
        Self(base.canonicalize().unwrap())
    }
}
impl Drop for Directory {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.0).unwrap();
    }
}

#[test]
fn shared_producer_manifest_opens_the_observer_and_rejects_changed_identity() {
    let corpus =
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../../packaging/linux/manifest/testdata");
    let architecture = std::env::consts::ARCH;
    let raw = fs::read(corpus.join(format!("{architecture}.json"))).unwrap();
    let canonical: Value = serde_json::from_slice(&raw).unwrap();
    let bytes = fs::read(corpus.join(format!("{architecture}.elf"))).unwrap();
    let directory = Directory::new();
    let root = directory.0.join("opt/painted-wolf-code");
    let bin = root.join("bin");
    fs::create_dir_all(&bin).unwrap();
    for path in [&directory.0, &directory.0.join("opt"), &root, &bin] {
        fs::set_permissions(path, fs::Permissions::from_mode(0o755)).unwrap();
    }
    let shell = bin.join("painted-wolf-code");
    fs::write(&shell, &bytes).unwrap();
    fs::set_permissions(&shell, fs::Permissions::from_mode(0o755)).unwrap();
    let observer = bin.join(NAME);
    fs::write(&observer, &bytes).unwrap();
    fs::set_permissions(&observer, fs::Permissions::from_mode(0o755)).unwrap();
    // Only relocate the physical prefix; retain producer shape and all sealed fields.
    let mut document = canonical.clone();
    let previous = canonical["install_root"].as_str().unwrap();
    document["install_root"] = serde_json::json!(root);
    for row in document["executables"].as_array_mut().unwrap() {
        let path = row["path"].as_str().unwrap();
        if let Some(relative) = path.strip_prefix(&format!("{previous}/")) {
            row["path"] = serde_json::json!(root.join(relative));
        }
    }
    let manifest_path = root.join("manifest.json");
    fs::write(&manifest_path, serde_json::to_vec(&document).unwrap()).unwrap();
    fs::set_permissions(&manifest_path, fs::Permissions::from_mode(0o644)).unwrap();
    let owner = unsafe { libc::geteuid() };
    assert!(Image::open_at(&shell, &directory.0, owner).is_ok());
    for mutation in ["unknown version", "missing seal", "changed bytes"] {
        let mut bad = document.clone();
        fs::write(&observer, &bytes).unwrap();
        match mutation {
            "unknown version" => bad["format_version"] = serde_json::json!(99),
            "missing seal" => {
                bad.as_object_mut().unwrap().remove("payload");
            }
            _ => {
                let mut changed = bytes.clone();
                changed[32] ^= 1;
                fs::write(&observer, changed).unwrap();
            }
        }
        fs::write(&manifest_path, serde_json::to_vec(&bad).unwrap()).unwrap();
        assert!(
            Image::open_at(&shell, &directory.0, owner).is_err(),
            "{mutation}"
        );
    }
}
