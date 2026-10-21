//! Auxiliary children execute a retained engine from the running desktop generation.
use super::manifest::{Executable, Manifest, VerifyError};
use sha2::{Digest, Sha256};
use std::fs::File;
use std::io::Read;
use std::os::fd::AsRawFd;
use std::os::unix::fs::{FileExt, MetadataExt};
use std::path::Path;
use std::sync::OnceLock;

static ENGINE: OnceLock<Result<CapturedEngine, AdmissionError>> = OnceLock::new();
const MANIFEST_LIMIT: u64 = 1024 * 1024;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum AdmissionError {
    ManifestUnavailable,
    GenerationMismatch,
    EngineUnavailable,
    UnqualifiedInstallation,
}

impl AdmissionError {
    pub(crate) fn guidance(self) -> &'static str {
        match self {
            Self::UnqualifiedInstallation => {
                "This installation has no verified native update transport"
            }
            _ => "Restart Painted Wolf Code to use the installed update transport",
        }
    }
}

struct CapturedEngine {
    file: File,
    expected: Executable,
}

/// Capture before package replacement can change the path used by a later check.
pub(super) fn setup() {
    let result = capture_installation(
        &crate::installation_mode::InstallationMode::current(),
        Path::new("/proc/self/exe"),
    );
    let _ = ENGINE.set(result);
}

fn capture_installation(
    installation: &crate::installation_mode::InstallationMode,
    running: &Path,
) -> Result<CapturedEngine, AdmissionError> {
    match installation {
        crate::installation_mode::InstallationMode::Package { root } => capture(root, running, true),
        crate::installation_mode::InstallationMode::Nix { root, .. } => {
            // Generation layout permits this user's helper execution. The vault
            // service separately enforces administrator-selected custody trust.
            capture(root, running, false)
        }
        _ => Err(AdmissionError::UnqualifiedInstallation),
    }
}

pub(crate) fn command() -> Result<tokio::process::Command, AdmissionError> {
    let captured = ENGINE
        .get()
        .ok_or(AdmissionError::EngineUnavailable)?
        .as_ref()
        .map_err(|error| *error)?;
    captured.command()
}

impl CapturedEngine {
    fn command(&self) -> Result<tokio::process::Command, AdmissionError> {
        verify(&self.file, &self.expected)?;
        // Keep the descriptor in ENGINE for the whole application lifetime. The
        // kernel resolves this descriptor before closing it during ELF exec.
        Ok(tokio::process::Command::new(format!(
            "/proc/self/fd/{}",
            self.file.as_raw_fd()
        )))
    }
}

fn capture(
    root: &Path,
    running: &Path,
    package_owned: bool,
) -> Result<CapturedEngine, AdmissionError> {
    let file =
        File::open(root.join("manifest.json")).map_err(|_| AdmissionError::ManifestUnavailable)?;
    let metadata = file
        .metadata()
        .map_err(|_| AdmissionError::ManifestUnavailable)?;
    if !metadata.is_file() || metadata.len() > MANIFEST_LIMIT || package_owned && !owned(&metadata)
    {
        return Err(AdmissionError::ManifestUnavailable);
    }
    let mut raw = Vec::new();
    file.take(MANIFEST_LIMIT + 1)
        .read_to_end(&mut raw)
        .map_err(|_| AdmissionError::ManifestUnavailable)?;
    let manifest: Manifest =
        serde_json::from_slice(&raw).map_err(|_| AdmissionError::ManifestUnavailable)?;
    if raw.len() as u64 > MANIFEST_LIMIT || manifest.architecture != std::env::consts::ARCH {
        return Err(AdmissionError::ManifestUnavailable);
    }
    manifest.validate_for_root(root).map_err(|err| match err {
        VerifyError::Mismatch => AdmissionError::GenerationMismatch,
        VerifyError::Invalid | VerifyError::Io => AdmissionError::ManifestUnavailable,
    })?;
    let desktop = entry(&manifest, &root.join("bin/painted-wolf-code"))?;
    verify(
        &File::open(running).map_err(|_| AdmissionError::GenerationMismatch)?,
        desktop,
    )?;
    let engine_path = root.join("bin/pw");
    let expected = entry(&manifest, &engine_path)?;
    let file = File::open(engine_path).map_err(|_| AdmissionError::EngineUnavailable)?;
    let metadata = file
        .metadata()
        .map_err(|_| AdmissionError::EngineUnavailable)?;
    if metadata.mode() & 0o111 == 0 || package_owned && !owned(&metadata) {
        return Err(AdmissionError::EngineUnavailable);
    }
    verify(&file, expected)?;
    Ok(CapturedEngine {
        file,
        expected: Executable {
            path: expected.path.clone(),
            sha256: expected.sha256.clone(),
            size: expected.size,
        },
    })
}

fn owned(metadata: &std::fs::Metadata) -> bool {
    metadata.uid() == 0 && metadata.mode() & 0o022 == 0
}

fn entry<'a>(manifest: &'a Manifest, path: &Path) -> Result<&'a Executable, AdmissionError> {
    manifest
        .executable(path)
        .map_err(|_| AdmissionError::ManifestUnavailable)
}

fn verify(file: &File, expected: &Executable) -> Result<(), AdmissionError> {
    let metadata = file
        .metadata()
        .map_err(|_| AdmissionError::EngineUnavailable)?;
    if !metadata.is_file()
        || metadata.len() != expected.size
        || expected.size > 2 * 1024 * 1024 * 1024
    {
        return Err(AdmissionError::GenerationMismatch);
    }
    let mut digest = Sha256::new();
    let mut offset = 0;
    let mut buffer = [0; 64 * 1024];
    while offset < expected.size {
        let count = file
            .read_at(&mut buffer, offset)
            .map_err(|_| AdmissionError::EngineUnavailable)?;
        if count == 0 {
            return Err(AdmissionError::GenerationMismatch);
        }
        digest.update(&buffer[..count]);
        offset += count as u64;
    }
    if offset != expected.size || hex::encode(digest.finalize()) != expected.sha256 {
        return Err(AdmissionError::GenerationMismatch);
    }
    Ok(())
}

#[cfg(test)]
mod tests;
