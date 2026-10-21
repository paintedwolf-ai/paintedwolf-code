//! Refuse fresh child work after the native package replaces this desktop.
//! The identity service remains the authority that verifies the launch chain.

use serde::Deserialize;
use sha2::{Digest, Sha256};
use std::io::Read;
use std::path::Path;

use super::SidecarStartError;
use crate::linux_install::{Ownership, QueryReason};

pub(super) const RESTART_GUIDANCE: &str = "The installed package changed while this desktop was running. Quit Painted Wolf Code, finish any package operation, then open it from the installed desktop launcher. Your saved credentials are preserved.";
pub(super) const UNVERIFIED_GUIDANCE: &str = "Painted Wolf Code could not confirm which package owns this installation, so it did not start new engine work. Wait for any package operation to finish, then try again. Your saved credentials are preserved.";

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(super) enum RestartReason {
    ManifestUnavailable,
    DesktopIdentityChanged,
    RunningImageUnavailable,
    /// Not a restart requirement: the replacement check could not run, and a retry asks again.
    OwnershipUnknown(QueryReason),
}

impl RestartReason {
    pub(super) fn guidance(self) -> String {
        let (copy, code) = match self {
            Self::ManifestUnavailable => (RESTART_GUIDANCE, "manifest_unavailable"),
            Self::DesktopIdentityChanged => (RESTART_GUIDANCE, "desktop_identity_changed"),
            Self::RunningImageUnavailable => (RESTART_GUIDANCE, "running_image_unavailable"),
            Self::OwnershipUnknown(_) => (UNVERIFIED_GUIDANCE, "package_ownership_unknown"),
        };
        format!("{copy} ({code})")
    }

    pub(super) fn restart_required(self) -> bool {
        !matches!(self, Self::OwnershipUnknown(_))
    }

    pub(super) fn start_error(self) -> SidecarStartError {
        if self.restart_required() {
            SidecarStartError::DesktopRestartRequired(self.guidance())
        } else {
            SidecarStartError::InstallationUnverified(self.guidance())
        }
    }
}

#[derive(Deserialize)]
struct Manifest {
    format_version: u32,
    install_root: std::path::PathBuf,
    executables: Vec<Executable>,
}

#[derive(Deserialize)]
struct Executable {
    path: std::path::PathBuf,
    sha256: String,
    size: u64,
}

/// This is an admission refusal for mutable native packages, never launcher trust.
/// Nix closures need their resolved generation facts rather than this native root.
pub(super) fn ensure_current() -> Result<(), RestartReason> {
    if !cfg!(target_os = "linux") {
        return Ok(());
    }
    admit(crate::linux_install::installed(), || {
        let root = Path::new(crate::linux_install::INSTALL_ROOT);
        let manifest = std::fs::File::open(root.join("manifest.json"))
            .map_err(|_| RestartReason::ManifestUnavailable)?;
        let running = std::fs::File::open("/proc/self/exe")
            .map_err(|_| RestartReason::RunningImageUnavailable)?;
        verify_current(root, manifest, running)
    })
}

/// Unknown ownership fails closed: the replacement check cannot be skipped by a slow database.
fn admit(
    ownership: Ownership,
    verify_package: impl FnOnce() -> Result<(), RestartReason>,
) -> Result<(), RestartReason> {
    match ownership {
        Ownership::Package(_) => verify_package(),
        Ownership::Unpackaged => Ok(()),
        Ownership::Unknown(reason) => Err(RestartReason::OwnershipUnknown(reason)),
    }
}

