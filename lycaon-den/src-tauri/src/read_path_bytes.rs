//! Native-drop path validation.

use std::fs::{self, File};
use std::io::Read;
use std::path::PathBuf;

#[cfg(unix)]
use std::path::Component;

#[cfg(unix)]
use std::ffi::CString;
#[cfg(unix)]
use std::os::fd::{AsRawFd, FromRawFd};
#[cfg(unix)]
use std::os::unix::ffi::OsStrExt;

use serde::Serialize;

use crate::reveal_in_file_manager::path_under_roots;

#[tauri::command]
pub async fn path_kind(abs_path: String, project_roots: Vec<String>) -> Result<String, String> {
    tauri::async_runtime::spawn_blocking(move || path_kind_at(&abs_path, &project_roots))
        .await
        .map_err(|e| format!("path_kind did not complete: {e}"))?
}

#[tauri::command]
pub async fn import_path_kind(abs_path: String) -> Result<String, String> {
    tauri::async_runtime::spawn_blocking(move || import_path_kind_at(&abs_path))
        .await
        .map_err(|e| format!("import_path_kind did not complete: {e}"))?
}

fn path_kind_at(abs_path: &str, project_roots: &[String]) -> Result<String, String> {
    let path = require_jailed_absolute(abs_path, project_roots)?;
    #[cfg(unix)]
    if path_traverses_symbolic_link(&path, project_roots) {
        return Err("attachment paths may not traverse symbolic links".to_string());
    }
    match fs::symlink_metadata(&path) {
        Ok(meta) => {
            if meta.is_dir() {
                Ok("folder".to_string())
            } else if meta.is_file() {
                Ok("file".to_string())
            } else {
                Ok("missing".to_string())
            }
        }
        Err(_) => Ok("missing".to_string()),
    }
}

#[cfg(unix)]
fn path_traverses_symbolic_link(path: &PathBuf, project_roots: &[String]) -> bool {
    for root_raw in project_roots {
        let root = PathBuf::from(root_raw.trim());
        if !root.is_absolute() {
            continue;
        }
        let Ok(relative) = path.strip_prefix(&root) else {
            continue;
        };
        if relative
            .components()
            .any(|part| !matches!(part, Component::Normal(_)))
        {
            continue;
        }
        let mut current = root;
        for component in relative.components() {
            let Component::Normal(name) = component else {
                return true;
            };
            current.push(name);
            if matches!(fs::symlink_metadata(&current), Ok(meta) if meta.is_symlink()) {
                return true;
            }
        }
    }
    false
}

fn import_path_kind_at(abs_path: &str) -> Result<String, String> {
    let trimmed = abs_path.trim();
    if trimmed.is_empty() {
        return Err("path is required".to_string());
    }
    let path = PathBuf::from(trimmed);
    if !path.is_absolute() {
        return Err("path must be absolute".to_string());
    }
    let meta = match fs::symlink_metadata(&path) {
        Ok(meta) => meta,
        Err(_) => return Ok("missing".to_string()),
    };
    let target = if meta.is_symlink() {
        match fs::canonicalize(&path) {
            Ok(target) => target,
            Err(_) => return Ok("missing".to_string()),
        }
    } else {
        path
    };
    match fs::metadata(target) {
        Ok(meta) if meta.is_file() => Ok("file".to_string()),
        Ok(meta) if meta.is_dir() => Ok("folder".to_string()),
        Ok(_) | Err(_) => Ok("missing".to_string()),
    }
}

#[tauri::command]
pub async fn read_path_bytes(
    abs_path: String,
    project_roots: Vec<String>,
    max_bytes: u64,
) -> Result<Vec<u8>, PathBytesError> {
    tauri::async_runtime::spawn_blocking(move || {
        read_path_bytes_at(&abs_path, &project_roots, max_bytes)
    })
    .await
    .map_err(|e| {
        PathBytesError::new(
            "attachment_unavailable",
            format!("read_path_bytes did not complete: {e}"),
        )
    })?
}

#[derive(Debug, Clone, Serialize)]
pub struct PathBytesError {
    pub code: String,
    pub message: String,
}

