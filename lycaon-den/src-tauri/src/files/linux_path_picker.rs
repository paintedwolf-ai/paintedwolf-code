//! The primary native chooser stays inside the protected desktop process.
use gtk::{prelude::*, ResponseType};
use std::path::PathBuf;
use std::sync::mpsc;
use super::pick_path::{PickOptions, PickTarget};
use super::picked_file::GrantMode;
type Selection = Option<(PathBuf, Option<GrantMode>)>;

/// This worker waits for GTK response signals without nesting the shell's loop.
pub(super) fn pick(window: &tauri::WebviewWindow, options: PickOptions) -> Result<Selection, String> {
    validate(&options)?;
    let (send, receive) = mpsc::channel();
    let target = window.clone();
    window
        .run_on_main_thread(move || {
            let parent = match target.gtk_window() {
                Ok(parent) => parent,
                Err(error) => {
                    let _ = send.send(Err(error.to_string()));
                    return;
                }
            };
            show(&parent, options, send);
        })
        .map_err(|error| error.to_string())?;
    receive
        .recv()
        .map_err(|_| "The file chooser window closed".to_owned())?
}

fn dimensions(width: i32, height: i32) -> (i32, i32) {
    (760.min((width - 48).max(1)), 560.min((height - 48).max(1)))
}

fn show(
    parent: &gtk::ApplicationWindow,
    options: PickOptions,
    send: mpsc::Sender<Result<Selection, String>>,
) {
    // Keep actions in the bottom action area. A header containing both actions
    // and a long title can itself exceed a small monitor's logical width.
    let dialog = gtk::Dialog::builder()
        .title(&options.title)
        .transient_for(parent)
        .modal(true)
        .destroy_with_parent(true)
        .use_header_bar(0)
        .build();
    let (action, mode, accept) = match &options.target {
        PickTarget::Folder => (gtk::FileChooserAction::SelectFolder, None, "Open"),
        PickTarget::File { .. } => (gtk::FileChooserAction::Open, Some(GrantMode::Read), "Open"),
        PickTarget::Save { .. } => (gtk::FileChooserAction::Save, Some(GrantMode::Write), "Save"),
    };
    dialog.add_buttons(&[("Cancel", ResponseType::Cancel), (accept, ResponseType::Accept)]);
    dialog.set_default_response(ResponseType::Accept);
    let chooser = gtk::FileChooserWidget::new(action);
    chooser.set_local_only(true);
    chooser.set_select_multiple(false);
    match &options.target {
        PickTarget::Save { file_name } => {
            chooser.set_current_name(file_name);
            chooser.set_do_overwrite_confirmation(true);
        },
        PickTarget::File { filter: Some(filter) } => {
            let selected = gtk::FileFilter::new();
            selected.set_name(Some(&filter.name));
            for extension in &filter.extensions { selected.add_pattern(&format!("*.{extension}")); }
            chooser.add_filter(&selected);
        },
        _ => {},
    }
    let viewport = gtk::ScrolledWindow::new(gtk::Adjustment::NONE, gtk::Adjustment::NONE);
    viewport.set_policy(gtk::PolicyType::Automatic, gtk::PolicyType::Automatic);
    viewport.set_min_content_width(0);
    viewport.set_min_content_height(0);
    viewport.set_propagate_natural_width(false);
    viewport.set_propagate_natural_height(false);
    viewport.add(&chooser);
    dialog.content_area().pack_start(&viewport, true, true, 0);
    let display = gtk::prelude::WidgetExt::display(parent);
    let monitor = parent
        .window()
        .and_then(|surface| display.monitor_at_window(&surface))
        .or_else(|| display.primary_monitor());
    let (width, height) = monitor
        .map(|monitor| {
            let area = monitor.workarea();
            dimensions(area.width(), area.height())
        })
        .unwrap_or((464, 336));
    dialog.set_default_size(width, height);
    let selected = chooser.clone();
    let reply = send.clone();
    dialog.connect_response(move |dialog, response| {
        if response == ResponseType::Accept {
            let Some(path) = selected.filename().filter(|path| valid_selection(path, mode)) else {
                return;
            };
            let _ = reply.send(Ok(Some((path, mode))));
        } else {
            let _ = reply.send(Ok(None));
        }
        // All results were read above; no callback queries this widget after destruction.
        unsafe {
            dialog.destroy();
        }
    });
    dialog.connect_destroy(move |_| {
        let _ = send.send(Ok(None));
    });
    let weak_dialog = dialog.downgrade();
    chooser.connect_file_activated(move |_| {
        if let Some(dialog) = weak_dialog.upgrade() {
            dialog.response(ResponseType::Accept);
        }
    });
    dialog.show_all();
}


