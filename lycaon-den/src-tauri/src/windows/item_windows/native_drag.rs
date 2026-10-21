//! Native GTK drag for file tabs on Linux.
//!
//! Wayland gives clients no global pointer or window coordinates, so a tab that
//! leaves its strip is carried by a GTK drag instead of a window that follows
//! the pointer. The compositor names the window under the pointer, and each Den
//! window hit-tests its own tab strip in its own coordinates. The drag carries
//! a shell-minted token (see `transfer`) and only drags from this process are
//! accepted. A drop on no accepting window tears the tab off into a new
//! window that the compositor places; Escape or a compositor cancellation
//! keeps it. X11 runs the same path.

use std::sync::{LazyLock, Mutex};

use gtk::gdk;
use gtk::glib::Propagation;
use gtk::prelude::*;
use serde::Serialize;
use tauri::{AppHandle, Emitter, Manager, WebviewWindow};

use super::transfer::{Delivery, Notice, Refusal, Settled, Transfers};
use super::{
    open_item_window, valid_id, FileTabDragEvent, ItemWindowView, FILE_TAB_DRAG_EVENT,
    FILE_TAB_DROP_TARGETS,
};

const TAB_TARGET: &str = "application/x-paintedwolf-file-tab";
const FILE_TAB_TRANSFER_EVENT: &str = "file-tab-transfer";

static TRANSFERS: LazyLock<Mutex<Transfers>> = LazyLock::new(|| Mutex::new(Transfers::default()));

/// The one GTK drag this process runs at a time.
struct ActiveDrag {
    token: String,
    initiator: String,
    /// Wayland reports the release before cancelling a drop nobody accepted.
    dropped: bool,
    hovered: Option<String>,
}

static ACTIVE: LazyLock<Mutex<Option<ActiveDrag>>> = LazyLock::new(|| Mutex::new(None));

#[derive(Clone, Serialize)]
#[serde(rename_all = "camelCase")]
struct TransferSettled {
    token: String,
    outcome: &'static str,
}

fn transfers() -> std::sync::MutexGuard<'static, Transfers> {
    TRANSFERS.lock().unwrap_or_else(|poisoned| poisoned.into_inner())
}

fn active() -> std::sync::MutexGuard<'static, Option<ActiveDrag>> {
    ACTIVE.lock().unwrap_or_else(|poisoned| poisoned.into_inner())
}

fn refused(refusal: Refusal) -> String {
    format!("file tab transfer refused: {refusal:?}")
}

fn notify(app: &AppHandle, notice: Notice) {
    let outcome = match notice.settled {
        Settled::Committed => "committed",
        Settled::Cancelled => "cancelled",
    };
    let _ = app.emit_to(
        notice.initiator.as_str(),
        FILE_TAB_TRANSFER_EVENT,
        TransferSettled { token: notice.token, outcome },
    );
}

fn tab_event(phase: &str, token: &str, view: &ItemWindowView, x: f64, y: f64, transfer: bool) -> FileTabDragEvent {
    FileTabDragEvent {
        phase: phase.into(),
        drag_label: token.into(),
        project_id: view.project_id.clone(),
        root_id: view.root_id.clone().unwrap_or_default(),
        path: view.path.clone().unwrap_or_default(),
        title: view.title.clone(),
        position_x: x,
        position_y: y,
        transfer,
    }
}

fn deliver(app: &AppHandle, token: &str, delivery: Delivery) {
    match delivery {
        Delivery::Drop { recipient, x, y, view } => {
            let event = tab_event("drop", token, &view, x, y, true);
            if app.emit_to(recipient.as_str(), FILE_TAB_DRAG_EVENT, event).is_err() {
                if let Ok(notice) = transfers().give_back(token, &recipient) {
                    notify(app, notice);
                }
            }
        }
        Delivery::TearOff { view } => {
            let app = app.clone();
            let token = token.to_string();
            // Window creation re-enters the event loop; leave the GTK callback first.
            tauri::async_runtime::spawn(async move {
                let opened = open_item_window(
                    app.clone(),
                    "file".into(),
                    view.project_id.clone(),
                    None,
                    view.root_id.clone(),
                    view.path.clone(),
                    None,
                    view.title.clone(),
                    None,
                    None,
                    None,
                );
                if let Err(err) = &opened {
                    eprintln!("Could not open the torn-off file window: {err}");
                }
                let notice = transfers().torn_off(&token, opened.is_ok());
                if let Some(notice) = notice {
                    notify(&app, notice);
                }
            });
        }
    }
}