impl PathBytesError {
    fn new(code: &str, message: impl Into<String>) -> Self {
        Self {
            code: code.to_string(),
            message: message.into(),
        }
    }
}

fn read_path_bytes_at(
    abs_path: &str,
    project_roots: &[String],
    max_bytes: u64,
) -> Result<Vec<u8>, PathBytesError> {
    let file = open_jailed_file(abs_path, project_roots)?;
    let file_meta = file
        .metadata()
        .map_err(|e| PathBytesError::new("attachment_not_found", e.to_string()))?;
    if !file_meta.is_file() {
        return Err(PathBytesError::new(
            "unsupported_attachment",
            "path is not a file",
        ));
    }
    if file_meta.len() > max_bytes {
        return Err(PathBytesError::new(
            "attachment_too_large",
            format!("file exceeds max_bytes ({max_bytes})"),
        ));
    }
    let mut bytes = Vec::with_capacity(file_meta.len().min(max_bytes).min(1 << 20) as usize);
    file.take(max_bytes.saturating_add(1))
        .read_to_end(&mut bytes)
        .map_err(|e| PathBytesError::new("attachment_unavailable", e.to_string()))?;
    if (bytes.len() as u64) > max_bytes {
        return Err(PathBytesError::new(
            "attachment_too_large",
            format!("file exceeds max_bytes ({max_bytes})"),
        ));
    }
    Ok(bytes)
}

#[cfg(unix)]
fn open_jailed_file(abs_path: &str, project_roots: &[String]) -> Result<File, PathBytesError> {
    let path = require_absolute_path(abs_path)?;
    for root_raw in project_roots {
        let root = PathBuf::from(root_raw.trim());
        if !root.is_absolute() {
            continue;
        }
        let Ok(relative) = path.strip_prefix(&root) else {
            continue;
        };
        let components: Vec<_> = relative.components().collect();
        if components.is_empty()
            || components
                .iter()
                .any(|part| !matches!(part, Component::Normal(_)))
        {
            continue;
        }
        let mut current = File::open(&root)
            .map_err(|e| PathBytesError::new("attachment_not_found", e.to_string()))?;
        let root_meta = current
            .metadata()
            .map_err(|e| PathBytesError::new("attachment_not_found", e.to_string()))?;
        if !root_meta.is_dir() {
            continue;
        }
        for (index, component) in components.iter().enumerate() {
            let Component::Normal(name) = component else {
                return Err(PathBytesError::new(
                    "unsupported_attachment",
                    "path is outside attached project roots",
                ));
            };
            let name = CString::new(name.as_bytes()).map_err(|_| {
                PathBytesError::new("unsupported_attachment", "path contains a null byte")
            })?;
            let mut flags = libc::O_RDONLY | libc::O_CLOEXEC | libc::O_NOFOLLOW;
            if index + 1 < components.len() {
                flags |= libc::O_DIRECTORY;
            }
            // Each step stays relative to the held directory descriptor.
            let fd = unsafe { libc::openat(current.as_raw_fd(), name.as_ptr(), flags) };
            if fd < 0 {
                let err = std::io::Error::last_os_error();
                if matches!(err.raw_os_error(), Some(code) if code == libc::ELOOP || code == libc::ENOTDIR)
                {
                    return Err(PathBytesError::new(
                        "unsupported_attachment",
                        "attachment paths may not traverse symbolic links",
                    ));
                }
                return Err(PathBytesError::new("attachment_not_found", err.to_string()));
            }
            current = unsafe { File::from_raw_fd(fd) };
        }
        return Ok(current);
    }
    Err(PathBytesError::new(
        "unsupported_attachment",
        "path is outside attached project roots",
    ))
}

