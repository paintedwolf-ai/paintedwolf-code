//! Read the release resource through the AppImage runtime without launching the app.
use super::{Failure, UpdateError};
use std::{
    fs,
    io::Read,
    path::Path,
    process::Stdio,
    time::{Duration, Instant},
};

struct Extraction(std::path::PathBuf);
impl Drop for Extraction {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

pub fn product_version(image: &Path, failure: Failure) -> Result<String, UpdateError> {
    super::verify_image(image, std::env::consts::ARCH)?;
    let directory = super::super::super::persistence::update_dir()?
        .join(format!("metadata-{}", uuid::Uuid::new_v4()));
    crate::config_dir::ensure_private_dir(&directory).map_err(|e| UpdateError::new(failure, e))?;
    let cleanup = Extraction(directory);
    let mut child = std::process::Command::new(image)
        .args(["--appimage-extract", "*release-version"])
        .current_dir(&cleanup.0)
        .env_remove("APPIMAGE")
        .env_remove("APPDIR")
        .env_remove("APPIMAGE_EXTRACT_AND_RUN")
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|e| UpdateError::new(failure, e))?;
    let deadline = Instant::now() + Duration::from_secs(30);
    loop {
        match child.try_wait() {
            Ok(Some(status)) if status.success() => break,
            Ok(Some(_)) => {
                return Err(UpdateError::new(
                    failure,
                    "Could not read the AppImage release resource",
                ))
            }
            Ok(None) if Instant::now() < deadline => std::thread::sleep(Duration::from_millis(25)),
            result => {
                let _ = child.kill();
                let _ = child.wait();
                return Err(UpdateError::new(
                    failure,
                    result
                        .err()
                        .map(|e| e.to_string())
                        .unwrap_or_else(|| "AppImage metadata extraction timed out".into()),
                ));
            }
        }
    }
    version_from_tree(&cleanup.0.join("squashfs-root"), failure)
}

fn version_from_tree(root: &Path, failure: Failure) -> Result<String, UpdateError> {
    fn visit(
        root: &Path,
        depth: usize,
        seen: &mut usize,
        versions: &mut Vec<String>,
        failure: Failure,
    ) -> Result<(), UpdateError> {
        if depth > 8 {
            return Err(failure.into());
        }
        for entry in fs::read_dir(root).map_err(|e| UpdateError::new(failure, e))? {
            *seen += 1;
            if *seen > 256 {
                return Err(failure.into());
            }
            let entry = entry.map_err(|e| UpdateError::new(failure, e))?;
            let kind = entry
                .file_type()
                .map_err(|e| UpdateError::new(failure, e))?;
            if kind.is_dir() {
                visit(&entry.path(), depth + 1, seen, versions, failure)?;
            } else if entry.file_name() == "release-version" {
                if !kind.is_file() {
                    return Err(failure.into());
                }
                let mut version = String::new();
                fs::File::open(entry.path())
                    .and_then(|file| file.take(129).read_to_string(&mut version))
                    .map_err(|e| UpdateError::new(failure, e))?;
                if version.len() > 128 || semver::Version::parse(version.trim()).is_err() {
                    return Err(failure.into());
                }
                versions.push(version.trim().to_owned());
            }
        }
        Ok(())
    }
    let mut versions = Vec::new();
    visit(root, 0, &mut 0, &mut versions, failure)?;
    if versions.len() != 1 {
        return Err(failure.into());
    }
    Ok(versions.remove(0))
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn version_resource_is_unique_bounded_and_never_a_symlink() {
        let root = crate::test_support::TempDir::new("linux-update-version");
        let resource = root.join("usr/lib/app/release-version");
        fs::create_dir_all(resource.parent().unwrap()).unwrap();
        fs::write(&resource, "1.2.3-rc.1\n").unwrap();
        assert_eq!(
            version_from_tree(&root, Failure::VerificationFailed).unwrap(),
            "1.2.3-rc.1"
        );
        fs::write(root.join("release-version"), "1.2.3").unwrap();
        assert!(version_from_tree(&root, Failure::VerificationFailed).is_err());
        fs::remove_file(root.join("release-version")).unwrap();
        fs::write(&resource, "1".repeat(129)).unwrap();
        assert!(version_from_tree(&root, Failure::VerificationFailed).is_err());
        fs::remove_file(&resource).unwrap();
        std::os::unix::fs::symlink("/etc/passwd", &resource).unwrap();
        assert!(version_from_tree(&root, Failure::VerificationFailed).is_err());
    }
}
