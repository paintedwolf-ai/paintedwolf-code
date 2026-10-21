//! The immutable package that contains this process. These facts describe
//! layout; only the identity service's administrator-selected manifest is trust.

use crate::sidecar::manifest::{Manifest, VerifyError, MANIFEST_LIMIT};
use serde::Deserialize;
use std::fs::File;
use std::io::{Read, Seek, SeekFrom};
use std::path::{Path, PathBuf};
use std::sync::OnceLock;

mod retention;
#[cfg(test)]
mod tests;
pub(crate) use retention::retain;

const PRIVATE_ROOT: &str = "lib/painted-wolf-code";
const ROLES: &[&str] = &[
    "painted-wolf-code",
    "pw",
    "pw-logs",
    "pw-document-core",
    "bialy",
    "pw-vault-identity",
];

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct LayoutReceipt {
    format_version: u32,
    output: PathBuf,
    nix_store: PathBuf,
}

pub(crate) struct Generation {
    pub(crate) root: PathBuf,
    pub(crate) output: PathBuf,
    pub(crate) version: String,
    manifest: Manifest,
    // Retain the live image rather than a profile pathname that can change.
    _running: File,
}

pub(crate) fn running() -> Result<Option<&'static Generation>, String> {
    static RUNNING: OnceLock<Result<Option<Generation>, String>> = OnceLock::new();
    RUNNING
        .get_or_init(|| {
            if !cfg!(target_os = "linux") {
                return Ok(None);
            }
            let executable = std::fs::read_link("/proc/self/exe").map_err(|e| e.to_string())?;
            let image = File::open("/proc/self/exe").map_err(|e| e.to_string())?;
            capture(&executable, image)
        })
        .as_ref()
        .map(Option::as_ref)
        .map_err(Clone::clone)
}

fn capture(executable: &Path, mut image: File) -> Result<Option<Generation>, String> {
    let Some(root) = executable.parent().and_then(Path::parent) else {
        return Ok(None);
    };
    let receipt_path = root.join("nix-layout.json");
    let receipt = match File::open(&receipt_path) {
        Ok(file) => file,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(format!("Nix layout unavailable: {e}")),
    };
    let receipt: LayoutReceipt = read_json(receipt, 16384, "layout")?;
    if receipt.format_version != 1
        || !receipt.output.is_absolute()
        || !receipt.nix_store.is_absolute()
        || receipt.output.join(PRIVATE_ROOT) != root
        || root.join("bin/painted-wolf-code") != executable
        || std::fs::canonicalize(root).ok().as_deref() != Some(root)
    {
        return Err("Nix layout does not describe the running desktop".into());
    }
    let file = File::open(root.join("manifest.json"))
        .map_err(|e| format!("Nix manifest unavailable: {e}"))?;
    let manifest: Manifest = read_json(file, MANIFEST_LIMIT, "manifest")?;
    manifest
        .validate_for_root(root)
        .map_err(|_| "Nix manifest does not describe the running generation".to_string())?;
    verify(&manifest, executable, &mut image)?;
    Ok(Some(Generation {
        root: root.to_path_buf(),
        output: receipt.output,
        version: manifest.version.clone(),
        manifest,
        _running: image,
    }))
}

fn read_json<T: serde::de::DeserializeOwned>(
    file: File,
    max: u64,
    label: &str,
) -> Result<T, String> {
    if !file.metadata().map_err(|e| e.to_string())?.is_file() {
        return Err(format!("Nix {label} must be a regular file"));
    }
    let mut raw = Vec::new();
    file.take(max + 1)
        .read_to_end(&mut raw)
        .map_err(|e| e.to_string())?;
    if raw.len() as u64 > max {
        return Err(format!("Nix {label} too large"));
    }
    serde_json::from_slice(&raw).map_err(|e| format!("Nix {label} invalid: {e}"))
}

fn verify(manifest: &Manifest, path: &Path, image: &mut File) -> Result<(), String> {
    let expected = manifest.executable(path).map_err(|err| match err {
        VerifyError::Invalid => "Nix helper digest invalid".to_string(),
        _ => "Nix helper missing or duplicated in manifest".to_string(),
    })?;
    image.seek(SeekFrom::Start(0)).map_err(|e| e.to_string())?;
    expected
        .verify_reader(&mut *image)
        .map_err(|err| match err {
            VerifyError::Invalid => "Nix helper digest invalid".to_string(),
            VerifyError::Mismatch => "Nix helper identity changed".to_string(),
            VerifyError::Io => "Nix helper unreadable".to_string(),
        })?;
    image.seek(SeekFrom::Start(0)).map_err(|e| e.to_string())?;
    Ok(())
}

impl Generation {
    pub(crate) fn engine_root(&self) -> Result<PathBuf, String> {
        let root = self.root.join("engine-root");
        if !root.join("schemas/oar/oar.schema.json").is_file()
            || std::fs::canonicalize(&root).ok().as_ref() != Some(&root)
        {
            return Err("Nix engine resources unavailable in running generation".into());
        }
        Ok(root)
    }

    pub(crate) fn helper(&self, role: &str) -> Result<HelperImage, String> {
        if !ROLES.contains(&role) {
            return Err("Unknown Nix executable role".into());
        }
        let path = self.root.join("bin").join(role);
        if std::fs::canonicalize(&path).ok().as_ref() != Some(&path) {
            return Err("Nix helper is not a private generation executable".into());
        }
        let mut file = File::open(&path).map_err(|e| format!("Nix helper unavailable: {e}"))?;
        let mut magic = [0u8; 4];
        file.read_exact(&mut magic).map_err(|e| e.to_string())?;
        if magic != *b"\x7fELF" {
            return Err("Nix helper must be an unwrapped ELF executable".into());
        }
        verify(&self.manifest, &path, &mut file)?;
        #[cfg(target_os = "linux")]
        let path = {
            use std::os::fd::AsRawFd;
            PathBuf::from(format!("/proc/self/fd/{}", file.as_raw_fd()))
        };
        Ok(HelperImage {
            path,
            _descriptor: Some(file),
        })
    }
}

/// Keep this value alive through spawn/output so the verified image stays open.
pub(crate) struct HelperImage {
    pub(crate) path: PathBuf,
    _descriptor: Option<File>,
}

pub(crate) fn capture_helper(path: &Path) -> Result<HelperImage, String> {
    if let Some(generation) = running()? {
        let role = path
            .file_name()
            .and_then(|n| n.to_str())
            .ok_or("Invalid Nix helper role")?;
        if generation.root.join("bin").join(role) != path {
            return Err("Helper is outside the running Nix generation".into());
        }
        return generation.helper(role);
    }
    Ok(HelperImage {
        path: path.to_path_buf(),
        _descriptor: None,
    })
}