#[cfg(windows)]
fn open_jailed_file(abs_path: &str, project_roots: &[String]) -> Result<File, PathBytesError> {
    use std::os::windows::io::AsRawHandle;
    use windows_sys::Win32::Foundation::HANDLE;
    use windows_sys::Win32::Storage::FileSystem::{
        GetFinalPathNameByHandleW, FILE_NAME_NORMALIZED, VOLUME_NAME_DOS,
    };

    let path = require_absolute_path(abs_path)?;
    use std::fs::OpenOptions;
    use std::os::windows::fs::OpenOptionsExt;
    let file = OpenOptions::new()
        .read(true)
        .share_mode(windows_sys::Win32::Storage::FileSystem::FILE_SHARE_READ)
        .open(&path)
        .map_err(|e| PathBytesError::new("attachment_not_found", e.to_string()))?;
    let handle = file.as_raw_handle();
    let mut buf = vec![0u16; 32_768];
    let len = unsafe {
        GetFinalPathNameByHandleW(
            handle as HANDLE,
            buf.as_mut_ptr(),
            buf.len() as u32,
            FILE_NAME_NORMALIZED | VOLUME_NAME_DOS,
        )
    };
    if len == 0 || len as usize >= buf.len() {
        return Err(PathBytesError::new(
            "attachment_unavailable",
            "could not resolve the opened attachment handle",
        ));
    }
    let mut opened = String::from_utf16_lossy(&buf[..len as usize]);
    if let Some(stripped) = opened.strip_prefix(r"\\?\UNC\") {
        opened = format!(r"\\{stripped}");
    } else if let Some(stripped) = opened.strip_prefix(r"\\?\") {
        opened = stripped.to_string();
    }
    let opened = PathBuf::from(opened);
    if !path_under_roots(&opened, project_roots) {
        return Err(PathBytesError::new(
            "unsupported_attachment",
            "opened file is outside attached project roots",
        ));
    }
    Ok(file)
}

#[cfg(not(any(unix, windows)))]
fn open_jailed_file(_abs_path: &str, _project_roots: &[String]) -> Result<File, PathBytesError> {
    Err(PathBytesError::new(
        "attachment_unavailable",
        "secure attachment snapshots are unsupported on this platform",
    ))
}

fn require_absolute_path(abs_path: &str) -> Result<PathBuf, PathBytesError> {
    let trimmed = abs_path.trim();
    if trimmed.is_empty() {
        return Err(PathBytesError::new(
            "unsupported_attachment",
            "path is required",
        ));
    }
    let path = PathBuf::from(trimmed);
    if !path.is_absolute() {
        return Err(PathBytesError::new(
            "unsupported_attachment",
            "path must be absolute",
        ));
    }
    Ok(path)
}

