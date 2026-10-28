use gtk::{prelude::*, ResponseType};
use std::path::PathBuf;
use std::sync::mpsc;

/// This worker waits for GTK response signals without nesting the shell's loop.
pub(super) fn pick(window: &tauri::WebviewWindow, title: &str) -> Result<Option<PathBuf>, String> {
    let (send, receive) = mpsc::channel();
    let target = window.clone();
    let title = title.to_owned();
    window
        .run_on_main_thread(move || {
            let parent = match target.gtk_window() {
                Ok(parent) => parent,
                Err(error) => {
                    let _ = send.send(Err(error.to_string()));
                    return;
                }
            };
            show(&parent, &title, send);
        })
        .map_err(|error| error.to_string())?;
    receive
        .recv()
        .map_err(|_| "The folder picker window closed".to_owned())?
}

fn dimensions(width: i32, height: i32) -> (i32, i32) {
    (760.min((width - 48).max(1)), 560.min((height - 48).max(1)))
}

fn show(
    parent: &gtk::ApplicationWindow,
    title: &str,
    send: mpsc::Sender<Result<Option<PathBuf>, String>>,
) {
    // Keep actions in the bottom action area. A header containing both actions
    // and a long title can itself exceed a small monitor's logical width.
    let dialog = gtk::Dialog::builder()
        .title(title)
        .transient_for(parent)
        .modal(true)
        .destroy_with_parent(true)
        .use_header_bar(0)
        .build();
    dialog.add_buttons(&[
        ("Cancel", ResponseType::Cancel),
        ("Open", ResponseType::Accept),
    ]);
    dialog.set_default_response(ResponseType::Accept);
    let chooser = gtk::FileChooserWidget::new(gtk::FileChooserAction::SelectFolder);
    chooser.set_local_only(true);
    chooser.set_select_multiple(false);
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
            let Some(path) = selected.filename().filter(|path| path.is_dir()) else {
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
}
