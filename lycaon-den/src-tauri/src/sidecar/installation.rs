//! Protected startup authenticates and pins the generation for engine admission.
//! Unprotected native packages refuse fresh child work when package replacement
//! changes the running desktop's installation.

use super::manifest::{Manifest, VerifyError, MANIFEST_LIMIT};
use std::io::Read;
use std::path::Path;

pub(super) const RESTART_GUIDANCE: &str = "The installed package changed while this desktop was running. Quit Painted Wolf Code, finish any package operation, then open it from the installed desktop launcher. Your saved credentials are preserved.";

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(super) enum RestartReason {
    ManifestUnavailable,
    DesktopIdentityChanged,
    RunningImageUnavailable,
}

impl RestartReason {
    pub(super) fn guidance(self) -> String {
        format!(
            "{RESTART_GUIDANCE} ({})",
            match self {
                Self::ManifestUnavailable => "manifest_unavailable",
                Self::DesktopIdentityChanged => "desktop_identity_changed",
                Self::RunningImageUnavailable => "running_image_unavailable",
            }
        )
    }
}

/// Use the selected protected generation, or verify a mutable native package
/// against the running desktop before admitting child work.
pub(super) fn ensure_current() -> Result<(), RestartReason> {
    selected_generation_current(crate::protected_launch::generation(), || {
        if cfg!(target_os = "linux") && crate::linux_install::installed().is_some() {
            let root = Path::new(crate::linux_install::INSTALL_ROOT);
            return verify_root(root, Path::new("/proc/self/exe"));
        }
        Ok(())
    })
}

fn selected_generation_current(
    generation: Result<
        Option<crate::protected_launch::Generation>,
        crate::protected_launch::GenerationError,
    >,
    mutable_package: impl FnOnce() -> Result<(), RestartReason>,
) -> Result<(), RestartReason> {
    match generation.map_err(|_| RestartReason::RunningImageUnavailable)? {
        Some(_) => Ok(()),
        None => mutable_package(),
    }
}

fn verify_root(root: &Path, running_exe: &Path) -> Result<(), RestartReason> {
    let manifest = std::fs::File::open(root.join("manifest.json"))
        .map_err(|_| RestartReason::ManifestUnavailable)?;
    let running =
        std::fs::File::open(running_exe).map_err(|_| RestartReason::RunningImageUnavailable)?;
    verify_current(root, manifest, running)
}

