use super::{Failure, UpdateError};
use sha2::{Digest, Sha256};
use std::{
    fs,
    io::Read,
    path::{Path, PathBuf},
};
pub(crate) fn executable(bundle: &Path) -> PathBuf {
    bundle.join("Contents/MacOS/painted-wolf-code")
}
pub(crate) fn hash(path: &Path) -> Result<String, UpdateError> {
    let mut file =
        fs::File::open(path).map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
    let mut digest = Sha256::new();
    let mut buffer = [0u8; 65536];
    loop {
        let n = file
            .read(&mut buffer)
            .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        if n == 0 {
            break;
        }
        digest.update(&buffer[..n]);
    }
    Ok(format!("{:x}", digest.finalize()))
}

#[cfg(target_os = "macos")]
pub(crate) fn bundle_hash(root: &Path) -> Result<String, UpdateError> {
    use std::os::unix::{ffi::OsStrExt, fs::PermissionsExt};
    fn bytes(digest: &mut Sha256, bytes: &[u8]) {
        digest.update((bytes.len() as u64).to_le_bytes());
        digest.update(bytes);
    }
    fn visit(root: &Path, path: &Path, digest: &mut Sha256) -> std::io::Result<()> {
        let metadata = fs::symlink_metadata(path)?;
        bytes(
            digest,
            path.strip_prefix(root).unwrap().as_os_str().as_bytes(),
        );
        digest.update((metadata.permissions().mode() & 0o7777).to_le_bytes());
        if metadata.is_symlink() {
            digest.update([1]);
            bytes(digest, fs::read_link(path)?.as_os_str().as_bytes());
        } else if metadata.is_dir() {
            digest.update([2]);
            let mut entries = fs::read_dir(path)?
                .map(|entry| entry.map(|entry| entry.path()))
                .collect::<Result<Vec<_>, _>>()?;
            entries.sort();
            digest.update((entries.len() as u64).to_le_bytes());
            for entry in entries {
                visit(root, &entry, digest)?;
            }
        } else if metadata.is_file() {
            digest.update([3]);
            digest.update(metadata.len().to_le_bytes());
            let mut file = fs::File::open(path)?;
            let mut buffer = [0u8; 65536];
            loop {
                let count = file.read(&mut buffer)?;
                if count == 0 {
                    break;
                }
                digest.update(&buffer[..count]);
            }
        } else {
            return Err(std::io::Error::new(
                std::io::ErrorKind::InvalidData,
                "Unsupported bundle entry",
            ));
        }
        Ok(())
    }
    let mut digest = Sha256::new();
    visit(root, root, &mut digest)
        .map_err(|error| UpdateError::new(Failure::VerificationFailed, error))?;
    Ok(format!("{:x}", digest.finalize()))
}

#[cfg(all(test, target_os = "macos"))]
mod tests {
    use super::*;
    use std::os::unix::fs::{symlink, PermissionsExt};
    #[test]
    fn bundle_identity_binds_resources_links_and_modes_and_survives_rename() {
        let root = crate::test_support::TempDir::new("update-bundle-identity");
        let bundle = root.join("prepared.app");
        fs::create_dir(&bundle).unwrap();
        fs::write(bundle.join("resource"), "original").unwrap();
        symlink("resource", bundle.join("link")).unwrap();
        let original = bundle_hash(&bundle).unwrap();
        fs::write(bundle.join("resource"), "replaced").unwrap();
        assert_ne!(bundle_hash(&bundle).unwrap(), original);
        fs::write(bundle.join("resource"), "original").unwrap();
        assert_eq!(bundle_hash(&bundle).unwrap(), original);
        fs::set_permissions(bundle.join("resource"), fs::Permissions::from_mode(0o755)).unwrap();
        assert_ne!(bundle_hash(&bundle).unwrap(), original);
        let mode_changed = bundle_hash(&bundle).unwrap();
        fs::remove_file(bundle.join("link")).unwrap();
        symlink("different", bundle.join("link")).unwrap();
        assert_ne!(bundle_hash(&bundle).unwrap(), mode_changed);
        let before_move = bundle_hash(&bundle).unwrap();
        let installed = root.join("installed.app");
        fs::rename(bundle, &installed).unwrap();
        assert_eq!(bundle_hash(&installed).unwrap(), before_move);
        fs::write(installed.join("extra"), "extra").unwrap();
        assert_ne!(bundle_hash(&installed).unwrap(), before_move);
    }
}
