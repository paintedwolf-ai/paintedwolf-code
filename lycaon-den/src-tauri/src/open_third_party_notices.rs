//! Open the generated third-party notices file with the OS default app.
//!
//! Packaged: `Contents/Resources/THIRD-PARTY-NOTICES.md` via the app resource directory.
//! Dev: repo-root `THIRD-PARTY-NOTICES.md` (produced by `./task licenses:notices`).

use std::path::{Path, PathBuf};
use std::process::Command;

use tauri::{AppHandle, Manager};

const NOTICES_NAME: &str = "THIRD-PARTY-NOTICES.md";

/// Resolve and open THIRD-PARTY-NOTICES.md (no in-app viewer).
#[tauri::command]
pub fn open_third_party_notices(app: AppHandle) -> Result<(), String> {
    let path = resolve_notices_path(&app)?;
    if !path.is_file() {
        return Err(format!(
            "third-party notices not found at {} — run ./task licenses:notices",
            path.display()
        ));
    }
    spawn_open(&path)
}

fn resolve_notices_path(app: &AppHandle) -> Result<PathBuf, String> {
    // Packaged / resource-bundled copy first.
    if let Ok(resource_dir) = app.path().resource_dir() {
        let candidate = resource_dir.join(NOTICES_NAME);
        if candidate.is_file() {
            return Ok(candidate);
        }
    }

    // Dev: walk up from the current exe / cwd looking for the repo-root file.
    for start in [
        std::env::current_dir().ok(),
        std::env::current_exe()
            .ok()
            .and_then(|p| p.parent().map(|d| d.to_path_buf())),
    ]
    .into_iter()
    .flatten()
    {
        if let Some(found) = find_upwards(&start, NOTICES_NAME, 8) {
            return Ok(found);
        }
    }

    Err(format!(
        "third-party notices not found — run ./task licenses:notices to generate {NOTICES_NAME}"
    ))
}

fn find_upwards(start: &Path, name: &str, max_up: usize) -> Option<PathBuf> {
    let mut cur = start.to_path_buf();
    for _ in 0..=max_up {
        let candidate = cur.join(name);
        if candidate.is_file() {
            return Some(candidate);
        }
        if !cur.pop() {
            break;
        }
    }
    None
}

fn spawn_open(path: &Path) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        Command::new("open")
            .arg(path)
            .spawn()
            .map_err(|e| format!("open notices: {e}"))?;
        return Ok(());
    }
    #[cfg(target_os = "linux")]
    {
        Command::new("xdg-open")
            .arg(path)
            .spawn()
            .map_err(|e| format!("xdg-open notices: {e}"))?;
        return Ok(());
    }
    #[cfg(target_os = "windows")]
    {
        Command::new("cmd")
            .args(["/C", "start", "", &path.to_string_lossy()])
            .spawn()
            .map_err(|e| format!("start notices: {e}"))?;
        return Ok(());
    }
    #[cfg(not(any(target_os = "macos", target_os = "linux", target_os = "windows")))]
    {
        let _ = path;
        Err("open third-party notices is not supported on this platform".into())
    }
}
