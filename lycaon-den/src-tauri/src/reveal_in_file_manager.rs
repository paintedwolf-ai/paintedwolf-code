//! Reveal a path in the platform file manager.

use std::path::{Component, Path, PathBuf};
use std::process::Command;

/// Reveal `absolute_path` in the platform file manager after root validation.
#[tauri::command]
pub fn reveal_in_file_manager(
    absolute_path: String,
    project_roots: Vec<String>,
) -> Result<(), String> {
    let trimmed = absolute_path.trim();
    if trimmed.is_empty() {
        return Err("path is required".to_string());
    }
    let path = PathBuf::from(trimmed);
    if !path.is_absolute() {
        return Err("path must be absolute".to_string());
    }
    if !path_under_roots(&path, &project_roots) {
        return Err("path is outside attached project roots".to_string());
    }
    spawn_reveal(&path)
}

/// Reveal an existing absolute path the person typed or pasted into search, wherever it lives.
/// Revealing only selects the item in the file manager, so it needs no root jail; opening a
/// path with an application stays jailed to project roots.
#[tauri::command]
pub fn reveal_typed_path_in_file_manager(absolute_path: String) -> Result<(), String> {
    let path = typed_reveal_target(&absolute_path)?;
    spawn_reveal(&path)
}

fn typed_reveal_target(absolute_path: &str) -> Result<PathBuf, String> {
    let trimmed = absolute_path.trim();
    if trimmed.is_empty() {
        return Err("path is required".to_string());
    }
    let path = PathBuf::from(trimmed);
    if !path.is_absolute() {
        return Err("path must be absolute".to_string());
    }
    if std::fs::symlink_metadata(&path).is_err() {
        return Err("path does not exist".to_string());
    }
    Ok(path)
}

/// Check whether a path is within an attached root.
pub(crate) fn path_under_roots(path: &Path, roots: &[String]) -> bool {
    let candidate = normalize_for_jail(path);
    for root_raw in roots {
        let root_trim = root_raw.trim();
        if root_trim.is_empty() {
            continue;
        }
        let root_path = PathBuf::from(root_trim);
        if !root_path.is_absolute() {
            continue;
        }
        let root = normalize_for_jail(&root_path);
        if paths_equal(&candidate, &root) || candidate.starts_with(&root) {
            return true;
        }
    }
    false
}

/// Canonicalize the longest existing prefix for missing children.
fn normalize_for_jail(path: &Path) -> PathBuf {
    if let Ok(canon) = path.canonicalize() {
        return canon;
    }
    let mut suffix: Vec<std::ffi::OsString> = Vec::new();
    let mut cur = path.to_path_buf();
    loop {
        if let Ok(canon) = cur.canonicalize() {
            let mut out = canon;
            for part in suffix.iter().rev() {
                out.push(part);
            }
            return out;
        }
        match cur.file_name() {
            Some(name) => {
                suffix.push(name.to_owned());
                if !cur.pop() {
                    break;
                }
            }
            None => break,
        }
    }
    lexical_normalize(path)
}

fn lexical_normalize(path: &Path) -> PathBuf {
    let mut out = PathBuf::new();
    for comp in path.components() {
        match comp {
            Component::CurDir => {}
            Component::ParentDir => {
                out.pop();
            }
            Component::RootDir | Component::Prefix(_) => out.push(comp.as_os_str()),
            Component::Normal(s) => out.push(s),
        }
    }
    out
}

fn paths_equal(a: &Path, b: &Path) -> bool {
    #[cfg(windows)]
    {
        a.as_os_str().eq_ignore_ascii_case(b.as_os_str())
    }
    #[cfg(not(windows))]
    {
        a == b
    }
}

