//! AppImage identity and same-filesystem activation.
use super::{Failure, UpdateError};
use std::{
    ffi::CString,
    fs,
    io::Read,
    os::unix::{ffi::OsStrExt, fs::MetadataExt},
    path::{Path, PathBuf},
};

mod metadata;
pub use metadata::product_version;

pub fn bundle() -> Result<PathBuf, UpdateError> {
    let image = std::env::var_os("APPIMAGE").ok_or(Failure::UnsupportedInstallation)?;
    let image = fs::canonicalize(image)
        .map_err(|e| UpdateError::new(Failure::UnsupportedInstallation, e))?;
    let appdir = std::env::var_os("APPDIR").ok_or(Failure::UnsupportedInstallation)?;
    let appdir = fs::canonicalize(appdir)
        .map_err(|e| UpdateError::new(Failure::UnsupportedInstallation, e))?;
    let running = std::env::current_exe()
        .map_err(|e| UpdateError::new(Failure::UnsupportedInstallation, e))?;
    if !running.starts_with(&appdir) || !appdir.is_dir() {
        return Err(Failure::UnsupportedInstallation.into());
    }
    if image.extension().and_then(|s| s.to_str()) != Some("AppImage") {
        return Err(Failure::UnsupportedInstallation.into());
    }
    verify_image(&image, std::env::consts::ARCH)?;
    Ok(image)
}

pub fn verify_image(path: &Path, architecture: &str) -> Result<(), UpdateError> {
    let metadata =
        fs::symlink_metadata(path).map_err(|e| UpdateError::new(Failure::VerificationFailed, e))?;
    if !metadata.is_file() || metadata.mode() & 0o111 == 0 || metadata.mode() & 0o6000 != 0 {
        return Err(Failure::VerificationFailed.into());
    }
    let mut header = [0u8; 20];
    fs::File::open(path)
        .and_then(|mut file| file.read_exact(&mut header))
        .map_err(|e| UpdateError::new(Failure::VerificationFailed, e))?;
    let machine = match architecture {
        "x86_64" => 62,
        "aarch64" => 183,
        _ => return Err(Failure::UnsupportedInstallation.into()),
    };
    if &header[..4] != b"\x7fELF"
        || header[4] != 2
        || header[5] != 1
        || &header[8..11] != b"AI\x02"
        || u16::from_le_bytes([header[18], header[19]]) != machine
    {
        return Err(Failure::VerificationFailed.into());
    }
    Ok(())
}

pub fn writable_installation(target: &Path) -> bool {
    let Some(parent) = target.parent() else {
        return false;
    };
    let Ok(metadata) = fs::symlink_metadata(target) else {
        return false;
    };
    let Ok(parent_metadata) = fs::symlink_metadata(parent) else {
        return false;
    };
    let Ok(parent_path) = CString::new(parent.as_os_str().as_bytes()) else {
        return false;
    };
    let mut stat = std::mem::MaybeUninit::<libc::statvfs>::uninit();
    metadata.is_file()
        && metadata.uid() == super::current_uid()
        && parent_metadata.is_dir()
        && parent_metadata.mode() & 0o022 == 0
        && unsafe {
            libc::statvfs(parent_path.as_ptr(), stat.as_mut_ptr()) == 0
                && stat.assume_init().f_flag & libc::ST_RDONLY == 0
                && libc::access(parent_path.as_ptr(), libc::W_OK | libc::X_OK) == 0
        }
}

pub fn exchange(left: &Path, right: &Path) -> Result<(), UpdateError> {
    let parent = left.parent().ok_or(Failure::ActivationFailed)?;
    if right.parent() != Some(parent) {
        return Err(Failure::ActivationFailed.into());
    }
    let l = CString::new(left.as_os_str().as_bytes())
        .map_err(|e| UpdateError::new(Failure::ActivationFailed, e))?;
    let r = CString::new(right.as_os_str().as_bytes())
        .map_err(|e| UpdateError::new(Failure::ActivationFailed, e))?;
    if unsafe {
        libc::renameat2(
            libc::AT_FDCWD,
            l.as_ptr(),
            libc::AT_FDCWD,
            r.as_ptr(),
            libc::RENAME_EXCHANGE,
        )
    } != 0
    {
        return Err(UpdateError::new(
            Failure::ActivationFailed,
            std::io::Error::last_os_error(),
        ));
    }
    fs::File::open(parent)
        .and_then(|file| file.sync_all())
        .map_err(|e| UpdateError::new(Failure::ActivationFailed, e))
}