fn validate(options: &PickOptions) -> Result<(), String> {
    let bounded = |value: &str| !value.contains('\0') && value.len() <= 1024;
    if !bounded(&options.title) { return Err("The file chooser title is invalid".into()); }
    match &options.target {
        PickTarget::Save { file_name } if !bounded(file_name) || file_name.is_empty()
            || PathBuf::from(file_name).file_name().and_then(|name| name.to_str()) != Some(file_name.as_str()) =>
                Err("The save file name must be one local file name".into()),
        PickTarget::File { filter: Some(filter) } if !bounded(&filter.name)
            || filter.extensions.len() > 32 || filter.extensions.iter().any(|extension|
                extension.is_empty() || extension.len() > 64 || !extension.bytes().all(|byte|
                    byte.is_ascii_alphanumeric() || b".-_".contains(&byte))) =>
                Err("The file chooser filter is invalid".into()),
        _ => Ok(()),
    }
}

fn valid_selection(path: &std::path::Path, mode: Option<GrantMode>) -> bool {
    path.is_absolute() && match mode {
        None => path.is_dir(),
        Some(GrantMode::Read) => path.is_file(),
        Some(GrantMode::Write) => path.file_name().is_some()
            && path.parent().is_some_and(|parent| parent.is_dir())
            && (!path.exists() || path.is_file()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn default_size_fits_logical_workarea_including_two_hundred_percent_scale() {
        assert_eq!(dimensions(1024, 768), (760, 560));
        assert_eq!(dimensions(512, 384), (464, 336));
        assert_eq!(dimensions(1920, 1080), (760, 560));
    }
    #[test]
    fn selections_retain_folder_read_and_save_authority() {
        let root = crate::test_support::TempDir::new("protected-picker");
        let file = root.join("notes é.txt");
        std::fs::write(&file, b"fixture").unwrap();
        let future = root.join("new.txt");
        assert!(valid_selection(root.path(), None));
        assert!(!valid_selection(&file, None));
        assert!(valid_selection(&file, Some(GrantMode::Read)));
        assert!(!valid_selection(&future, Some(GrantMode::Read)));
        assert!(valid_selection(&future, Some(GrantMode::Write)));
        assert!(valid_selection(&file, Some(GrantMode::Write)));
        assert!(!valid_selection(root.path(), Some(GrantMode::Write)));
        assert!(!valid_selection(std::path::Path::new("relative"), None));
    }
    #[test]
    fn options_refuse_nonlocal_save_names_and_filter_patterns() {
        let options = |target| PickOptions { title: "Choose location".into(), target };
        assert!(validate(&options(PickTarget::Save { file_name: "notes é.txt".into() })).is_ok());
        for file_name in ["", "../escape", "/absolute", "nul\0.txt"] {
            assert!(validate(&options(PickTarget::Save { file_name: file_name.into() })).is_err());
        }
        let filter = |extension: &str| options(PickTarget::File { filter: Some(super::super::pick_path::PickFilter {
            name: "Files".into(), extensions: vec![extension.into()],
        }) });
        assert!(validate(&filter("tar.gz")).is_ok());
        for extension in ["", "*", "a/b", "x?", "x\0"] { assert!(validate(&filter(extension)).is_err()); }
    }

}
