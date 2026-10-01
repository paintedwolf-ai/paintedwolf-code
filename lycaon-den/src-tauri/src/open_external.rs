//! File opening is confined to supplied project roots.
//! Editor commands come from the shared detection catalog.

use crate::reveal_in_file_manager::path_under_roots;
use std::path::{Path, PathBuf};
use std::process::Command;

/// Open `abs_path` in an external editor at optional `line`.
/// The root check catches caller mistakes; configured launch arguments provide authority.
#[tauri::command]
pub async fn open_in_editor(
    abs_path: String,
    line: Option<u32>,
    preset: String,
    custom_template: Option<String>,
    custom_folder_template: Option<String>,
    project_roots: Vec<String>,
) -> Result<(), String> {
    tauri::async_runtime::spawn_blocking(move || {
        let trimmed = abs_path.trim();
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
        if !path.exists() {
            return Err("This item is not present on disk.".into());
        }
        let line_str = line
            .filter(|n| *n > 0)
            .map(|n| n.to_string())
            .unwrap_or_else(|| "1".to_string());
        let path_str = path.to_string_lossy().to_string();
        match preset.as_str() {
            "custom" => {
                let file_template = custom_template.unwrap_or_default();
                let folder_template = custom_folder_template.unwrap_or_default();
                let tmpl = editor_template(&file_template, &folder_template, path.is_dir())?;
                spawn_custom_editor(tmpl, &path_str, &line_str)
            }
            other => spawn_preset_editor(other, &path_str, &line_str),
        }
    })
    .await
    .map_err(|error| error.to_string())?
}

/// Open `url` in the chosen browser preset.
#[tauri::command]
pub async fn open_in_browser(
    url: String,
    preset: String,
    custom_template: Option<String>,
) -> Result<(), String> {
    let url = url.trim();
    if !is_allowed_external_url(url) {
        return Err("url scheme is not allowed".to_string());
    }
    let url = url.to_string();
    tauri::async_runtime::spawn_blocking(move || spawn_browser(&url, &preset, custom_template))
        .await
        .map_err(|error| error.to_string())?
}

pub(crate) fn spawn_browser(
    url: &str,
    preset: &str,
    custom_template: Option<String>,
) -> Result<(), String> {
    match preset {
        "system-default" => spawn_system_browser(url),
        "chrome" => spawn_named_browser("chrome", url),
        "firefox" => spawn_named_browser("firefox", url),
        "safari" => spawn_safari(url),
        "custom" => {
            let tmpl = custom_template.unwrap_or_default();
            spawn_custom_browser(&tmpl, url)
        }
        other => Err(format!("unknown browser preset: {other}")),
    }
}

fn is_allowed_external_url(url: &str) -> bool {
    let lower = url.to_ascii_lowercase();
    lower.starts_with("http://")
        || lower.starts_with("https://")
        || lower.starts_with("mailto:")
        || lower.starts_with("tel:")
}

/// Split before substitution so each path or URL stays one argument.
fn tokenize_template(template: &str, pairs: &[(&str, &str)]) -> Result<Vec<String>, String> {
    let parts: Vec<String> = template
        .split_whitespace()
        .filter(|s| !s.is_empty())
        .map(|token| replace_tokens(token, pairs))
        .filter(|s| !s.is_empty())
        .collect();
    if parts.is_empty() {
        return Err("custom command is empty".to_string());
    }
    Ok(parts)
}

/// Substituted values are literal, even when a filename contains a placeholder.
fn replace_tokens(mut input: &str, pairs: &[(&str, &str)]) -> String {
    let mut output = String::new();
    while let Some(start) = input.find('{') {
        output.push_str(&input[..start]);
        input = &input[start..];
        if let Some((key, value)) = pairs.iter().find(|(key, _)| input.starts_with(key)) {
            output.push_str(value);
            input = &input[key.len()..];
        } else {
            output.push('{');
            input = &input[1..];
        }
    }
    output.push_str(input);
    output
}

fn spawn_argv(program: &str, args: &[&str]) -> Result<(), String> {
    let status = Command::new(program)
        .args(args)
        .status()
        .map_err(|e| e.to_string())?;
    if status.success() {
        Ok(())
    } else {
        Err(format!("{program} exited with {status}"))
    }
}

/// Each substituted path stays in one argument, including embedded spaces.
fn substitute_tokens(args: &[&str], path: &str, line: &str) -> Vec<String> {
    args.iter()
        .map(|a| replace_tokens(a, &[("{path}", path), ("{line}", line)]))
        .collect()
}