/// Starts carrying a file tab from `window` with a native drag while the
/// pointer button is still held, returning the transfer token.
#[tauri::command(rename = "begin_native_file_tab_drag")]
pub fn ipc_begin_native_file_tab_drag(
    app: AppHandle,
    window: WebviewWindow,
    project_id: String,
    root_id: String,
    path: String,
    title: String,
) -> Result<String, String> {
    if !valid_id(&project_id) || !valid_id(&root_id) || path.trim().is_empty() {
        return Err("invalid file tab".into());
    }
    let initiator = window.label().to_string();
    let token = uuid::Uuid::new_v4().simple().to_string();
    {
        let mut running = active();
        if running.is_some() {
            return Err("a file tab drag is already in progress".into());
        }
        let view = ItemWindowView {
            label: String::new(),
            title,
            view_number: 0,
            kind: "file".into(),
            project_id,
            session_id: None,
            root_id: Some(root_id),
            path: Some(path),
            stage_id: None,
        };
        transfers().begin(token.clone(), &initiator, view).map_err(refused)?;
        *running = Some(ActiveDrag {
            token: token.clone(),
            initiator,
            dropped: false,
            hovered: None,
        });
    }
    let started = token.clone();
    let source = app.clone();
    window
        .with_webview(move |webview| {
            let widget: gtk::Widget = webview.inner().upcast();
            let targets = gtk::TargetList::new(&[gtk::TargetEntry::new(TAB_TARGET, gtk::TargetFlags::SAME_APP, 0)]);
            if widget
                .drag_begin_with_coordinates(&targets, gdk::DragAction::MOVE, 1, None, -1, -1)
                .is_none()
            {
                end_active(&started);
                if let Some(notice) = transfers().cancel(&started) {
                    notify(&source, notice);
                }
            }
        })
        .map_err(|err| err.to_string())?;
    Ok(token)
}

/// The initiator published the tab's latest draft.
#[tauri::command(rename = "ready_file_tab_transfer")]
pub fn ipc_ready_file_tab_transfer(app: AppHandle, window: WebviewWindow, token: String) -> Result<(), String> {
    let delivery = transfers().ready(&token, window.label()).map_err(refused)?;
    if let Some(delivery) = delivery {
        deliver(&app, &token, delivery);
    }
    Ok(())
}

/// The initiator could not publish the draft; the tab stays.
#[tauri::command(rename = "abandon_file_tab_transfer")]
pub fn ipc_abandon_file_tab_transfer(app: AppHandle, window: WebviewWindow, token: String) -> Result<(), String> {
    let notice = transfers().abandon(&token, window.label()).map_err(refused)?;
    notify(&app, notice);
    Ok(())
}

/// The recipient opened the delivered file.
#[tauri::command(rename = "commit_file_tab_transfer")]
pub fn ipc_commit_file_tab_transfer(app: AppHandle, window: WebviewWindow, token: String) -> Result<(), String> {
    let notice = transfers().commit(&token, window.label()).map_err(refused)?;
    notify(&app, notice);
    Ok(())
}

/// The recipient could not open the delivered file.
#[tauri::command(rename = "return_file_tab_transfer")]
pub fn ipc_return_file_tab_transfer(app: AppHandle, window: WebviewWindow, token: String) -> Result<(), String> {
    let notice = transfers().give_back(&token, window.label()).map_err(refused)?;
    notify(&app, notice);
    Ok(())
}

/// Settles the transfers a destroyed window took part in.
pub(super) fn window_destroyed(app: &AppHandle, label: &str) {
    let notices = transfers().window_destroyed(label);
    for notice in notices {
        notify(app, notice);
    }
}

fn end_active(token: &str) -> Option<ActiveDrag> {
    let mut running = active();
    if running.as_ref().is_some_and(|drag| drag.token == token) {
        return running.take();
    }
    None
}

