use super::*;
use sha2::{Digest, Sha256};
use std::{
    ffi::CString,
    os::{
        fd::AsRawFd,
        unix::{ffi::OsStrExt, fs::PermissionsExt},
    },
};
pub struct InstallationLease(pub fs::File);

pub fn writable_installation(target: &Path) -> bool {
    let Some(parent) = target.parent() else {
        return false;
    };
    let Ok(parent) = CString::new(parent.as_os_str().as_bytes()) else {
        return false;
    };
    // Translocated and disk-image applications reside on a read-only mount.
    let mut stat = std::mem::MaybeUninit::<libc::statvfs>::uninit();
    unsafe {
        libc::statvfs(parent.as_ptr(), stat.as_mut_ptr()) == 0
            && stat.assume_init().f_flag & libc::ST_RDONLY == 0
            && libc::access(parent.as_ptr(), libc::W_OK | libc::X_OK) == 0
    }
}

pub fn acquire_gate(target: &Path) -> Result<InstallationLease, UpdateError> {
    acquire_named_lease(target, "gate", true, false)
}
pub fn acquire_lease(
    target: &Path,
    exclusive: bool,
    nonblocking: bool,
) -> Result<InstallationLease, UpdateError> {
    acquire_named_lease(target, "lifetime", exclusive, nonblocking)
}
fn acquire_named_lease(
    target: &Path,
    kind: &str,
    exclusive: bool,
    nonblocking: bool,
) -> Result<InstallationLease, UpdateError> {
    let root = super::super::persistence::update_dir()?;
    crate::config_dir::ensure_private_dir(&root)
        .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
    let id = format!("{:x}", Sha256::digest(target.as_os_str().as_bytes()));
    acquire_lock(
        &root.join(format!("installation-{id}-{kind}.lock")),
        exclusive,
        nonblocking,
    )
}
fn acquire_lock(
    path: &Path,
    exclusive: bool,
    nonblocking: bool,
) -> Result<InstallationLease, UpdateError> {
    let file = fs::OpenOptions::new()
        .create(true)
        .truncate(false)
        .read(true)
        .write(true)
        .open(path)
        .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
    let mode = if exclusive {
        libc::LOCK_EX
    } else {
        libc::LOCK_SH
    } | if nonblocking { libc::LOCK_NB } else { 0 };
    if unsafe { libc::flock(file.as_raw_fd(), mode) } != 0 {
        let error = std::io::Error::last_os_error();
        let code = if error.kind() == std::io::ErrorKind::WouldBlock {
            Failure::InvalidTransition
        } else {
            Failure::StateUnavailable
        };
        return Err(UpdateError::new(code, error));
    }
    Ok(InstallationLease(file))
}
pub fn exchange(left: &Path, right: &Path) -> Result<(), UpdateError> {
    let l = CString::new(left.as_os_str().as_bytes())
        .map_err(|e| UpdateError::new(Failure::ActivationFailed, e))?;
    let r = CString::new(right.as_os_str().as_bytes())
        .map_err(|e| UpdateError::new(Failure::ActivationFailed, e))?;
    if unsafe { libc::renamex_np(l.as_ptr(), r.as_ptr(), libc::RENAME_SWAP) } != 0 {
        return Err(UpdateError::new(
            Failure::ActivationFailed,
            std::io::Error::last_os_error(),
        ));
    }
    fs::File::open(left.parent().ok_or(Failure::ActivationFailed)?)
        .and_then(|f| f.sync_all())
        .map_err(|e| UpdateError::new(Failure::ActivationFailed, e))
}
fn relative(path: &Path) -> Result<PathBuf, UpdateError> {
    let mut components = path.components();
    let Some(std::path::Component::Normal(root)) = components.next() else {
        return Err(Failure::InvalidRelease.into());
    };
    if !root.to_string_lossy().ends_with(".app") {
        return Err(Failure::InvalidRelease.into());
    }
    let mut out = PathBuf::new();
    for component in components {
        if !matches!(component, std::path::Component::Normal(_)) {
            return Err(Failure::InvalidRelease.into());
        }
        out.push(component);
    }
    Ok(out)
}
fn safe_link(path: &Path, link: &Path) -> bool {
    if link.is_absolute() {
        return false;
    }
    let mut depth = path.parent().map_or(0, |p| p.components().count());
    for component in link.components() {
        match component {
            std::path::Component::ParentDir => {
                if depth == 0 {
                    return false;
                }
                depth -= 1;
            }
            std::path::Component::Normal(_) => depth += 1,
            std::path::Component::CurDir => {}
            _ => return false,
        }
    }
    true
}
pub fn prepare(
    target: &Path,
    candidate: &Candidate,
    artifact: &Path,
) -> Result<String, UpdateError> {
    verify_bundle(target)?;
    let parent = target.parent().ok_or(Failure::UnsupportedInstallation)?;
    check_space(parent, expanded_size(artifact)?)?;
    let preparing_prefix = format!("{}preparing-", prepared_prefix(target));
    for entry in fs::read_dir(parent).map_err(|e| UpdateError::new(Failure::StateUnavailable, e))? {
        let entry = entry.map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        let name = entry.file_name().to_string_lossy().into_owned();
        if name
            .strip_prefix(&preparing_prefix)
            .is_some_and(|id| uuid::Uuid::parse_str(id).is_ok())
            && entry.file_type().is_ok_and(|kind| kind.is_dir())
        {
            fs::remove_dir_all(entry.path())
                .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        }
    }
    let temp = parent.join(format!("{preparing_prefix}{}", uuid::Uuid::new_v4()));
    fs::create_dir(&temp).map_err(|e| UpdateError::new(Failure::UnsupportedInstallation, e))?;
    fs::set_permissions(&temp, fs::Permissions::from_mode(0o700))
        .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
    let result = extract(artifact, &temp)
        .and_then(|_| verify_bundle(&temp))
        .and_then(|_| {
            let version = fs::read_to_string(temp.join("Contents/Resources/release-version"))
                .map_err(|e| UpdateError::new(Failure::VerificationFailed, e))?;
            if version.trim() != candidate.version {
                return Err(Failure::VerificationFailed.into());
            }
            Ok(())
        });
    if let Err(error) = result {
        let _ = fs::remove_dir_all(&temp);
        return Err(error);
    }
    fs::set_permissions(
        &temp,
        fs::metadata(target)
            .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?
            .permissions(),
    )
    .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
    let executable_hash =
        super::super::transaction::hash(&super::super::transaction::executable(&temp))?;
    let destination = prepared_path(target, candidate)?;
    if destination.exists() {
        fs::remove_dir_all(&destination)
            .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
    }
    crate::atomic_file::replace(&temp, &destination, true)
        .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
    Ok(executable_hash)
}
fn expanded_size(artifact: &Path) -> Result<u64, UpdateError> {
    let file = fs::File::open(artifact).map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
    let mut archive = tar::Archive::new(flate2::read::GzDecoder::new(file));
    let mut total = 0u64;
    for entry in archive
        .entries()
        .map_err(|e| UpdateError::new(Failure::InvalidRelease, e))?
    {
        let entry = entry.map_err(|e| UpdateError::new(Failure::InvalidRelease, e))?;
        total = total
            .checked_add(entry.size())
            .ok_or(Failure::InvalidRelease)?;
        if total > verification::limits().max_expanded_bytes {
            return Err(Failure::InvalidRelease.into());
        }
    }
    Ok(total)
}
fn extract(artifact: &Path, temp: &Path) -> Result<(), UpdateError> {
    let file = fs::File::open(artifact).map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
    let mut archive = tar::Archive::new(flate2::read::GzDecoder::new(file));
    let mut total = 0u64;
    let mut links = Vec::new();
    let mut seen = std::collections::BTreeSet::new();
    let mut archive_root = None;
    let mut directories = std::collections::BTreeSet::from([temp.to_path_buf()]);
    for entry in archive
        .entries()
        .map_err(|e| UpdateError::new(Failure::InvalidRelease, e))?
    {
        let mut entry = entry.map_err(|e| UpdateError::new(Failure::InvalidRelease, e))?;
        let raw = entry
            .path()
            .map_err(|e| UpdateError::new(Failure::InvalidRelease, e))?
            .into_owned();
        let root = raw
            .components()
            .next()
            .ok_or(Failure::InvalidRelease)?
            .as_os_str()
            .to_owned();
        if archive_root.as_ref().is_some_and(|old| old != &root) {
            return Err(Failure::InvalidRelease.into());
        }
        archive_root = Some(root);
        let path = relative(&raw)?;
        if path.as_os_str().is_empty() {
            continue;
        }
        if !seen.insert(path.clone()) || seen.len() > verification::limits().max_archive_entries {
            return Err(Failure::InvalidRelease.into());
        }
        total = total
            .checked_add(entry.size())
            .ok_or(Failure::InvalidRelease)?;
        if total > verification::limits().max_expanded_bytes {
            return Err(Failure::InvalidRelease.into());
        }
        let kind = entry.header().entry_type();
        if kind.is_symlink() {
            let link = entry
                .link_name()
                .map_err(|e| UpdateError::new(Failure::InvalidRelease, e))?
                .ok_or(Failure::InvalidRelease)?
                .into_owned();
            if !safe_link(&path, &link) {
                return Err(Failure::InvalidRelease.into());
            }
            links.push((path, link));
            continue;
        }
        if !kind.is_dir() && !kind.is_file() {
            return Err(Failure::InvalidRelease.into());
        }
        let dest = temp.join(&path);
        if let Some(parent) = dest.parent() {
            fs::create_dir_all(parent).map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
            for directory in parent.ancestors().take_while(|p| p.starts_with(temp)) {
                directories.insert(directory.to_path_buf());
            }
        }
        if kind.is_dir() {
            fs::create_dir_all(&dest).map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
            directories.insert(dest.clone());
        } else {
            let mut file = fs::OpenOptions::new()
                .write(true)
                .create_new(true)
                .open(&dest)
                .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
            std::io::copy(&mut entry, &mut file)
                .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
            file.set_permissions(fs::Permissions::from_mode(
                entry.header().mode().unwrap_or(0o644) & 0o777,
            ))
            .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
            file.sync_all()
                .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
        }
    }
    for (path, link) in &links {
        std::os::unix::fs::symlink(link, temp.join(path))
            .map_err(|e| UpdateError::new(Failure::InvalidRelease, e))?;
    }
    let canonical =
        fs::canonicalize(temp).map_err(|e| UpdateError::new(Failure::InvalidRelease, e))?;
    for (path, _) in links {
        let resolved = fs::canonicalize(temp.join(path))
            .map_err(|e| UpdateError::new(Failure::InvalidRelease, e))?;
        if !resolved.starts_with(&canonical) {
            return Err(Failure::InvalidRelease.into());
        }
    }
    for directory in directories.iter().rev() {
        fs::File::open(directory)
            .and_then(|f| f.sync_all())
            .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
    }
    Ok(())
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn concurrent_instances_share_lifetime_while_gate_excludes_activation() {
        let root = crate::test_support::TempDir::new("update-locks");
        let lifetime = root.join("lifetime");
        let gate = root.join("gate");
        let first = acquire_lock(&lifetime, false, true).unwrap();
        assert!(acquire_lock(&lifetime, true, true).is_err());
        let second = acquire_lock(&lifetime, false, true).unwrap();
        drop(first);
        assert!(acquire_lock(&lifetime, true, true).is_err());
        drop(second);
        let admission = acquire_lock(&gate, true, true).unwrap();
        let exclusive = acquire_lock(&lifetime, true, true).unwrap();
        assert_eq!(
            unsafe { libc::flock(exclusive.0.as_raw_fd(), libc::LOCK_UN) },
            0
        );
        assert!(acquire_lock(&gate, true, true).is_err());
        assert_eq!(
            unsafe { libc::flock(exclusive.0.as_raw_fd(), libc::LOCK_SH) },
            0
        );
        drop(admission);
        let _helper = acquire_lock(&gate, true, true).unwrap();
        assert!(acquire_lock(&lifetime, true, true).is_err());
    }
    #[test]
    fn archive_paths_cannot_escape_the_bundle() {
        for bad in [
            "/tmp/A.app/a",
            "A.app/../escape",
            "other/a",
            "A.app/a/../../b",
        ] {
            assert!(relative(Path::new(bad)).is_err(), "{bad}");
        }
        assert_eq!(
            relative(Path::new("A.app/Contents/MacOS/app")).unwrap(),
            Path::new("Contents/MacOS/app")
        );
        assert!(!safe_link(
            Path::new("Contents/link"),
            Path::new("../../outside")
        ));
        assert!(safe_link(
            Path::new("Contents/Frameworks/F/Versions/Current"),
            Path::new("A")
        ));
    }
    fn archive(path: &Path, escaping: bool) {
        let file = fs::File::create(path).unwrap();
        let gzip = flate2::write::GzEncoder::new(file, flate2::Compression::default());
        let mut tar = tar::Builder::new(gzip);
        let mut header = tar::Header::new_gnu();
        header.set_size(7);
        header.set_mode(0o755);
        header.set_cksum();
        tar.append_data(&mut header, "App.app/Contents/MacOS/app", &b"program"[..])
            .unwrap();
        let mut link = tar::Header::new_gnu();
        link.set_entry_type(tar::EntryType::Symlink);
        link.set_size(0);
        link.set_mode(0o777);
        tar.append_link(&mut link, "App.app/Contents/root", "..")
            .unwrap();
        if escaping {
            tar.append_link(&mut link, "App.app/Contents/escape", "root/../outside")
                .unwrap();
        }
        tar.into_inner().unwrap().finish().unwrap();
    }
    #[test]
    fn extraction_preserves_executables_and_contained_links() {
        let root = crate::test_support::TempDir::new("update-valid-archive");
        let artifact = root.join("update.tar.gz");
        let bundle = root.join("bundle");
        fs::create_dir(&bundle).unwrap();
        archive(&artifact, false);
        extract(&artifact, &bundle).unwrap();
        assert_eq!(
            fs::read(bundle.join("Contents/MacOS/app")).unwrap(),
            b"program"
        );
        assert_eq!(
            fs::metadata(bundle.join("Contents/MacOS/app"))
                .unwrap()
                .permissions()
                .mode()
                & 0o777,
            0o755
        );
        assert_eq!(
            fs::canonicalize(bundle.join("Contents/root")).unwrap(),
            fs::canonicalize(bundle).unwrap()
        );
    }
    #[test]
    fn chained_links_cannot_escape_after_lexical_validation() {
        let root = crate::test_support::TempDir::new("update-link-escape");
        let artifact = root.join("update.tar.gz");
        let bundle = root.join("bundle");
        fs::create_dir(&bundle).unwrap();
        fs::write(root.join("outside"), "untouched").unwrap();
        archive(&artifact, true);
        assert!(extract(&artifact, &bundle).is_err());
        assert_eq!(
            fs::read_to_string(root.join("outside")).unwrap(),
            "untouched"
        );
    }
    #[test]
    fn activation_exchange_keeps_both_complete_trees() {
        let dir = crate::test_support::TempDir::new("update-exchange");
        let a = dir.join("old");
        let b = dir.join("new");
        fs::create_dir(&a).unwrap();
        fs::create_dir(&b).unwrap();
        fs::write(a.join("identity"), "old").unwrap();
        fs::write(b.join("identity"), "new").unwrap();
        exchange(&a, &b).unwrap();
        assert_eq!(fs::read_to_string(a.join("identity")).unwrap(), "new");
        assert_eq!(fs::read_to_string(b.join("identity")).unwrap(), "old");
    }
}
