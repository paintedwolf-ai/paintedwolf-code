//! Editor discovery through the desktop application database.
//!
//! An editor is installed when the desktop file the catalog names exists in
//! an XDG application directory and its program can run. The entry's `Exec`
//! line, minus field codes, is the launch command when no CLI is on `PATH`,
//! which also covers sandboxed (Flatpak, Snap) installations.

use std::path::{Path, PathBuf};

/// A desktop entry that resolved to a runnable program.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct DesktopEntry {
    pub(crate) path: PathBuf,
    /// Program and fixed arguments from `Exec`, without field codes.
    pub(crate) command: Vec<String>,
}

/// Application directories in XDG precedence order: the data home first,
/// then each data directory.
pub(crate) fn application_directories(
    data_home: Option<&str>,
    data_dirs: Option<&str>,
    home: Option<&str>,
) -> Vec<PathBuf> {
    let mut out = Vec::new();
    match data_home.filter(|dir| dir.starts_with('/')) {
        Some(dir) => out.push(PathBuf::from(dir).join("applications")),
        None => {
            if let Some(home) = home.filter(|home| home.starts_with('/')) {
                out.push(PathBuf::from(home).join(".local/share/applications"));
            }
        }
    }
    let dirs = data_dirs
        .filter(|dirs| !dirs.trim().is_empty())
        .unwrap_or("/usr/local/share:/usr/share");
    for dir in dirs.split(':').filter(|dir| dir.starts_with('/')) {
        let path = PathBuf::from(dir).join("applications");
        if !out.contains(&path) {
            out.push(path);
        }
    }
    out
}

pub(crate) fn system_application_directories() -> Vec<PathBuf> {
    application_directories(
        std::env::var("XDG_DATA_HOME").ok().as_deref(),
        std::env::var("XDG_DATA_DIRS").ok().as_deref(),
        std::env::var("HOME").ok().as_deref(),
    )
}

/// The first desktop file for any of `ids` across `directories`; the first
/// directory holding a file for an id shadows the rest, as the desktop does.
pub(crate) fn find_entry(
    ids: &[&str],
    directories: &[PathBuf],
    runnable: &dyn Fn(&str) -> bool,
) -> Option<DesktopEntry> {
    for id in ids {
        for directory in directories {
            let path = directory.join(id);
            let Ok(text) = std::fs::read_to_string(&path) else {
                continue;
            };
            // A file at this precedence decides for the id, even when it hides it.
            return parse_entry(&text).and_then(|parsed| {
                if parsed.try_exec.as_deref().is_some_and(|program| !runnable(program)) {
                    return None;
                }
                let command = exec_command(&parsed.exec);
                if command.first().is_some_and(|program| runnable(program)) {
                    Some(DesktopEntry { path: path.clone(), command })
                } else {
                    None
                }
            });
        }
    }
    None
}

struct ParsedEntry {
    exec: String,
    try_exec: Option<String>,
}

/// Reads the `[Desktop Entry]` group; hidden entries and non-application
/// types are not installed editors.
fn parse_entry(text: &str) -> Option<ParsedEntry> {
    let mut in_group = false;
    let mut exec = None;
    let mut try_exec = None;
    let mut kind = None;
    for line in text.lines() {
        let line = line.trim();
        if line.starts_with('[') {
            in_group = line == "[Desktop Entry]";
            continue;
        }
        if !in_group || line.is_empty() || line.starts_with('#') {
            continue;
        }
        let Some((key, value)) = line.split_once('=') else {
            continue;
        };
        match key.trim() {
            "Exec" => exec = Some(value.trim().to_string()),
            "TryExec" => try_exec = Some(value.trim().to_string()),
            "Type" => kind = Some(value.trim().to_string()),
            "Hidden" if value.trim() == "true" => return None,
            _ => {}
        }
    }
    if kind.is_some_and(|kind| kind != "Application") {
        return None;
    }
    Some(ParsedEntry { exec: exec?, try_exec })
}

/// Splits `Exec` with the desktop entry quoting rules and drops the field
/// codes and sandbox launcher markers that stand for the files to open.
pub(crate) fn exec_command(exec: &str) -> Vec<String> {
    let mut out = Vec::new();
    let mut current = String::new();
    let mut in_token = false;
    let mut quoted = false;
    let mut chars = exec.chars().peekable();
    while let Some(ch) = chars.next() {
        match ch {
            '"' => {
                quoted = !quoted;
                in_token = true;
            }
            '\\' if quoted => {
                if let Some(next) = chars.next() {
                    current.push(next);
                }
            }
            '%' if !quoted => match chars.next() {
                Some('%') => current.push('%'),
                Some(_) | None => {}
            },
            ' ' | '\t' if !quoted => {
                if in_token {
                    push_token(&mut out, std::mem::take(&mut current));
                    in_token = false;
                }
            }
            other => {
                current.push(other);
                in_token = true;
            }
        }
    }
    if in_token {
        push_token(&mut out, current);
    }
    out
}

fn push_token(out: &mut Vec<String>, token: String) {
    // Flatpak exports wrap the file arguments in `@@u … @@` markers.
    if token.is_empty() || token.starts_with("@@") {
        return;
    }
    out.push(token);
}

