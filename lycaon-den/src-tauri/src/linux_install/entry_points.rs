//! Read-only launcher status: exact package wrapper bytes and executable targets.
//! This is installation status, not credential or child-process admission.

use serde::Deserialize;
use sha2::{Digest, Sha256};
use std::fs::File;
use std::io::Read;
use std::path::{Path, PathBuf};

const MANIFEST_LIMIT: u64 = 1024 * 1024;
const WRAPPER_LIMIT: u64 = 64 * 1024;

#[derive(Deserialize)]
struct Manifest {
    format_version: u32,
    install_root: PathBuf,
    executables: Vec<Entry>,
}

#[derive(Deserialize)]
struct Entry {
    path: PathBuf,
    sha256: String,
    size: u64,
}

pub(crate) fn executable(path: &Path) -> bool {
    let Ok(info) = path.metadata() else { return false };
    if !info.is_file() {
        return false;
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        info.permissions().mode() & 0o111 != 0
    }
    #[cfg(not(unix))]
    false
}

pub(crate) fn matches(root: &Path, entry_point: &Path) -> bool {
    verify(root, entry_point).is_some()
}

fn verify(root: &Path, entry_point: &Path) -> Option<()> {
    let role = entry_point.file_name()?.to_str()?;
    if !matches!(role, "pw" | "pw-logs")
        || !executable(entry_point)
        || !executable(&root.join("bin").join(role))
    {
        return None;
    }
    let mut raw = Vec::new();
    File::open(root.join("manifest.json")).ok()?
        .take(MANIFEST_LIMIT + 1).read_to_end(&mut raw).ok()?;
    if raw.len() as u64 > MANIFEST_LIMIT {
        return None;
    }
    let manifest: Manifest = serde_json::from_slice(&raw).ok()?;
    if manifest.format_version != 1 || manifest.install_root != root {
        return None;
    }
    let mut entries = manifest.executables.iter().filter(|entry| entry.path == entry_point);
    let expected = entries.next()?;
    if entries.next().is_some() || expected.size > WRAPPER_LIMIT || expected.sha256.len() != 64 {
        return None;
    }
    let mut file = File::open(entry_point).ok()?;
    if file.metadata().ok()?.len() != expected.size {
        return None;
    }
    let mut bytes = Vec::new();
    file.take(WRAPPER_LIMIT + 1).read_to_end(&mut bytes).ok()?;
    if bytes.len() as u64 != expected.size || hex::encode(Sha256::digest(&bytes)) != expected.sha256 {
        return None;
    }
    Some(())
}