fn spawn_reveal(path: &Path) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        let status = Command::new("open")
            .arg("-R")
            .arg(path)
            .status()
            .map_err(|e| e.to_string())?;
        if status.success() {
            Ok(())
        } else {
            Err(format!("open -R exited with {status}"))
        }
    }

    #[cfg(target_os = "windows")]
    {
        // The selector argument has no space after the comma.
        let arg = format!("/select,{}", path.display());
        let status = Command::new("explorer")
            .arg(arg)
            .status()
            .map_err(|e| e.to_string())?;
        // A completed command is sufficient on Windows.
        let _ = status;
        Ok(())
    }

    #[cfg(target_os = "linux")]
    {
        if try_linux_show_items(path).is_ok() {
            return Ok(());
        }
        let parent = path.parent().unwrap_or(path);
        let status = Command::new("xdg-open")
            .arg(parent)
            .status()
            .map_err(|e| e.to_string())?;
        if status.success() {
            Ok(())
        } else {
            Err(format!("xdg-open exited with {status}"))
        }
    }

    #[cfg(not(any(target_os = "macos", target_os = "windows", target_os = "linux")))]
    {
        let _ = path;
        Err("reveal is not supported on this platform".to_string())
    }
}

#[cfg(target_os = "linux")]
fn try_linux_show_items(path: &Path) -> Result<(), String> {
    let uri = path_to_file_uri(path)?;
    let status = Command::new("dbus-send")
        .args([
            "--session",
            "--dest=org.freedesktop.FileManager1",
            "--type=method_call",
            "/org/freedesktop/FileManager1",
            "org.freedesktop.FileManager1.ShowItems",
        ])
        .arg(format!("array:string:{uri}"))
        .arg("string:")
        .status()
        .map_err(|e| e.to_string())?;
    if status.success() {
        Ok(())
    } else {
        Err(format!("ShowItems exited with {status}"))
    }
}

#[cfg(target_os = "linux")]
fn path_to_file_uri(path: &Path) -> Result<String, String> {
    use std::fmt::Write as _;

    let abs = path
        .to_str()
        .ok_or_else(|| "path is not valid UTF-8".to_string())?;
    // Preserve separators and percent-encode other bytes.
    let mut encoded = String::with_capacity(abs.len());
    for byte in abs.bytes() {
        match byte {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' | b'/' => {
                encoded.push(byte as char);
            }
            _ => {
                write!(&mut encoded, "%{byte:02X}").expect("writing to String cannot fail");
            }
        }
    }
    Ok(format!("file://{encoded}"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn typed_reveal_requires_an_existing_absolute_path() {
        let dir = std::env::temp_dir();
        assert_eq!(typed_reveal_target(dir.to_str().unwrap()), Ok(dir.clone()));
        assert!(typed_reveal_target("").is_err());
        assert!(typed_reveal_target("relative/path").is_err());
        assert!(typed_reveal_target(dir.join("painted-wolf-missing-9f3c").to_str().unwrap()).is_err());
    }

    #[test]
    fn jail_accepts_under_root_and_root_itself() {
        let roots = vec!["/proj".to_string()];
        assert!(path_under_roots(Path::new("/proj"), &roots));
        assert!(path_under_roots(Path::new("/proj/src/a.go"), &roots));
    }

    #[test]
    fn jail_rejects_sibling_prefix() {
        let roots = vec!["/proj".to_string()];
        assert!(!path_under_roots(Path::new("/proj-evil/x"), &roots));
        assert!(!path_under_roots(Path::new("/other"), &roots));
    }

    #[test]
    fn jail_rejects_relative_and_empty_roots() {
        let roots = vec!["/proj".to_string()];
        assert!(!path_under_roots(Path::new("rel"), &roots));
        assert!(!path_under_roots(Path::new("/proj/a"), &[]));
        assert!(!path_under_roots(
            Path::new("/proj/a"),
            &["rel".to_string()]
        ));
    }

    #[test]
    fn lexical_normalize_drops_dot_dot() {
        let n = lexical_normalize(Path::new("/proj/./src/../src/a.go"));
        assert_eq!(n, PathBuf::from("/proj/src/a.go"));
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn linux_file_uri_encodes_reserved_and_unicode_bytes() {
        assert_eq!(
            path_to_file_uri(Path::new("/repo/a #?%é.txt")).unwrap(),
            "file:///repo/a%20%23%3F%25%C3%A9.txt",
        );
    }
}