pub fn prepare(
    target: &Path,
    candidate: &super::Candidate,
    artifact: &mut fs::File,
) -> Result<super::PreparedIdentity, UpdateError> {
    use std::{
        io::{Seek, SeekFrom},
        os::unix::fs::{OpenOptionsExt, PermissionsExt},
    };
    let parent = target.parent().ok_or(Failure::UnsupportedInstallation)?;
    if !writable_installation(target) || candidate.platform != super::super::feed::target() {
        return Err(Failure::UnsupportedInstallation.into());
    }
    super::check_space(
        parent,
        artifact
            .metadata()
            .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?
            .len(),
    )?;
    let destination = super::prepared_path(target, candidate)?;
    let temporary = parent.join(format!(
        "{}preparing-{}",
        super::prepared_prefix(target),
        uuid::Uuid::new_v4()
    ));
    let result = (|| {
        let mut copy = fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&temporary)
            .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
        artifact
            .seek(SeekFrom::Start(0))
            .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
        std::io::copy(artifact, &mut copy)
            .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
        copy.set_permissions(fs::Permissions::from_mode(
            fs::metadata(target)
                .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?
                .mode()
                & 0o777,
        ))
        .and_then(|_| copy.sync_all())
        .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
        drop(copy);
        let mut prepared = fs::File::open(&temporary)
            .map_err(|e| UpdateError::new(Failure::VerificationFailed, e))?;
        super::super::verification::verify_file(
            &mut prepared,
            &candidate.artifact_signature,
            &super::super::feed::embedded_key().1,
            &candidate.version,
        )
        .map_err(|e| UpdateError::new(Failure::VerificationFailed, e))?;
        verify_image(&temporary, std::env::consts::ARCH)?;
        if product_version(&temporary, Failure::VerificationFailed)? != candidate.version {
            return Err(Failure::VerificationFailed.into());
        }
        let identity = super::PreparedIdentity {
            executable_hash: super::hash(&temporary)?,
            bundle_hash: super::bundle_hash(&temporary)?,
        };
        if destination.exists() && !super::current_user_installation(&destination) {
            return Err(Failure::VerificationFailed.into());
        }
        crate::atomic_file::replace(&temporary, &destination, true)
            .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
        Ok(identity)
    })();
    if result.is_err() {
        let _ = fs::remove_file(&temporary);
    }
    result
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::{symlink, PermissionsExt};
    #[test]
    fn exchange_keeps_both_images_and_preserves_modes() {
        let root = crate::test_support::TempDir::new("linux-update-exchange");
        let installed = root.join("installed.AppImage");
        let prepared = root.join("prepared.AppImage");
        fs::write(&installed, b"old").unwrap();
        fs::write(&prepared, b"new").unwrap();
        fs::set_permissions(&prepared, fs::Permissions::from_mode(0o700)).unwrap();
        exchange(&installed, &prepared).unwrap();
        assert_eq!(fs::read(&installed).unwrap(), b"new");
        assert_eq!(fs::read(&prepared).unwrap(), b"old");
        assert_eq!(fs::metadata(&installed).unwrap().mode() & 0o777, 0o700);
        exchange(&installed, &prepared).unwrap();
        assert_eq!(fs::read(&installed).unwrap(), b"old");
        assert_eq!(fs::read(&prepared).unwrap(), b"new");
    }
    #[test]
    fn images_require_native_type_two_elf_and_executable_permissions() {
        let root = crate::test_support::TempDir::new("linux-update-image");
        let image = root.join("app.AppImage");
        let mut header = [0u8; 20];
        header[..4].copy_from_slice(b"\x7fELF");
        header[4] = 2;
        header[5] = 1;
        header[8..11].copy_from_slice(b"AI\x02");
        header[18] = 62;
        fs::write(&image, header).unwrap();
        assert!(verify_image(&image, "x86_64").is_err());
        fs::set_permissions(&image, fs::Permissions::from_mode(0o700)).unwrap();
        verify_image(&image, "x86_64").unwrap();
        assert!(verify_image(&image, "aarch64").is_err());
        header[8] = 0;
        fs::write(&image, header).unwrap();
        assert!(verify_image(&image, "x86_64").is_err());
        let link = root.join("linked.AppImage");
        symlink(&image, &link).unwrap();
        assert!(verify_image(&link, "x86_64").is_err());
    }
}
