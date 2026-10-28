//! The primary native chooser stays inside the protected desktop process.
use gtk::{prelude::*, ResponseType};
use std::path::PathBuf;
use std::sync::mpsc;
use super::pick_path::{valid_selection, PickOptions, PickTarget};
type Selection = Option<PathBuf>;

/// This worker waits for GTK response signals without nesting the shell's loop.
pub(super) fn pick(window: &tauri::WebviewWindow, options: PickOptions) -> Result<Selection, String> {
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
            show(parent.upcast_ref(), options, send);
        })
        .map_err(|error| error.to_string())?;
    receive
        .recv()
        .map_err(|_| "The file chooser window closed".to_owned())?
}

fn dimensions(width: i32, height: i32) -> (i32, i32) {
    (760.min((width - 48).max(1)), 560.min((height - 48).max(1)))
}

/// The chooser dialog and the widget whose selection it returns.
struct Shown {
    #[cfg_attr(not(test), allow(dead_code))]
    dialog: gtk::Dialog,
    #[cfg_attr(not(test), allow(dead_code))]
    chooser: gtk::FileChooserWidget,
}

fn show(
    parent: &gtk::Window,
    options: PickOptions,
    send: mpsc::Sender<Result<Selection, String>>,
) -> Shown {
    // Keep actions in the bottom action area. A header containing both actions
    // and a long title can itself exceed a small monitor's logical width.
    let dialog = gtk::Dialog::builder()
        .title(options.title.as_str())
        .transient_for(parent)
        .modal(true)
        .destroy_with_parent(true)
        .use_header_bar(0)
        .build();
    let mode = options.target.grant_mode();
    let (action, accept) = match &options.target {
        PickTarget::Folder => (gtk::FileChooserAction::SelectFolder, "Open"),
        PickTarget::File { .. } => (gtk::FileChooserAction::Open, "Open"),
        PickTarget::Save { .. } => (gtk::FileChooserAction::Save, "Save"),
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
            chooser.add_filter(selected);
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
    let display = WidgetExt::display(parent);
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
            let _ = reply.send(Ok(Some(path)));
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
    Shown { dialog, chooser }
}


#[cfg(test)]
mod display_tests;

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn default_size_fits_logical_workarea_including_two_hundred_percent_scale() {
        assert_eq!(dimensions(1024, 768), (760, 560));
        assert_eq!(dimensions(512, 384), (464, 336));
        assert_eq!(dimensions(1920, 1080), (760, 560));
    }
}