fn carries_tab(context: &gdk::DragContext) -> bool {
    context.list_targets().iter().any(|target| target.name() == TAB_TARGET)
}

/// The drop target the window published, hit-tested in its own coordinates.
fn strip_accepts(label: &str, token: &str, x: f64, y: f64) -> bool {
    let Some(project) = transfers().view_of(token).map(|view| view.project_id) else {
        return false;
    };
    let Ok(targets) = FILE_TAB_DROP_TARGETS.lock() else {
        return false;
    };
    targets.get(label).is_some_and(|target| {
        target.project_id == project
            && x >= target.x
            && x <= target.x + target.width
            && y >= target.y
            && y <= target.y + target.height
    })
}

fn set_hover(app: &AppHandle, label: &str, hover: Option<(f64, f64)>) {
    let (token, previous) = {
        let mut running = active();
        let Some(drag) = running.as_mut() else {
            return;
        };
        let previous = if hover.is_some() {
            drag.hovered.replace(label.into())
        } else if drag.hovered.as_deref() == Some(label) {
            drag.hovered.take()
        } else {
            None
        };
        (drag.token.clone(), previous)
    };
    let Some(view) = transfers().view_of(&token) else {
        return;
    };
    if let Some(previous) = previous.filter(|previous| hover.is_none() || previous != label) {
        let _ = app.emit_to(previous.as_str(), FILE_TAB_DRAG_EVENT, tab_event("leave", &token, &view, 0.0, 0.0, true));
    }
    if let Some((x, y)) = hover {
        let _ = app.emit_to(label, FILE_TAB_DRAG_EVENT, tab_event("hover", &token, &view, x, y, true));
    }
}

fn active_token() -> Option<(String, String)> {
    active().as_ref().map(|drag| (drag.token.clone(), drag.initiator.clone()))
}

/// Accepts a drop of `token` on `label`; false refuses it.
fn accept_drop(app: &AppHandle, label: &str, token: &str, x: f64, y: f64) -> bool {
    if active_token().is_some_and(|(active, initiator)| active == token && initiator == label) {
        // Back on its own strip: an ordinary reorder, nothing changes owner.
        let view = transfers().view_of(token);
        if let Some(notice) = transfers().cancel(token) {
            notify(app, notice);
        }
        if let Some(view) = view {
            let _ = app.emit_to(label, FILE_TAB_DRAG_EVENT, tab_event("drop", token, &view, x, y, false));
        }
        return true;
    }
    let Some(project) = FILE_TAB_DROP_TARGETS
        .lock()
        .ok()
        .and_then(|targets| targets.get(label).map(|target| target.project_id.clone()))
    else {
        return false;
    };
    let dropped = transfers().drop_at(token, label, &project, x, y);
    match dropped {
        Ok(Some(delivery)) => {
            deliver(app, token, delivery);
            true
        }
        Ok(None) => true,
        Err(refusal) => {
            eprintln!("{}", refused(refusal));
            false
        }
    }
}

/// The source settled the drag without a recipient.
fn source_failed(app: &AppHandle, label: &str, result: gtk::DragResult) {
    let Some(drag) = active()
        .as_ref()
        .filter(|drag| drag.initiator == label)
        .map(|drag| (drag.token.clone(), drag.dropped))
    else {
        return;
    };
    let (token, dropped) = drag;
    let tear_off = match result {
        gtk::DragResult::NoTarget => true,
        // Wayland cancels both an unaccepted drop and a compositor abort; only
        // the former reported the release first.
        gtk::DragResult::Error => dropped,
        _ => false,
    };
    if tear_off {
        let delivery = transfers().tear_off(&token);
        if let Ok(Some(delivery)) = delivery {
            deliver(app, &token, delivery);
        }
    } else if let Some(notice) = transfers().cancel(&token) {
        notify(app, notice);
    }
}

/// Makes `window`'s web view a source and destination for native tab drags.
/// Drags of other content continue to the web view's own handlers.
pub(crate) fn install(window: &WebviewWindow) {
    let app = window.app_handle().clone();
    let label = window.label().to_string();
    let result = window.with_webview(move |webview| {
        let widget: gtk::Widget = webview.inner().upcast();
        connect_source(&widget, &app, &label);
        connect_destination(&widget, &app, &label);
    });
    if let Err(err) = result {
        eprintln!("Native file tab drag is unavailable for this window: {err}");
    }
}

