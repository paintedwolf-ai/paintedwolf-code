//! Bounded extraction of a release archive into a private directory.
//!
//! The archive must hold exactly one `*.app` root. Entries are regular files, directories,
//! and relative symlinks that stay inside the bundle; links are created after every file so
//! no write can follow a link, and the finished tree is checked again by canonical path.
use super::{verification, Failure, UpdateError};
use std::{
    fs,
    io::{Read, Seek, SeekFrom},
    os::unix::fs::PermissionsExt,
    path::{Path, PathBuf},
};

pub(super) fn relative(path: &Path) -> Result<PathBuf, UpdateError> {
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
pub(super) fn safe_link(path: &Path, link: &Path) -> bool {
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
/// Whether a Mach-O file, thin or universal, contains a slice for `cpu`.
pub(super) fn contains_architecture(reader: &mut impl Read, cpu: u32) -> std::io::Result<bool> {
    let mut header = [0u8; 8];
    reader.read_exact(&mut header)?;
    let little = matches!(
        &header[..4],
        [0xcf, 0xfa, 0xed, 0xfe]
            | [0xce, 0xfa, 0xed, 0xfe]
            | [0xbe, 0xba, 0xfe, 0xca]
            | [0xbf, 0xba, 0xfe, 0xca]
    );
    let number = |bytes: &[u8]| {
        let bytes: [u8; 4] = bytes.try_into().expect("four-byte field");
        if little {
            u32::from_le_bytes(bytes)
        } else {
            u32::from_be_bytes(bytes)
        }
    };
    let magic = number(&header[..4]);
    if matches!(magic, 0xfeedface | 0xfeedfacf) {
        return Ok(number(&header[4..]) == cpu);
    }
    if !matches!(magic, 0xcafebabe | 0xcafebabf) {
        return Ok(false);
    }
    let count = number(&header[4..]);
    if count == 0 || count > 128 {
        return Ok(false);
    }
    let mut found = false;
    for _ in 0..count {
        let mut entry = [0u8; 32];
        let size = if magic == 0xcafebabf { 32 } else { 20 };
        reader.read_exact(&mut entry[..size])?;
        found |= number(&entry[..4]) == cpu;
    }
    Ok(found)
}
pub(super) fn expanded_size(file: &mut fs::File) -> Result<u64, UpdateError> {
    file.seek(SeekFrom::Start(0))
        .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
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
pub(super) fn extract(file: &mut fs::File, temp: &Path) -> Result<(), UpdateError> {
    file.seek(SeekFrom::Start(0))
        .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
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
                entry.header().mode().unwrap_or(0o644) & 0o755,
            ))
            .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
            file.sync_all()
                .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
        }
    }
    for (path, link) in &links {
        for parent in path
            .ancestors()
            .skip(1)
            .filter(|p| !p.as_os_str().is_empty())
        {
            if fs::symlink_metadata(temp.join(parent))
                .map_err(|e| UpdateError::new(Failure::InvalidRelease, e))?
                .file_type()
                .is_symlink()
            {
                return Err(Failure::InvalidRelease.into());
            }
        }
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
        if directory != temp {
            fs::set_permissions(directory, fs::Permissions::from_mode(0o755))
                .map_err(|e| UpdateError::new(Failure::InstallFailed, e))?;
        }
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
    fn signed_executable_architecture_is_checked_independently_of_the_feed() {
        let mut thin = vec![0xcf, 0xfa, 0xed, 0xfe];
        thin.extend_from_slice(&0x0100000cu32.to_le_bytes());
        assert!(contains_architecture(&mut thin.as_slice(), 0x0100000c).unwrap());
        assert!(!contains_architecture(&mut thin.as_slice(), 0x01000007).unwrap());
        let mut fat = vec![0xca, 0xfe, 0xba, 0xbe, 0, 0, 0, 2];
        for cpu in [0x0100000cu32, 0x01000007u32] {
            fat.extend_from_slice(&cpu.to_be_bytes());
            fat.extend_from_slice(&[0; 16]);
        }
        assert!(contains_architecture(&mut fat.as_slice(), 0x01000007).unwrap());
        assert!(contains_architecture(&mut &fat[..20], 0x01000007).is_err());
        assert!(!contains_architecture(&mut b"notmachO".as_slice(), 0x01000007).unwrap());
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
        extract(&mut fs::File::open(&artifact).unwrap(), &bundle).unwrap();
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
        assert!(expanded_size(&mut fs::File::open(&artifact).unwrap()).unwrap() >= 7);
    }
    #[test]
    fn chained_links_cannot_escape_after_lexical_validation() {
        let root = crate::test_support::TempDir::new("update-link-escape");
        let artifact = root.join("update.tar.gz");
        let bundle = root.join("bundle");
        fs::create_dir(&bundle).unwrap();
        fs::write(root.join("outside"), "untouched").unwrap();
        archive(&artifact, true);
        assert!(extract(&mut fs::File::open(&artifact).unwrap(), &bundle).is_err());
        assert_eq!(
            fs::read_to_string(root.join("outside")).unwrap(),
            "untouched"
        );
    }
    #[test]
    fn group_and_world_writable_modes_are_not_preserved() {
        let root = crate::test_support::TempDir::new("update-archive-modes");
        let artifact = root.join("update.tar.gz");
        let bundle = root.join("bundle");
        fs::create_dir(&bundle).unwrap();
        let file = fs::File::create(&artifact).unwrap();
        let gzip = flate2::write::GzEncoder::new(file, flate2::Compression::default());
        let mut tar = tar::Builder::new(gzip);
        let mut header = tar::Header::new_gnu();
        header.set_size(1);
        header.set_mode(0o4777);
        header.set_cksum();
        tar.append_data(&mut header, "App.app/Contents/Resources/open", &b"x"[..])
            .unwrap();
        tar.into_inner().unwrap().finish().unwrap();
        extract(&mut fs::File::open(&artifact).unwrap(), &bundle).unwrap();
        let mode = fs::metadata(bundle.join("Contents/Resources/open"))
            .unwrap()
            .permissions()
            .mode();
        assert_eq!(mode & 0o7777, 0o755);
        assert_eq!(
            fs::metadata(bundle.join("Contents")).unwrap().permissions().mode() & 0o777,
            0o755
        );
    }
}
