use serde::Deserialize;
use sha2::{Digest, Sha256};
use std::{
    fs::{File, OpenOptions},
    io::{self, Read},
    os::unix::{
        fs::{MetadataExt, OpenOptionsExt},
        io::AsRawFd,
    },
    path::{Path, PathBuf},
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
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Entry {
    path: String,
    sha256: String,
    size: u64,
}
pub(super) struct Image {
    file: File,
}
fn invalid() -> io::Error {
    io::Error::new(
        io::ErrorKind::InvalidData,
        "desktop settings image is not its installed manifest identity",
    )
}
fn secure(path: &Path) -> io::Result<()> {
    for item in path.ancestors() {
        let info = std::fs::symlink_metadata(item)?;
        if info.uid() != 0
            || info.file_type().is_symlink()
            || (info.mode() & 0o022 != 0 && !(info.is_dir() && info.mode() & 0o1000 != 0))
        {
            return Err(invalid());
        }
    }
    Ok(())
}
impl Image {
    pub(super) fn open(shell: &Path) -> io::Result<Self> {
        let shell = shell.canonicalize()?;
        let root = shell.parent().and_then(Path::parent).ok_or_else(invalid)?;
        let path = root.join("bin").join(NAME);
        secure(&path)?;
        let manifest_path = root.join("manifest.json");
        secure(&manifest_path)?;
        let mut raw = Vec::new();
        OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NOFOLLOW)
            .open(manifest_path)?
            .take((4 << 20) + 1)
            .read_to_end(&mut raw)?;
        if raw.len() > 4 << 20 {
            return Err(invalid());
        }
        let manifest: Manifest = serde_json::from_slice(&raw)?;
        if manifest.format_version != 1
            || manifest.package != "painted-wolf-code"
            || manifest.version.is_empty()
            || manifest.architecture != std::env::consts::ARCH
            || Path::new(&manifest.install_root) != root
            || manifest.executables.len() > 8192
        {
            return Err(invalid());
        }
        let entries: Vec<_> = manifest
            .executables
            .iter()
            .filter(|entry| Path::new(&entry.path) == path)
            .collect();
        if entries.len() != 1 {
            return Err(invalid());
        }
        let expected = entries[0];
        let mut file = OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NOFOLLOW)
            .open(path)?;
        let before = file.metadata()?;
        if !before.is_file()
            || before.mode() & 0o111 == 0
            || before.mode() & 0o6022 != 0
            || before.len() != expected.size
            || before.len() > 16 << 20
        {
            return Err(invalid());
        }
        let mut hasher = Sha256::new();
        let mut header = [0u8; 20];
        file.read_exact(&mut header)?;
        hasher.update(header);
        let machine = if std::env::consts::ARCH == "x86_64" {
            62u16
        } else if std::env::consts::ARCH == "aarch64" {
            183
        } else {
            return Err(invalid());
        };
        if &header[..4] != b"\x7fELF"
            || header[4] != 2
            || header[5] != 1
            || u16::from_le_bytes([header[18], header[19]]) != machine
        {
            return Err(invalid());
        }
        let mut chunk = [0u8; 65536];
        loop {
            let count = file.read(&mut chunk)?;
            if count == 0 {
                break;
            };
            hasher.update(&chunk[..count]);
        }
        let after = file.metadata()?;
        if format!("{:x}", hasher.finalize()) != expected.sha256
            || (
                before.dev(),
                before.ino(),
                before.len(),
                before.mtime(),
                before.mtime_nsec(),
            ) != (
                after.dev(),
                after.ino(),
                after.len(),
                after.mtime(),
                after.mtime_nsec(),
            )
        {
            return Err(invalid());
        }
        Ok(Self { file })
    }
    pub(super) fn executable(&self) -> PathBuf {
        PathBuf::from(format!("/proc/self/fd/{}", self.file.as_raw_fd()))
    }
}
