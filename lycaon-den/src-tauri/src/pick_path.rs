//! App-modal native open/save panels at standard size.

use serde::{Deserialize, Serialize};

use crate::picked_file::{self, GrantMode};

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
pub async fn pick_path(options: PickOptions) -> Result<Option<PickedPath>, String> {
    let picked = tauri::async_runtime::spawn_blocking(move || run_picker(options))
        .await
        .map_err(|e| e.to_string())?;
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
fn run_picker(options: PickOptions) -> Option<(std::path::PathBuf, Option<GrantMode>)> {
    let dialog = rfd::FileDialog::new().set_title(&options.title);
    match &options.target {
        PickTarget::Folder => dialog.pick_folder().map(|p| (p, None)),
        PickTarget::File { filter } => match filter {
            Some(f) => dialog.add_filter(&f.name, &f.extensions).pick_file(),
            None => dialog.pick_file(),
        }
        .map(|p| (p, Some(GrantMode::Read))),
        PickTarget::Save { file_name } => dialog
            .set_file_name(file_name)
            .save_file()
            .map(|p| (p, Some(GrantMode::Write))),
    }
}