fn verify_current(
    root: &Path,
    manifest: impl Read,
    running: impl Read,
) -> Result<(), RestartReason> {
    let manifest = Manifest::from_reader(manifest, MANIFEST_LIMIT, root)
        .map_err(|_| RestartReason::ManifestUnavailable)?;
    if manifest.architecture != std::env::consts::ARCH {
        return Err(RestartReason::ManifestUnavailable);
    }
    let desktop = root.join("bin/painted-wolf-code");
    let expected = manifest
        .executable(&desktop)
        .ok_or(RestartReason::ManifestUnavailable)?;
    expected.verify_reader(running).map_err(|err| match err {
        VerifyError::ReadFailed => RestartReason::RunningImageUnavailable,
        VerifyError::IdentityMismatch => RestartReason::DesktopIdentityChanged,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::sidecar::manifest::fixture_json;

    #[test]
    fn selected_protected_generation_never_uses_package_ownership_for_admission() {
        let generation = crate::protected_launch::Generation {
            logical_root: "/opt/selected-generation".into(),
        };
        assert_eq!(
            selected_generation_current(Ok(Some(generation)), || panic!("package query ran")),
            Ok(())
        );
        assert_eq!(
            selected_generation_current(
                Err(crate::protected_launch::GenerationError::FrameUnavailable),
                || panic!("package fallback ran")
            ),
            Err(RestartReason::RunningImageUnavailable)
        );
    }

    #[test]
    fn unprotected_admission_preserves_mutable_package_refusals() {
        assert_eq!(selected_generation_current(Ok(None), || Ok(())), Ok(()));
        assert_eq!(
            selected_generation_current(Ok(None), || Err(RestartReason::DesktopIdentityChanged)),
            Err(RestartReason::DesktopIdentityChanged)
        );
    }

    fn manifest_for(root: &Path, image: &[u8]) -> Vec<u8> {
        let desktop = root.join("bin/painted-wolf-code");
        serde_json::to_vec(&fixture_json(
            root,
            std::env::consts::ARCH,
            &[(&desktop, image)],
            &[],
        ))
        .unwrap()
    }

    fn manifest(image: &[u8]) -> Vec<u8> {
        manifest_for(Path::new("/opt/painted-wolf-code"), image)
    }

    fn stage_generation(root: &Path, image: &[u8]) -> std::path::PathBuf {
        std::fs::create_dir_all(root.join("bin")).unwrap();
        let desktop = root.join("bin/painted-wolf-code");
        std::fs::write(&desktop, image).unwrap();
        std::fs::write(root.join("manifest.json"), manifest_for(root, image)).unwrap();
        desktop
    }

    #[test]
    fn fresh_install_with_format_1_or_2_manifest_starts_without_restart() {
        let dir = crate::test_support::TempDir::new("fresh-install-v2");
        let desktop = stage_generation(dir.path(), b"fresh desktop image");
        assert_eq!(verify_root(dir.path(), &desktop), Ok(()));

        let root = Path::new("/opt/painted-wolf-code");
        let desktop_path = root.join("bin/painted-wolf-code");
        let mut v1 = fixture_json(
            root,
            std::env::consts::ARCH,
            &[(&desktop_path, b"desktop v1")],
            &[],
        );
        v1["format_version"] = serde_json::json!(1);
        v1.as_object_mut().unwrap().remove("payload");
        let raw = serde_json::to_vec(&v1).unwrap();
        assert_eq!(
            verify_current(root, raw.as_slice(), b"desktop v1".as_slice()),
            Ok(())
        );
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
    fn unknown_versions_and_mismatched_or_unsealed_payloads_are_refused() {
        let root = Path::new("/opt/painted-wolf-code");
        let desktop = root.join("bin/painted-wolf-code");
        let base = fixture_json(root, std::env::consts::ARCH, &[(&desktop, b"desktop")], &[]);
        for version in [0u32, 1, 3, 99] {
            let mut bad = base.clone();
            bad["format_version"] = serde_json::json!(version);
            let raw = serde_json::to_vec(&bad).unwrap();
            assert_eq!(
                verify_current(root, raw.as_slice(), b"desktop".as_slice()),
                Err(RestartReason::ManifestUnavailable)
            );
            if version != 1 {
                bad.as_object_mut().unwrap().remove("payload");
                let raw_no_payload = serde_json::to_vec(&bad).unwrap();
                assert_eq!(
                    verify_current(root, raw_no_payload.as_slice(), b"desktop".as_slice()),
                    Err(RestartReason::ManifestUnavailable)
                );
            }
        }
        for mutate in [
            |m: &mut serde_json::Value| {
                m.as_object_mut().unwrap().remove("payload");
            },
            |m: &mut serde_json::Value| {
                m["payload"] = serde_json::Value::Null;
            },
            |m: &mut serde_json::Value| {
                m["payload"] = serde_json::json!([]);
            },
            |m: &mut serde_json::Value| {
                m["payload"][1]["sha256"] = serde_json::json!("0".repeat(64));
            },
            |m: &mut serde_json::Value| {
                m["unknown_field"] = serde_json::json!(true);
            },
        ] {
            let mut bad = base.clone();
            mutate(&mut bad);
            let raw = serde_json::to_vec(&bad).unwrap();
            assert_eq!(
                verify_current(root, raw.as_slice(), b"desktop".as_slice()),
                Err(RestartReason::ManifestUnavailable)
            );
        }
    }

    #[cfg(unix)]
    #[test]
    fn comparison_reads_the_open_image_after_its_path_is_replaced() {
        let directory = crate::test_support::TempDir::new("package-replacement");
        let desktop = stage_generation(directory.path(), b"desktop A");
        let running = std::fs::File::open(&desktop).unwrap();
        std::fs::remove_file(&desktop).unwrap();
        stage_generation(directory.path(), b"desktop B");
        let current = std::fs::File::open(directory.join("manifest.json")).unwrap();
        assert_eq!(
            verify_current(directory.path(), current, running),
            Err(RestartReason::DesktopIdentityChanged)
        );
    }

    #[cfg(unix)]
    #[test]
    fn protected_launch_view_keeps_running_across_host_package_upgrade() {
        let directory = crate::test_support::TempDir::new("protected-launch-upgrade");
        let protected_view = directory.join("protected-opt");
        let host_mutable_root = directory.join("host-opt");
        let launched_desktop = stage_generation(&protected_view, b"generation A desktop");
        stage_generation(&host_mutable_root, b"generation A desktop");

        // Host upgrades `/opt/painted-wolf-code` in the host mount namespace to generation B,
        // while the protected desktop's private mount namespace keeps its `/opt/painted-wolf-code`
        // view bound to the immutable generation A directory it started from.
        stage_generation(&host_mutable_root, b"generation B desktop");

        assert_eq!(verify_root(&protected_view, &launched_desktop), Ok(()));
        assert_eq!(
            verify_current(
                &host_mutable_root,
                std::fs::File::open(host_mutable_root.join("manifest.json")).unwrap(),
                std::fs::File::open(&launched_desktop).unwrap(),
            ),
            Err(RestartReason::DesktopIdentityChanged)
        );
    }
}
