//! Detect installed external editors from filesystem and PATH state.
//! Returned IDs match supported launch presets.

use self::catalog::{catalog, EditorCatalogEntry};
#[cfg(target_os = "macos")]
use std::path::PathBuf;
use which::which;

pub(crate) mod catalog;
#[cfg(test)]
mod tests;

#[derive(serde::Serialize, Clone, Debug, PartialEq, Eq)]
#[serde(rename_all = "camelCase")]
pub struct FoundEditor {
    pub id: String,
    pub display_name: String,
    pub install_path: String,
}

/// Filesystem and PATH probe.
pub trait EditorProbe {
    fn path_exists(&self, path: &str) -> bool;
    fn cli_on_path(&self, cli: &str) -> bool;
}

struct SystemProbe;

impl EditorProbe for SystemProbe {
    fn path_exists(&self, path: &str) -> bool {
        std::path::Path::new(path).exists()
    }
    fn cli_on_path(&self, cli: &str) -> bool {
        which(cli).is_ok()
    }
}

/// Find an install path, preferring the CLI on PATH.
fn find_among(probe: &impl EditorProbe, cli: &str, candidates: &[String]) -> Option<String> {
    if !cli.is_empty() && probe.cli_on_path(cli) {
        return Some(format!("cli:{cli}"));
    }
    for c in candidates {
        if probe.path_exists(c) {
            return Some(c.clone());
        }
    }
    None
}

/// Return bundle paths in probe priority order.
#[cfg(target_os = "macos")]
fn macos_bundle_candidates_with_home(
    entry: &EditorCatalogEntry,
    home: Option<&str>,
) -> Vec<String> {
    let mut out = Vec::new();
    for bundle in entry.macos_app_bundles {
        out.push(format!("/Applications/{bundle}"));
        if let Some(home) = home {
            out.push(format!("{home}/Applications/{bundle}"));
        }
    }
    out
}

/// Bundle lookup is compiled only on its supported platform.
#[cfg(target_os = "macos")]
fn macos_bundle_candidates(entry: &EditorCatalogEntry) -> Vec<String> {
    macos_bundle_candidates_with_home(entry, std::env::var("HOME").ok().as_deref())
}

impl EditorCatalogEntry {
    fn find(&self, probe: &impl EditorProbe) -> Option<String> {
        #[cfg(target_os = "macos")]
        {
            find_among(probe, self.macos_cli, &macos_bundle_candidates(self))
        }
        #[cfg(target_os = "windows")]
        {
            let mut candidates = Vec::new();
            if let Ok(local) = std::env::var("LOCALAPPDATA") {
                if !self.windows_install_rel.is_empty() {
                    candidates.push(format!("{local}\\Programs\\{}", self.windows_install_rel));
                }
            }
            // Prefer the command-line entry.
            for cli in self.windows_clis {
                if let Some(p) = find_among(probe, cli, &candidates) {
                    return Some(p);
                }
            }
            None
        }
        #[cfg(target_os = "linux")]
        {
            let candidates: Vec<String> = self
                .linux_candidate_paths
                .iter()
                .map(|s| s.to_string())
                .collect();
            find_among(probe, self.linux_cli, &candidates)
        }
        #[cfg(not(any(target_os = "macos", target_os = "windows", target_os = "linux")))]
        {
            let _ = probe;
            None
        }
    }
}

/// Detect installed editors in catalog order.
pub fn detect_installed_editors() -> Vec<FoundEditor> {
    detect_installed_editors_with(&SystemProbe)
}

fn detect_installed_editors_with(probe: &impl EditorProbe) -> Vec<FoundEditor> {
    let mut out = Vec::new();
    for entry in catalog() {
        if let Some(install_path) = entry.find(probe) {
            out.push(FoundEditor {
                id: entry.id.to_string(),
                display_name: entry.display_name.to_string(),
                install_path,
            });
        }
    }
    out
}

/// Return the first installed bundle path.
#[cfg(target_os = "macos")]
pub(crate) fn macos_bundle_path_for_preset(preset: &str) -> Option<PathBuf> {
    let entry = catalog().iter().find(|e| e.id == preset)?;
    for c in macos_bundle_candidates(entry) {
        let p = PathBuf::from(&c);
        if p.exists() {
            return Some(p);
        }
    }
    None
}

/// Lists installed editors for the Settings dropdown.
#[tauri::command]
pub fn detect_editors() -> Vec<FoundEditor> {
    detect_installed_editors()
}
