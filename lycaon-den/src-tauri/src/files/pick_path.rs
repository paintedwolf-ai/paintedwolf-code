//! App-modal native open/save panels at standard size.

use serde::{Deserialize, Serialize};
use std::path::{Path, PathBuf};

use crate::files::picked_file::{self, GrantMode};

#[derive(Deserialize)]
pub struct PickOptions {
    pub title: String,
    pub target: PickTarget,
}

#[derive(Deserialize)]
#[serde(tag = "kind", rename_all = "camelCase")]
pub enum PickTarget {
    Folder,
    File {
        filter: Option<PickFilter>,
    },
    Save {
        #[serde(rename = "fileName")]
        file_name: String,
    },
}

#[derive(Deserialize)]
pub struct PickFilter {
    pub name: String,
    pub extensions: Vec<String>,
}

/// `grant` carries byte access; `path` is for display and for locations handed
/// to the sidecar. Folder picks carry no grant.
#[derive(Serialize)]
pub struct PickedPath {
    pub path: String,
    pub grant: Option<String>,
}

/// The pick, or `None` on cancel.
/// Blocking worker hosts the modal loop so the async runtime stays free.
#[tauri::command]
pub async fn pick_path(
    window: tauri::WebviewWindow,
    options: PickOptions,
) -> Result<Option<PickedPath>, String> {
    let picked = tauri::async_runtime::spawn_blocking(move || {
        select_path(options, |options| {
            #[cfg(target_os = "linux")]
            { super::linux_path_picker::pick(&window, options) }
            #[cfg(not(target_os = "linux"))]
            { let _ = window; Ok(run_picker(options)) }
        })
    })
    .await
    .map_err(|e| e.to_string())??;
    let Some((path, mode)) = picked else {
        return Ok(None);
    };
    let grant = match mode {
        Some(mode) => Some(picked_file::mint(&path, mode)?),
        None => None,
    };
    Ok(Some(PickedPath {
        path: path.to_string_lossy().into_owned(),
        grant,
    }))
}

/// The picked path and the byte access that pick confers.
#[cfg(not(target_os = "linux"))]
fn run_picker(options: PickOptions) -> Option<PathBuf> {
    let dialog = rfd::FileDialog::new().set_title(&options.title);
    match &options.target {
        PickTarget::Folder => dialog.pick_folder(),
        PickTarget::File { filter } => match filter {
            Some(f) => dialog.add_filter(&f.name, &f.extensions).pick_file(),
            None => dialog.pick_file(),
        },
        PickTarget::Save { file_name } => dialog
            .set_file_name(file_name)
            .save_file(),
    }
}

/// One request and selection contract surrounds either native panel adapter.
fn select_path(
    options: PickOptions,
    choose: impl FnOnce(PickOptions) -> Result<Option<PathBuf>, String>,
) -> Result<Option<(PathBuf, Option<GrantMode>)>, String> {
    options.validate()?;
    let mode = options.target.grant_mode();
    let Some(path) = choose(options)? else { return Ok(None); };
    if !valid_selection(&path, mode) {
        return Err("The selected location does not match the requested file access".into());
    }
    Ok(Some((path, mode)))
}

impl PickOptions {
    fn validate(&self) -> Result<(), String> {
        let bounded = |value: &str| !value.contains('\0') && value.len() <= 1024;
        if !bounded(&self.title) { return Err("The file chooser title is invalid".into()); }
        match &self.target {
            PickTarget::Save { file_name } if !bounded(file_name) || file_name.is_empty()
                || Path::new(file_name).file_name().and_then(|name| name.to_str()) != Some(file_name.as_str()) =>
                    Err("The save file name must be one local file name".into()),
            PickTarget::File { filter: Some(filter) } if !bounded(&filter.name)
                || filter.extensions.len() > 32 || filter.extensions.iter().any(|extension|
                    extension.is_empty() || extension.len() > 64 || !extension.bytes().all(|byte|
                        byte.is_ascii_alphanumeric() || b".-_".contains(&byte))) =>
                    Err("The file chooser filter is invalid".into()),
            _ => Ok(()),
        }
    }
}

impl PickTarget {
    pub(super) fn grant_mode(&self) -> Option<GrantMode> {
        match self {
            Self::Folder => None,
            Self::File { .. } => Some(GrantMode::Read),
            Self::Save { .. } => Some(GrantMode::Write),
        }
    }
}

pub(super) fn valid_selection(path: &Path, mode: Option<GrantMode>) -> bool {
    path.is_absolute() && match mode {
        None => path.is_dir(),
        Some(GrantMode::Read) => path.is_file(),
        Some(GrantMode::Write) => path.file_name().is_some()
            && path.parent().is_some_and(|parent| parent.is_dir())
            && (!path.exists() || path.is_file()),
    }
}

#[cfg(test)]
mod tests;