fn verify_current(
    root: &Path,
    manifest: impl Read,
    mut running: impl Read,
) -> Result<(), RestartReason> {
    let manifest: Manifest = serde_json::from_reader(manifest.take((4 << 20) + 1))
        .map_err(|_| RestartReason::ManifestUnavailable)?;
    if manifest.format_version != 1 || manifest.install_root != root {
        return Err(RestartReason::ManifestUnavailable);
    }
    let desktop = root.join("bin/painted-wolf-code");
    let entries: Vec<_> = manifest
        .executables
        .iter()
        .filter(|entry| entry.path == desktop)
        .collect();
    let [expected] = entries.as_slice() else {
        return Err(RestartReason::ManifestUnavailable);
    };
    let mut digest = Sha256::new();
    let mut size = 0u64;
    let mut buffer = [0u8; 65536];
    loop {
        let count = running
            .read(&mut buffer)
            .map_err(|_| RestartReason::RunningImageUnavailable)?;
        if count == 0 {
            break;
        }
        size = size.saturating_add(count as u64);
        if size > expected.size {
            return Err(RestartReason::DesktopIdentityChanged);
        }
        digest.update(&buffer[..count]);
    }
    if size != expected.size || hex::encode(digest.finalize()) != expected.sha256 {
        return Err(RestartReason::DesktopIdentityChanged);
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn manifest(image: &[u8]) -> Vec<u8> {
        serde_json::to_vec(&serde_json::json!({
            "format_version": 1,
            "install_root": "/opt/painted-wolf-code",
            "executables": [{"path": "/opt/painted-wolf-code/bin/painted-wolf-code", "sha256": hex::encode(Sha256::digest(image)), "size": image.len()}]
        })).unwrap()
    }

    #[test]
    fn old_parent_is_refused_after_package_replacement_and_new_parent_succeeds() {
        let root = Path::new("/opt/painted-wolf-code");
        let current = manifest(b"desktop B");
        assert_eq!(
            verify_current(root, current.as_slice(), b"desktop A".as_slice()),
            Err(RestartReason::DesktopIdentityChanged)
        );
        assert_eq!(
            verify_current(root, current.as_slice(), b"desktop B".as_slice()),
            Ok(())
        );
        assert_eq!(
            verify_current(root, current.as_slice(), b"tampered".as_slice()),
            Err(RestartReason::DesktopIdentityChanged)
        );
    }

    #[test]
    fn incomplete_or_wrong_installation_is_recoverable() {
        let root = Path::new("/opt/painted-wolf-code");
        for raw in [b"{".as_slice(), b"{}".as_slice()] {
            assert_eq!(
                verify_current(root, raw, b"desktop".as_slice()),
                Err(RestartReason::ManifestUnavailable)
            );
        }
        assert_eq!(
            verify_current(
                Path::new("/other"),
                manifest(b"desktop").as_slice(),
                b"desktop".as_slice()
            ),
            Err(RestartReason::ManifestUnavailable)
        );
    }

    #[test]
    fn unknown_ownership_refuses_without_skipping_to_unpackaged_admission() {
        let refused = admit(Ownership::Unknown(QueryReason::TimedOut), || panic!("no package to verify"));
        assert_eq!(refused, Err(RestartReason::OwnershipUnknown(QueryReason::TimedOut)));
        let reason = refused.unwrap_err();
        assert!(!reason.restart_required());
        assert!(matches!(reason.start_error(), SidecarStartError::InstallationUnverified(message)
            if message.starts_with(UNVERIFIED_GUIDANCE) && message.ends_with("(package_ownership_unknown)")));
        assert_eq!(admit(Ownership::Unpackaged, || panic!("no package to verify")), Ok(()));
        let changed = RestartReason::DesktopIdentityChanged;
        assert!(matches!(changed.start_error(), SidecarStartError::DesktopRestartRequired(message) if message.starts_with(RESTART_GUIDANCE)));
    }

    #[cfg(unix)]
    #[test]
    fn comparison_reads_the_open_image_after_its_path_is_replaced() {
        let directory = crate::test_support::TempDir::new("package-replacement");
        let path = directory.join("desktop");
        std::fs::write(&path, b"desktop A").unwrap();
        let running = std::fs::File::open(&path).unwrap();
        std::fs::remove_file(&path).unwrap();
        std::fs::write(&path, b"desktop B").unwrap();
        let current = manifest(b"desktop B");
        assert_eq!(
            verify_current(
                Path::new("/opt/painted-wolf-code"),
                current.as_slice(),
                running
            ),
            Err(RestartReason::DesktopIdentityChanged)
        );
    }
}