fn spawn_argv_owned(program: &str, args: &[String]) -> Result<(), String> {
    let refs: Vec<&str> = args.iter().map(|s| s.as_str()).collect();
    spawn_argv(program, &refs)
}

/// Editor discovery and launch share a catalog entry.
fn spawn_preset_editor(preset: &str, path: &str, line: &str) -> Result<(), String> {
    let entry = crate::detect_editors::catalog::catalog()
        .iter()
        .find(|e| e.id == preset)
        .ok_or_else(|| format!("unknown editor preset: {preset}"))?;
    let folder = Path::new(path).is_dir();
    let cli_args = substitute_tokens(
        if folder { entry.folder_cli_args } else { entry.cli_args },
        path,
        line,
    );

    #[cfg(target_os = "windows")]
    {
        for cli in entry.windows_clis {
            if spawn_argv_owned(cli, &cli_args).is_ok() {
                return Ok(());
            }
        }
        return Err(format!(
            "no {} command-line tool found on PATH",
            entry.display_name
        ));
    }
    #[cfg(target_os = "macos")]
    {
        if !entry.macos_cli.is_empty() && which::which(entry.macos_cli).is_ok() {
            return spawn_argv_owned(entry.macos_cli, &cli_args);
        }
        let bundle = crate::detect_editors::macos_bundle_path_for_preset(preset)
            .ok_or_else(|| format!("no {} application found", entry.display_name))?;
        // `open -a` expects the bundle's file stem.
        let app = match bundle.file_stem().and_then(|s| s.to_str()) {
            Some(name) => name.to_string(),
            None => entry.display_name.to_string(),
        };
        let open_args = substitute_tokens(
            if folder { entry.folder_macos_open_args } else { entry.macos_open_args },
            path,
            line,
        );
        let mut argv = vec!["-a".to_string(), app];
        argv.extend(open_args);
        spawn_argv_owned("open", &argv)
    }
    #[cfg(target_os = "linux")]
    {
        if !entry.linux_cli.is_empty() {
            if which::which(entry.linux_cli).is_ok() {
                return spawn_argv_owned(entry.linux_cli, &cli_args);
            }
            for candidate in entry.linux_candidate_paths {
                if Path::new(candidate).exists() {
                    return spawn_argv_owned(candidate, &cli_args);
                }
            }
        }
        Err(format!("no {} command-line tool found", entry.display_name))
    }
    #[cfg(not(any(target_os = "macos", target_os = "windows", target_os = "linux")))]
    {
        let _ = (path, line, cli_args);
        Err(format!(
            "unsupported platform for editor {}",
            entry.display_name
        ))
    }
}

fn editor_template<'a>(file: &'a str, folder: &'a str, is_folder: bool) -> Result<&'a str, String> {
    if !is_folder {
        return Ok(file);
    }
    if !folder.trim().is_empty() {
        return Ok(folder);
    }
    if !file.contains("{line}") {
        return Ok(file);
    }
    Err("Set a folder command in editor settings to open folders with this editor.".into())
}

fn spawn_custom_editor(template: &str, path: &str, line: &str) -> Result<(), String> {
    let parts = tokenize_template(template, &[("{path}", path), ("{line}", line)])?;
    let (prog, rest) = parts.split_first().ok_or("custom command is empty")?;
    let args: Vec<&str> = rest.iter().map(|s| s.as_str()).collect();
    spawn_argv(prog, &args)
}

fn spawn_system_browser(url: &str) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        spawn_argv("open", &[url])
    }
    #[cfg(target_os = "linux")]
    {
        spawn_argv("xdg-open", &[url])
    }
    #[cfg(target_os = "windows")]
    {
        crate::open_local_path::windows_open_associated(url)
    }
    #[cfg(not(any(target_os = "macos", target_os = "windows", target_os = "linux")))]
    {
        let _ = url;
        Err("browser open is not supported on this platform".to_string())
    }
}

fn spawn_named_browser(name: &str, url: &str) -> Result<(), String> {
    match name {
        "chrome" => spawn_chrome(url),
        "firefox" => spawn_firefox(url),
        _ => Err(format!("unknown browser: {name}")),
    }
}

fn spawn_chrome(url: &str) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        spawn_argv("open", &["-a", "Google Chrome", url])
    }
    #[cfg(target_os = "linux")]
    {
        if spawn_argv("google-chrome", &[url]).is_ok() {
            return Ok(());
        }
        spawn_argv("chromium", &[url])
    }
    #[cfg(target_os = "windows")]
    {
        spawn_argv("chrome.exe", &[url])
    }
    #[cfg(not(any(target_os = "macos", target_os = "windows", target_os = "linux")))]
    {
        let _ = url;
        Err("chrome is not supported on this platform".to_string())
    }
}

