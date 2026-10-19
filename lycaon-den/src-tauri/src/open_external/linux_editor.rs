//! Linux editor launch, including exact-document grants for Flatpak apps.

use std::path::Path;

use super::{spawn_argv_owned, substitute_tokens};
use crate::detect_editors::{catalog::EditorCatalogEntry, linux_desktop};

pub(super) fn spawn(entry: &EditorCatalogEntry, path: &str, line: &str) -> Result<(), String> {
    let folder = Path::new(path).is_dir();
    let templates = if folder {
        entry.folder_cli_args
    } else {
        entry.cli_args
    };
    if !entry.linux_cli.is_empty() && which::which(entry.linux_cli).is_ok() {
        return spawn_argv_owned(entry.linux_cli, &substitute_tokens(templates, path, line));
    }
    let desktop = linux_desktop::find_entry(
        entry.linux_desktop_ids,
        &linux_desktop::system_application_directories(),
        &linux_desktop::program_runnable,
    )
    .ok_or_else(|| format!("{} is not installed", entry.display_name))?;
    let document = desktop
        .command
        .flatpak_app()?
        .map(|app| linux_desktop::documents::export(Path::new(path), &app))
        .transpose()?;
    let selected_path = document
        .as_ref()
        .map(|document| document.path.as_path())
        .unwrap_or_else(|| Path::new(path));
    let selected_path = selected_path
        .to_str()
        .ok_or("The editor path is not valid UTF-8.")?;
    let args = substitute_tokens(templates, selected_path, line);
    let argv = desktop.command.arguments(&args, document.is_some())?;
    spawn_argv_owned(&desktop.command.program, &argv)?;
    if let Some(document) = document {
        document.retain();
    }
    Ok(())
}
