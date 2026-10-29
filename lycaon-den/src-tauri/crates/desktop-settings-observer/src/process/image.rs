use serde::Deserialize;
use sha2::{Digest, Sha256};
use std::{
    collections::BTreeSet,
    fs::{File, OpenOptions},
    io::{self, Read},
    os::unix::fs::{MetadataExt, OpenOptionsExt},
    path::{Component, Path},
};
const NAME: &str = "pw-desktop-settings";
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Manifest {
    format_version: u8,
    package: String,
    version: String,
    architecture: String,
    install_root: String,
    executables: Vec<Entry>,
    payload: Vec<PayloadEntry>,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Entry {
    path: String,
    sha256: String,
    size: u64,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct PayloadEntry {
    path: String,
    kind: String,
    mode: u32,
    #[serde(deserialize_with = "Option::deserialize")]
    sha256: Option<String>,
    #[serde(deserialize_with = "Option::deserialize")]
    size: Option<u64>,
    #[serde(deserialize_with = "Option::deserialize")]
    target: Option<String>,
}
pub(super) struct Image {
    file: File,
}
fn invalid() -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, "desktop settings image is not its installed manifest identity")
}
fn valid_digest(digest: &str) -> bool {
    digest.len() == 64 && digest.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}
fn clean_relative(path: &str) -> bool {
    !path.is_empty() && path != "manifest.json" && !path.starts_with('/') && !path.ends_with('/') && !path.contains("//")
        && Path::new(path).components().all(|part| matches!(part, Component::Normal(_)))
}
fn verify_payload(payload: &[PayloadEntry], expected: &Entry) -> io::Result<()> {
    if payload.is_empty() {
        return Err(invalid());
    }
    let (mut seen, mut has_bin_dir, mut matched) = (BTreeSet::new(), false, false);
    let observer_path = format!("bin/{NAME}");
    for item in payload {
        if !clean_relative(&item.path) || !seen.insert(item.path.as_str()) || item.mode > 0o777 || (item.kind != "symlink" && item.mode & 0o022 != 0) {
            return Err(invalid());
        }
        let valid = match item.kind.as_str() {
            "file" => item.sha256.as_deref().is_some_and(valid_digest) && item.size.is_some() && item.target.is_none(),
            "directory" => item.mode & 0o500 == 0o500 && item.sha256.is_none() && item.size.is_none() && item.target.is_none(),
            "symlink" => item.mode == 0o777 && item.sha256.is_none() && item.size.is_none() && item.target.as_deref().is_some_and(|t| !t.is_empty() && !t.starts_with('/')),
            _ => false,
        };
        if !valid {
            return Err(invalid());
        }
        has_bin_dir |= item.path == "bin" && item.kind == "directory";
        if item.path == observer_path {
            if item.kind != "file" || item.mode & 0o111 == 0 || item.sha256.as_deref() != Some(expected.sha256.as_str()) || item.size != Some(expected.size) {
                return Err(invalid());
            }
            matched = true;
        }
    }
    (has_bin_dir && matched).then_some(()).ok_or_else(invalid)
}
fn secure(path: &Path, anchor: &Path, owner: u32) -> io::Result<()> {
    if !path.starts_with(anchor) {
        return Err(invalid());
    }
    for item in path.ancestors() {
        let info = std::fs::symlink_metadata(item)?;
        if (info.uid() != 0 && info.uid() != owner) || info.file_type().is_symlink() || info.mode() & 0o022 != 0 {
            return Err(invalid());
        }
        if item == anchor {
            return Ok(());
        }
    }
    Err(invalid())
}
impl Image {
    pub(super) fn open(shell: &Path) -> io::Result<Self> {
        Self::open_at(shell, Path::new("/"), 0)
    }
    fn open_at(shell: &Path, anchor: &Path, owner: u32) -> io::Result<Self> {
        let shell = shell.canonicalize()?;
        let root = shell.parent().and_then(Path::parent).ok_or_else(invalid)?;
        let path = root.join("bin").join(NAME);
        secure(&path, anchor, owner)?;
        let manifest_path = root.join("manifest.json");
        secure(&manifest_path, anchor, owner)?;
        let mut raw = Vec::new();
        let manifest_file = OpenOptions::new().read(true).custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK).open(manifest_path)?;
        if !manifest_file.metadata()?.is_file() {
            return Err(invalid());
        }
        manifest_file.take((4 << 20) + 1).read_to_end(&mut raw)?;
        if raw.len() > 4 << 20 {
            return Err(invalid());
        }
        let manifest: Manifest = serde_json::from_slice(&raw)?;
        if manifest.format_version != 2
            || manifest.package != "painted-wolf-code"
            || manifest.version.is_empty()
            || manifest.architecture != std::env::consts::ARCH
            || Path::new(&manifest.install_root) != root
            || manifest.executables.is_empty()
            || manifest.executables.len() > 8192
        {
            return Err(invalid());
        }
        let entries: Vec<_> = manifest.executables.iter().filter(|entry| Path::new(&entry.path) == path).collect();
        if entries.len() != 1 || !valid_digest(&entries[0].sha256) {
            return Err(invalid());
        }
        let expected = entries[0];
        verify_payload(&manifest.payload, expected)?;
        let mut file = OpenOptions::new().read(true).custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK).open(path)?;
        let before = file.metadata()?;
        if !before.is_file() || before.mode() & 0o111 == 0 || before.mode() & 0o6022 != 0 || before.len() != expected.size || before.len() > 16 << 20 {
            return Err(invalid());
        }
        let mut hasher = Sha256::new();
        let mut header = [0u8; 20];
        file.read_exact(&mut header)?;
        hasher.update(header);
        let machine = match std::env::consts::ARCH {
            "x86_64" => 62u16,
            "aarch64" => 183,
            _ => return Err(invalid()),
        };
        if &header[..4] != b"\x7fELF" || header[4] != 2 || header[5] != 1 || u16::from_le_bytes([header[18], header[19]]) != machine {
            return Err(invalid());
        }
        let mut chunk = [0u8; 65536];
        loop {
            let count = file.read(&mut chunk)?;
            if count == 0 {
                break;
            }
            hasher.update(&chunk[..count]);
        }
        let after = file.metadata()?;
        let digest: String = hasher.finalize().iter().map(|byte| format!("{byte:02x}")).collect();
        if digest != expected.sha256
            || (before.dev(), before.ino(), before.len(), before.mtime(), before.mtime_nsec())
                != (after.dev(), after.ino(), after.len(), after.mtime(), after.mtime_nsec())
        {
            return Err(invalid());
        }
        Ok(Self { file })
    }
    /// The verified image, which the launcher executes by descriptor.
    pub(super) fn file(&self) -> &File {
        &self.file
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::{json, Value};
    use std::{fs::Permissions, os::unix::fs::PermissionsExt, path::PathBuf, sync::atomic::{AtomicU64, Ordering}};

    struct Fixture {
        base: PathBuf,
        root: PathBuf,
        shell: PathBuf,
        sha256: String,
        size: usize,
        uid: u32,
    }
    impl Fixture {
        fn new() -> Self {
            static NEXT: AtomicU64 = AtomicU64::new(0);
            let raw = std::env::temp_dir().join(format!("pw-observer-image-{}-{}", std::process::id(), NEXT.fetch_add(1, Ordering::Relaxed)));
            std::fs::create_dir_all(&raw).unwrap();
            let base = raw.canonicalize().unwrap();
            let root = base.join("opt/painted-wolf-code");
            let bin = root.join("bin");
            std::fs::create_dir_all(&bin).unwrap();
            for dir in [&base, &base.join("opt"), &root, &bin] {
                std::fs::set_permissions(dir, Permissions::from_mode(0o755)).unwrap();
            }
            let shell = bin.join("painted-wolf-code");
            std::fs::write(&shell, b"shell").unwrap();
            std::fs::set_permissions(&shell, Permissions::from_mode(0o755)).unwrap();
            let mut elf = [0u8; 64];
            elf[..6].copy_from_slice(b"\x7fELF\x02\x01");
            let machine: u16 = if std::env::consts::ARCH == "aarch64" { 183 } else { 62 };
            elf[18..20].copy_from_slice(&machine.to_le_bytes());
            let observer = bin.join(NAME);
            std::fs::write(&observer, elf).unwrap();
            std::fs::set_permissions(&observer, Permissions::from_mode(0o755)).unwrap();
            let sha256 = Sha256::digest(elf).iter().map(|b| format!("{b:02x}")).collect();
            let fixture = Self { base, root, shell, sha256, size: elf.len(), uid: unsafe { libc::geteuid() } };
            fixture.write_manifest(&fixture.manifest_v2());
            fixture
        }
        fn manifest_v2(&self) -> Value {
            let observer_abs = self.root.join("bin").join(NAME);
            json!({
                "format_version": 2,
                "package": "painted-wolf-code",
                "version": "1.2.3",
                "architecture": std::env::consts::ARCH,
                "install_root": self.root.to_string_lossy(),
                "executables": [{"path": observer_abs.to_string_lossy(), "sha256": self.sha256, "size": self.size}],
                "payload": [
                    {"path": "bin", "kind": "directory", "mode": 0o755, "sha256": null, "size": null, "target": null},
                    {"path": format!("bin/{NAME}"), "kind": "file", "mode": 0o755, "sha256": self.sha256, "size": self.size, "target": null}
                ]
            })
        }
        fn write_manifest(&self, value: &Value) {
            let path = self.root.join("manifest.json");
            std::fs::write(&path, serde_json::to_vec(value).unwrap()).unwrap();
            std::fs::set_permissions(&path, Permissions::from_mode(0o644)).unwrap();
        }
        fn open(&self) -> io::Result<Image> {
            Image::open_at(&self.shell, &self.base, self.uid)
        }
    }
    impl Drop for Fixture {
        fn drop(&mut self) {
            let _ = std::fs::set_permissions(self.base.join("opt"), Permissions::from_mode(0o755));
            let _ = std::fs::remove_dir_all(&self.base);
        }
    }

    #[test]
    fn format_2_manifest_and_payload_seal_are_verified() {
        let fixture = Fixture::new();
        assert!(fixture.open().unwrap().file().metadata().is_ok());
        for mutate in [
            |m: &mut Value| { m["format_version"] = json!(1); },
            |m: &mut Value| { m["format_version"] = json!(1); m.as_object_mut().unwrap().remove("payload"); },
            |m: &mut Value| { m.as_object_mut().unwrap().remove("payload"); },
            |m: &mut Value| { m["payload"][1]["sha256"] = json!("0".repeat(64)); },
            |m: &mut Value| { m["payload"][1]["size"] = json!(1); },
            |m: &mut Value| { m["payload"][1]["mode"] = json!(0o644); },
            |m: &mut Value| { m["payload"][1]["mode"] = json!(0o775); },
            |m: &mut Value| { m["payload"] = json!([m["payload"][0].clone()]); },
        ] {
            let mut bad = fixture.manifest_v2();
            mutate(&mut bad);
            fixture.write_manifest(&bad);
            assert!(fixture.open().is_err());
        }
    }

    #[test]
    fn unknown_future_manifest_versions_are_refused() {
        let fixture = Fixture::new();
        for version in [0u8, 1, 3, 99] {
            let mut bad = fixture.manifest_v2();
            bad["format_version"] = json!(version);
            fixture.write_manifest(&bad);
            assert_eq!(fixture.open().err().unwrap().kind(), io::ErrorKind::InvalidData);
        }
    }

    #[test]
    fn sticky_writable_ancestors_are_refused() {
        let fixture = Fixture::new();
        let ancestor = fixture.base.join("opt");
        for mode in [0o1777, 0o1775] {
            std::fs::set_permissions(&ancestor, Permissions::from_mode(mode)).unwrap();
            assert_eq!(fixture.open().err().unwrap().kind(), io::ErrorKind::InvalidData);
        }
        std::fs::set_permissions(&ancestor, Permissions::from_mode(0o755)).unwrap();
        assert!(fixture.open().is_ok());
        assert!(Image::open_at(&fixture.shell, Path::new("/"), fixture.uid).is_err());
        assert!(Image::open(&fixture.shell).is_err());
    }
}

#[cfg(test)]
#[path = "image_producer_tests.rs"]
mod producer_contract_tests;