fn spawn_firefox(url: &str) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        spawn_argv("open", &["-a", "Firefox", url])
    }
    #[cfg(target_os = "linux")]
    {
        spawn_argv("firefox", &[url])
    }
    #[cfg(target_os = "windows")]
    {
        spawn_argv("firefox.exe", &[url])
    }
    #[cfg(not(any(target_os = "macos", target_os = "windows", target_os = "linux")))]
    {
        let _ = url;
        Err("firefox is not supported on this platform".to_string())
    }
}

fn spawn_safari(url: &str) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        spawn_argv("open", &["-a", "Safari", url])
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = url;
        Err("Safari is only available on macOS".to_string())
    }
}

fn spawn_custom_browser(template: &str, url: &str) -> Result<(), String> {
    let parts = tokenize_template(template, &[("{url}", url)])?;
    let (prog, rest) = parts.split_first().ok_or("custom command is empty")?;
    let args: Vec<&str> = rest.iter().map(|s| s.as_str()).collect();
    spawn_argv(prog, &args)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn filenames_with_placeholders_remain_literal() {
        let path = "/repo/{line}/file {path}.ts";
        assert_eq!(substitute_tokens(&["-g", "{path}:{line}"], path, "9"),
            vec!["-g", "/repo/{line}/file {path}.ts:9"]);
        assert_eq!(tokenize_template("editor {path}:{line}", &[("{path}", path), ("{line}", "9")]).unwrap(),
            vec!["editor", "/repo/{line}/file {path}.ts:9"]);
    }

    #[test]
    fn custom_folder_commands_do_not_inherit_line_arguments() {
        assert_eq!(editor_template("editor {path}:{line}", "editor {path}", true).unwrap(), "editor {path}");
        assert!(editor_template("editor {path}:{line}", "", true).is_err());
        assert_eq!(editor_template("editor {path}", "", true).unwrap(), "editor {path}");
        assert_eq!(editor_template("editor {path}:{line}", "folder {path}", false).unwrap(), "editor {path}:{line}");
    }

    #[test]
    fn url_allowlist() {
        assert!(is_allowed_external_url("https://example.com"));
        assert!(is_allowed_external_url("mailto:a@b.c"));
        assert!(!is_allowed_external_url("javascript:alert(1)"));
        assert!(!is_allowed_external_url("file:///etc/passwd"));
    }

    #[test]
    fn jail_rejects_outside() {
        let roots = vec!["/proj".to_string()];
        assert!(!path_under_roots(Path::new("/other/a"), &roots));
        assert!(path_under_roots(Path::new("/proj/a"), &roots));
    }

    #[test]
    fn tokenize_path_line() {
        let parts = tokenize_template(
            "cursor -g {path}:{line}",
            &[("{path}", "/a/b.ts"), ("{line}", "12")],
        )
        .unwrap();
        assert_eq!(parts, vec!["cursor", "-g", "/a/b.ts:12"]);
    }

    // Splitting before substitution keeps path contents in one argument.
    #[test]
    fn tokenize_keeps_a_whitespace_path_as_one_token() {
        let parts = tokenize_template(
            "cursor -g {path}",
            &[("{path}", "/proj/foo --reuse-window bar.ts")],
        )
        .unwrap();
        assert_eq!(
            parts,
            vec!["cursor", "-g", "/proj/foo --reuse-window bar.ts"]
        );
    }

    #[test]
    fn substitute_tokens_replaces_path_and_line() {
        let got = substitute_tokens(&["--line", "{line}", "{path}"], "/a/b.ts", "12");
        assert_eq!(got, vec!["--line", "12", "/a/b.ts"]);
    }

    #[test]
    fn substitute_tokens_keeps_arg_boundaries() {
        // `{path}:{line}` is one argv element — a path with spaces stays a
        // single token (no whitespace splitting, no shell).
        let got = substitute_tokens(&["-g", "{path}:{line}"], "/a b/c.ts", "12");
        assert_eq!(got, vec!["-g", "/a b/c.ts:12"]);
    }

    #[test]
    fn spawn_preset_editor_rejects_unknown_preset() {
        let result = spawn_preset_editor("nope", "/proj/a.ts", "12");
        assert!(result.is_err());
        assert!(
            result.unwrap_err().contains("unknown editor preset"),
            "rejects unknown preset"
        );
    }

    #[test]
    fn safari_rejected_off_macos() {
        #[cfg(not(target_os = "macos"))]
        {
            assert!(spawn_safari("https://example.com").is_err());
        }
    }
}
