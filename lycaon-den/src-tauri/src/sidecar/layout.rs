use std::path::{Path, PathBuf};
use std::sync::OnceLock;
use tauri::Manager;

pub(super) const BUNDLED_SIDECAR_NAME: &str = "pw";
pub(super) const BUNDLED_ENGINE_RESOURCES_DIR: &str = "engine-root";
pub(super) const BUNDLED_ENGINE_MARKER: &str = "schemas/oar/oar.schema.json";

#[derive(Clone)]
pub(super) struct BundledEngineLayout {
    pub(super) binary: PathBuf,
    pub(super) engine_root: PathBuf,
}

static BUNDLED_ENGINE_LAYOUT: OnceLock<Option<BundledEngineLayout>> = OnceLock::new();

pub(super) fn bundled_sidecar_name(target_os: &str) -> &'static str {
    if target_os == "windows" {
        "pw.exe"
    } else {
        BUNDLED_SIDECAR_NAME
    }
}

pub(crate) fn bundled_sidecar_path(executable: &Path, target_os: &str) -> Option<PathBuf> {
    let directory = executable.parent()?;
    if target_os == "macos" {
        Some(
            directory
                .parent()?
                .join("Helpers/Painted Wolf Code engine.app/Contents/MacOS/pw"),
        )
    } else {
        Some(directory.join(bundled_sidecar_name(target_os)))
    }
}

pub(super) fn bundled_engine_layout_from(
    executable: &Path,
    resource_dir: &Path,
    target_os: &str,
) -> Option<BundledEngineLayout> {
    let binary = bundled_sidecar_path(executable, target_os)?;
    if !binary.is_file() {
        return None;
    }
    let engine_root = resource_dir.join(BUNDLED_ENGINE_RESOURCES_DIR);
    if !engine_root.join(BUNDLED_ENGINE_MARKER).is_file() {
        return None;
    }
    Some(BundledEngineLayout {
        binary,
        engine_root,
    })
}

pub(crate) fn setup_bundled_engine_layout(app: &tauri::AppHandle) {
    let layout = std::env::current_exe()
        .ok()
        .and_then(|exe| {
            app.path()
                .resource_dir()
                .ok()
                .map(|resources| (exe, resources))
        })
        .and_then(|(exe, resources)| {
            bundled_engine_layout_from(&exe, &resources, std::env::consts::OS)
        });
    let _ = BUNDLED_ENGINE_LAYOUT.set(layout);
}

pub(super) fn bundled_engine_layout() -> Option<BundledEngineLayout> {
    BUNDLED_ENGINE_LAYOUT.get().cloned().flatten()
}

pub(super) fn resolve_lycaon_binary() -> Result<PathBuf, String> {
    if let Some(layout) = bundled_engine_layout() {
        return Ok(layout.binary);
    }
    #[cfg(debug_assertions)]
    {
        if let Ok(raw) = std::env::var("LYCAON_SIDECAR_BIN") {
            let path = PathBuf::from(raw.trim());
            if path.is_file() {
                return Ok(path);
            }
            return Err(format!("LYCAON_SIDECAR_BIN not found: {}", path.display()));
        }
        if let Ok(path) = which::which("lycaon") {
            return Ok(path);
        }
    }
    Err("bundled engine helper not found in the app".into())
}