fn require_jailed_absolute(abs_path: &str, project_roots: &[String]) -> Result<PathBuf, String> {
    let trimmed = abs_path.trim();
    if trimmed.is_empty() {
        return Err("path is required".to_string());
    }
    let path = PathBuf::from(trimmed);
    if !path.is_absolute() {
        return Err("path must be absolute".to_string());
    }
    if !path_under_roots(&path, project_roots) {
        return Err("path is outside attached project roots".to_string());
    }
    Ok(path)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_support::TempDir;

    #[test]
    fn path_kind_file_folder_missing() {
        let dir = TempDir::new("read-path");
        let root = dir.path().to_string_lossy().to_string();
        let file = dir.path().join("a.txt");
        fs::write(&file, b"hi").unwrap();
        let folder = dir.path().join("sub");
        fs::create_dir(&folder).unwrap();

        assert_eq!(
            path_kind_at(&file.to_string_lossy(), &[root.clone()]).unwrap(),
            "file"
        );
        assert_eq!(
            path_kind_at(&folder.to_string_lossy(), &[root.clone()]).unwrap(),
            "folder"
        );
        assert_eq!(
            path_kind_at(&dir.path().join("gone.txt").to_string_lossy(), &[root]).unwrap(),
            "missing"
        );
    }

    #[test]
    fn path_kind_rejects_outside_jail() {
        let dir = TempDir::new("read-path");
        let root = dir.path().to_string_lossy().to_string();
        let elsewhere = TempDir::new("outside-jail");
        let outside = elsewhere.path().join("outside.txt");
        let _ = fs::write(&outside, b"x");
        let err = path_kind_at(&outside.to_string_lossy(), &[root]).unwrap_err();
        assert!(err.contains("outside"));
    }

    #[test]
    fn read_path_bytes_honors_jail_and_cap() {
        let dir = TempDir::new("read-path");
        let root = dir.path().to_string_lossy().to_string();
        let file = dir.path().join("img.bin");
        fs::write(&file, b"abcdef").unwrap();

        let got = read_path_bytes_at(&file.to_string_lossy(), &[root.clone()], 16).unwrap();
        assert_eq!(got, b"abcdef");

        let too_big = read_path_bytes_at(&file.to_string_lossy(), &[root.clone()], 3).unwrap_err();
        assert_eq!(too_big.code, "attachment_too_large");
        assert!(too_big.message.contains("max_bytes"));

        let elsewhere = TempDir::new("outside-read");
        let outside = elsewhere.path().join("outside.bin");
        fs::write(&outside, b"nope").unwrap();
        let jail_err = read_path_bytes_at(&outside.to_string_lossy(), &[root], 16).unwrap_err();
        assert!(jail_err.message.contains("outside"));
    }

    #[test]
    #[cfg(unix)]
    fn read_path_bytes_refuses_symlink_escape() {
        let dir = TempDir::new("read-path");
        let root = dir.path().to_string_lossy().to_string();
        let elsewhere = TempDir::new("symlink-escape");
        let outside = elsewhere.path().join("target.bin");
        fs::write(&outside, b"secret").unwrap();
        let link = dir.path().join("escape.link");
        std::os::unix::fs::symlink(&outside, &link).unwrap();

        let err = read_path_bytes_at(&link.to_string_lossy(), &[root], 1024).unwrap_err();
        assert_eq!(err.code, "unsupported_attachment");
        assert!(err.message.contains("symbolic"), "got {:?}", err);
    }

    #[test]
    #[cfg(unix)]
    fn read_path_bytes_refuses_even_in_root_symlink_components() {
        let dir = TempDir::new("read-path");
        let root = dir.path().to_string_lossy().to_string();
        let real = dir.path().join("real");
        fs::create_dir(&real).unwrap();
        fs::write(real.join("image.bin"), b"image").unwrap();
        let link = dir.path().join("linked");
        std::os::unix::fs::symlink(&real, &link).unwrap();

        let err = read_path_bytes_at(&link.join("image.bin").to_string_lossy(), &[root], 1024)
            .unwrap_err();
        assert_eq!(err.code, "unsupported_attachment");
        assert!(err.message.contains("symbolic"), "got {:?}", err);
    }

    #[test]
    #[cfg(unix)]
    fn path_kind_refuses_symbolic_link_components_before_attachment_staging() {
        let dir = TempDir::new("read-path");
        let root = dir.path().to_string_lossy().to_string();
        let real = dir.path().join("real");
        fs::create_dir(&real).unwrap();
        fs::write(real.join("image.bin"), b"image").unwrap();
        let link = dir.path().join("linked");
        std::os::unix::fs::symlink(&real, &link).unwrap();

        let err = path_kind_at(&link.join("image.bin").to_string_lossy(), &[root]).unwrap_err();
        assert!(err.contains("symbolic"), "got {err}");
    }

    #[test]
    fn require_absolute() {
        let err = path_kind_at("rel/path", &["/proj".into()]).unwrap_err();
        assert!(err.contains("absolute"));
    }

    #[test]
    fn import_path_kind_classifies_external_regular_files_without_a_jail() {
        let dir = TempDir::new("import-path");
        let file = dir.path().join("outside.txt");
        fs::write(&file, b"snapshot").expect("write import file");
        let folder = dir.path().join("folder");
        fs::create_dir(&folder).expect("create import folder");

        assert_eq!(
            import_path_kind_at(&file.to_string_lossy()).expect("classify import file"),
            "file"
        );
        assert_eq!(
            import_path_kind_at(&folder.to_string_lossy()).expect("classify import folder"),
            "folder"
        );
        assert_eq!(
            import_path_kind_at(&dir.path().join("gone").to_string_lossy())
                .expect("classify missing import path"),
            "missing"
        );
    }
}