/// Whether `program` names something this process could execute.
pub(crate) fn program_runnable(program: &str) -> bool {
    if program.contains('/') {
        return Path::new(program).is_file();
    }
    which::which(program).is_ok()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_support::TempDir;

    #[test]
    fn application_directories_follow_xdg_precedence() {
        let dirs = application_directories(Some("/home/me/.data"), Some("/opt/share:/usr/share:relative:/usr/share"), Some("/home/me"));
        assert_eq!(
            dirs,
            vec![
                PathBuf::from("/home/me/.data/applications"),
                PathBuf::from("/opt/share/applications"),
                PathBuf::from("/usr/share/applications"),
            ]
        );
        let defaults = application_directories(None, None, Some("/home/me"));
        assert_eq!(
            defaults,
            vec![
                PathBuf::from("/home/me/.local/share/applications"),
                PathBuf::from("/usr/local/share/applications"),
                PathBuf::from("/usr/share/applications"),
            ]
        );
        assert_eq!(application_directories(Some("relative"), Some(""), None).len(), 2);
    }

    #[test]
    fn exec_lines_drop_field_codes_and_keep_quoted_arguments() {
        assert_eq!(exec_command("code --unity-launch %F"), vec!["code", "--unity-launch"]);
        assert_eq!(
            exec_command("/usr/bin/flatpak run --branch=stable --arch=x86_64 --command=code --file-forwarding com.visualstudio.code @@u %U @@"),
            vec!["/usr/bin/flatpak", "run", "--branch=stable", "--arch=x86_64", "--command=code", "--file-forwarding", "com.visualstudio.code"]
        );
        assert_eq!(
            exec_command("\"/opt/My Editor/bin/editor\" --name=\"a \\\"quoted\\\" name\" 100%% %f"),
            vec!["/opt/My Editor/bin/editor", "--name=a \"quoted\" name", "100%"]
        );
        assert_eq!(exec_command("env BAMF_DESKTOP_FILE_HINT=/x.desktop /snap/bin/code %F"), vec!["env", "BAMF_DESKTOP_FILE_HINT=/x.desktop", "/snap/bin/code"]);
        assert!(exec_command("   ").is_empty());
    }

    fn write_entry(dir: &Path, id: &str, body: &str) {
        std::fs::create_dir_all(dir).unwrap();
        std::fs::write(dir.join(id), body).unwrap();
    }

    #[test]
    fn entries_resolve_by_precedence_and_runnable_programs() {
        let root = TempDir::new("desktop-entries");
        let user = root.path().join("user");
        let system = root.path().join("system");
        write_entry(&system, "code.desktop", "[Desktop Entry]\nType=Application\nName=Code\nExec=/usr/share/code/code --unity-launch %F\n");
        write_entry(&system, "dev.zed.Zed.desktop", "[Desktop Entry]\nType=Application\nExec=zed %U\nTryExec=zed\n");
        write_entry(&user, "code.desktop", "[Desktop Entry]\nType=Application\nExec=flatpak run com.visualstudio.code @@u %U @@\n");
        write_entry(&user, "hidden.desktop", "[Desktop Entry]\nType=Application\nHidden=true\nExec=hidden\n");
        write_entry(&user, "link.desktop", "[Desktop Entry]\nType=Link\nURL=https://example.com\nExec=ignored\n");
        let dirs = vec![user.clone(), system.clone()];
        let runnable = |program: &str| matches!(program, "flatpak" | "/usr/share/code/code");
        let entry = find_entry(&["code.desktop"], &dirs, &runnable).unwrap();
        assert_eq!(entry.path, user.join("code.desktop"));
        assert_eq!(entry.command, vec!["flatpak", "run", "com.visualstudio.code"]);
        assert!(find_entry(&["dev.zed.Zed.desktop"], &dirs, &runnable).is_none(), "TryExec names a missing program");
        assert!(find_entry(&["hidden.desktop"], &dirs, &runnable).is_none());
        assert!(find_entry(&["link.desktop"], &dirs, &runnable).is_none());
        assert!(find_entry(&["missing.desktop"], &dirs, &runnable).is_none());
        let user_only = find_entry(&["code.desktop"], &[system.clone()], &runnable).unwrap();
        assert_eq!(user_only.command, vec!["/usr/share/code/code", "--unity-launch"]);
    }

    #[test]
    fn a_hiding_user_entry_shadows_the_system_entry() {
        let root = TempDir::new("desktop-shadow");
        let user = root.path().join("user");
        let system = root.path().join("system");
        write_entry(&user, "code.desktop", "[Desktop Entry]\nType=Application\nHidden=true\nExec=code\n");
        write_entry(&system, "code.desktop", "[Desktop Entry]\nType=Application\nExec=code %F\n");
        assert!(find_entry(&["code.desktop"], &[user, system], &|_| true).is_none());
    }

    #[test]
    fn runnable_programs_are_files_or_path_entries() {
        let root = TempDir::new("desktop-runnable");
        let program = root.path().join("editor");
        assert!(!program_runnable(program.to_str().unwrap()));
        std::fs::write(&program, b"#!/bin/sh\n").unwrap();
        assert!(program_runnable(program.to_str().unwrap()));
        assert!(!program_runnable("painted-wolf-missing-editor-9f3c"));
    }
}