fn connect_source(widget: &gtk::Widget, app: &AppHandle, label: &str) {
    {
        let label = label.to_string();
        widget.connect_drag_begin(move |_, context| {
            if !carries_tab(context) || !active_token().is_some_and(|(_, initiator)| initiator == label) {
                return;
            }
            context.drag_set_icon_name("text-x-generic", 0, 0);
            context.connect_local("drop-performed", false, |_| {
                if let Some(drag) = active().as_mut() {
                    drag.dropped = true;
                }
                None
            });
        });
    }
    {
        let label = label.to_string();
        widget.connect_drag_data_get(move |widget, context, data, _, _| {
            if !carries_tab(context) {
                return;
            }
            let Some((token, initiator)) = active_token() else {
                return;
            };
            if initiator != label {
                return;
            }
            data.set(&gdk::Atom::intern(TAB_TARGET), 8, token.as_bytes());
            widget.stop_signal_emission_by_name("drag-data-get");
        });
    }
    {
        let app = app.clone();
        let label = label.to_string();
        widget.connect_drag_failed(move |_, context, result| {
            if !carries_tab(context) {
                return Propagation::Proceed;
            }
            source_failed(&app, &label, result);
            // No snap-back animation: the tab either stayed or moved.
            Propagation::Stop
        });
    }
    {
        let app = app.clone();
        let label = label.to_string();
        widget.connect_drag_end(move |_, context| {
            if !carries_tab(context) {
                return;
            }
            let Some((token, initiator)) = active_token() else {
                return;
            };
            if initiator != label {
                return;
            }
            if let Some(hovered) = end_active(&token).and_then(|drag| drag.hovered) {
                if let Some(view) = transfers().view_of(&token) {
                    let _ = app.emit_to(hovered.as_str(), FILE_TAB_DRAG_EVENT, tab_event("leave", &token, &view, 0.0, 0.0, true));
                }
            }
            // A drag that ended without a drop or failure keeps its tab.
            if let Some(notice) = transfers().cancel(&token) {
                notify(&app, notice);
            }
        });
    }
}

fn connect_destination(widget: &gtk::Widget, app: &AppHandle, label: &str) {
    let atom = gdk::Atom::intern(TAB_TARGET);
    {
        let app = app.clone();
        let label = label.to_string();
        widget.connect_drag_motion(move |_, context, x, y, time| {
            if !carries_tab(context) {
                return false;
            }
            let (x, y) = (f64::from(x), f64::from(y));
            let accepts = context.drag_get_source_widget().is_some()
                && active_token().is_some_and(|(token, _)| strip_accepts(&label, &token, x, y));
            context.drag_status(if accepts { gdk::DragAction::MOVE } else { gdk::DragAction::empty() }, time);
            set_hover(&app, &label, accepts.then_some((x, y)));
            true
        });
    }
    {
        let app = app.clone();
        let label = label.to_string();
        widget.connect_drag_leave(move |_, context, _| {
            if carries_tab(context) {
                set_hover(&app, &label, None);
            }
        });
    }
    {
        let label = label.to_string();
        let atom = atom.clone();
        widget.connect_drag_drop(move |widget, context, x, y, time| {
            if !carries_tab(context) {
                return false;
            }
            let accepts = context.drag_get_source_widget().is_some()
                && active_token().is_some_and(|(token, _)| strip_accepts(&label, &token, f64::from(x), f64::from(y)));
            if accepts {
                widget.drag_get_data(context, &atom, time);
            } else {
                context.drag_finish(false, false, time);
            }
            true
        });
    }
    {
        let app = app.clone();
        let label = label.to_string();
        widget.connect_drag_data_received(move |widget, context, x, y, data, _, time| {
            if data.target().name() != TAB_TARGET {
                return;
            }
            widget.stop_signal_emission_by_name("drag-data-received");
            let accepted = std::str::from_utf8(&data.data())
                .ok()
                .is_some_and(|token| accept_drop(&app, &label, token, f64::from(x), f64::from(y)));
            context.drag_finish(accepted, false, time);
        });
    }
}
