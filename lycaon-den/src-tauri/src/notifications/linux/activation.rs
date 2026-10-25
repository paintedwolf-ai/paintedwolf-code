use std::{
    cell::Cell,
    rc::Rc,
    sync::atomic::{AtomicU64, Ordering},
    time::Duration,
};

use gtk::{glib::prelude::*, prelude::*};
use tauri::{AppHandle, Emitter, Manager};
use super::super::{ActivatedPayload, PresentationOwner, ACTIVATED_EVENT};

const WAYLAND_DISPLAY_TYPE: &str = "GdkWaylandDisplay";
const FOLLOWUP_INTERVAL: Duration = Duration::from_millis(120);
const FOLLOWUP_MAX_TICKS: u8 = 6;

static ACTIVE_REQUEST: AtomicU64 = AtomicU64::new(0);

/// How the main window is raised for an activation.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Raise {
    /// A Wayland token activates the surface by itself; presenting as well
    /// would request a second token without an input serial.
    Token,
    /// X11 reads the token's timestamp and still needs the present call.
    TokenThenPresent,
    /// Unmaps an inactive visible Wayland surface before presenting when no
    /// token was supplied, avoiding a focus request with a stale seat serial.
    RemapThenPresent,
    Present,
}

/// Post-map follow-up decision for an untokenized Wayland activation.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Followup {
    /// The surface is not yet configured and painted on Wayland; keep observing.
    WaitMap,
    /// The surface is mapped, configured, and painted and no focus or modal
    /// state has superseded the request; present once.
    PresentOnce,
    /// The request succeeded or was superseded by another request, a hidden
    /// surface, or a modal/transient dialog; finish and stop.
    Finish,
}

fn raise(wayland: bool, token: bool, visible: bool, active: bool) -> Raise {
    match (wayland, token) {
        (true, true) => Raise::Token,
        (false, true) => Raise::TokenThenPresent,
        (true, false) if visible && !active => Raise::RemapThenPresent,
        (_, false) => Raise::Present,
    }
}

#[allow(clippy::too_many_arguments)]
fn followup(
    request_id: u64,
    current_request_id: u64,
    completed: bool,
    visible: bool,
    mapped: bool,
    painted: bool,
    active: bool,
    modal_or_transient_open: bool,
) -> Followup {
    if completed
        || request_id != current_request_id
        || !visible
        || active
        || modal_or_transient_open
    {
        return Followup::Finish;
    }
    if !mapped || !painted {
        return Followup::WaitMap;
    }
    Followup::PresentOnce
}

pub(crate) fn present_main_window(window: &tauri::WebviewWindow) -> Result<(), String> {
    present_native(window, None)
}

pub(super) fn activate(app: &AppHandle, session_id: Option<String>, token: Option<String>) {
    let target = app.clone();
    let _ = app.run_on_main_thread(move || {
        if target
            .try_state::<super::NativeNotifications>()
            .is_none_or(|state| state.stopped.load(Ordering::Acquire))
        {
            return;
        }
        if let Some(window) = target.get_webview_window("main") {
            let presentation = if present_native(&window, token.as_deref()).is_ok() {
                PresentationOwner::Native
            } else {
                PresentationOwner::Renderer
            };
            let _ = target.emit_to(
                "main",
                ACTIVATED_EVENT,
                ActivatedPayload {
                    session_id,
                    presentation,
                },
            );
        }
    });
}

fn present_native(window: &tauri::WebviewWindow, token: Option<&str>) -> Result<(), String> {
    let native = window.gtk_window().map_err(|error| error.to_string())?;
    let wayland = native.display().type_().name() == WAYLAND_DISPLAY_TYPE;
    let visible = native.is_visible();
    let active = native.is_active();
    // Cancel any earlier untokenized follow-up before starting this activation.
    let request_id = ACTIVE_REQUEST.fetch_add(1, Ordering::AcqRel) + 1;
    if has_visible_modal_or_transient(&native) {
        return Ok(());
    }
    // A closed workspace is hidden, not destroyed; set the token before mapping.
    match raise(wayland, token.is_some(), visible, active) {
        Raise::Token => {
            native.set_startup_id(token.unwrap_or_default());
            if !visible {
                window.show().map_err(|error| error.to_string())?;
            }
        }
        Raise::TokenThenPresent => {
            native.set_startup_id(token.unwrap_or_default());
            if !visible {
                let _ = window.show();
            }
            native.present();
        }
        Raise::RemapThenPresent => {
            native.hide();
            native.present();
            schedule_wayland_followup(&native, request_id);
        }
        Raise::Present => {
            if !visible {
                let _ = window.show();
            }
            native.present();
            if wayland && !active {
                schedule_wayland_followup(&native, request_id);
            }
        }
    }
    Ok(())
}

fn has_visible_modal_or_transient(native: &gtk::ApplicationWindow) -> bool {
    let main_window: &gtk::Window = native.upcast_ref();
    gtk::Window::list_toplevels()
        .into_iter()
        .filter_map(|widget| widget.downcast::<gtk::Window>().ok())
        .any(|window| {
            &window != main_window
                && window.is_visible()
                && (window.is_modal() || window.transient_for().as_ref() == Some(main_window))
        })
}

fn schedule_wayland_followup(native: &gtk::ApplicationWindow, request_id: u64) {
    if native.is_active() {
        return;
    }
    let completed = Rc::new(Cell::new(false));
    let completed_on_active = Rc::clone(&completed);
    let active_handler = Rc::new(Cell::new(Some(native.connect_is_active_notify(
        move |win| {
            if win.is_active() || has_visible_modal_or_transient(win) {
                completed_on_active.set(true);
            }
        },
    ))));
    let completed_on_hide = Rc::clone(&completed);
    let hide_handler = Rc::new(Cell::new(Some(native.connect_hide(move |_| {
        completed_on_hide.set(true);
    }))));
    let initial_frame = native.frame_clock().map_or(0, |clock| clock.frame_counter());
    let target = native.clone();
    let remaining = Cell::new(FOLLOWUP_MAX_TICKS);
    gtk::glib::timeout_add_local(FOLLOWUP_INTERVAL, move || {
        let current_id = ACTIVE_REQUEST.load(Ordering::Acquire);
        let visible = target.is_visible();
        let mapped = target.is_mapped()
            && target.window().is_some_and(|w| {
                w.is_visible() && !w.state().contains(gtk::gdk::WindowState::WITHDRAWN)
            });
        let painted = target
            .frame_clock()
            .is_some_and(|clock| clock.frame_counter() > initial_frame);
        let active = target.is_active();
        let modal_open = has_visible_modal_or_transient(&target);
        match followup(
            request_id,
            current_id,
            completed.get(),
            visible,
            mapped,
            painted,
            active,
            modal_open,
        ) {
            Followup::PresentOnce => {
                completed.set(true);
                if let Some(id) = active_handler.take() {
                    target.disconnect(id);
                }
                if let Some(id) = hide_handler.take() {
                    target.disconnect(id);
                }
                target.present();
                gtk::glib::ControlFlow::Break
            }
            Followup::Finish => {
                completed.set(true);
                if let Some(id) = active_handler.take() {
                    target.disconnect(id);
                }
                if let Some(id) = hide_handler.take() {
                    target.disconnect(id);
                }
                gtk::glib::ControlFlow::Break
            }
            Followup::WaitMap => {
                let left = remaining.get().saturating_sub(1);
                remaining.set(left);
                if left == 0 {
                    completed.set(true);
                    if let Some(id) = active_handler.take() {
                        target.disconnect(id);
                    }
                    if let Some(id) = hide_handler.take() {
                        target.disconnect(id);
                    }
                    gtk::glib::ControlFlow::Break
                } else {
                    gtk::glib::ControlFlow::Continue
                }
            }
        }
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn wayland_tokens_activate_without_a_second_present() {
        assert_eq!(raise(true, true, false, false), Raise::Token);
        assert_eq!(raise(true, true, true, false), Raise::Token);
        assert_eq!(raise(false, true, false, false), Raise::TokenThenPresent);
        assert_eq!(raise(true, false, false, false), Raise::Present);
        assert_eq!(raise(true, false, true, false), Raise::RemapThenPresent);
        assert_eq!(raise(true, false, true, true), Raise::Present);
        assert_eq!(raise(false, false, true, false), Raise::Present);
    }

    #[test]
    fn followup_waits_for_mapped_surface_then_presents_once() {
        assert_eq!(
            followup(7, 7, false, true, false, false, false, false),
            Followup::WaitMap
        );
        assert_eq!(
            followup(7, 7, false, true, true, false, false, false),
            Followup::WaitMap
        );
        assert_eq!(
            followup(7, 7, false, true, true, true, false, false),
            Followup::PresentOnce
        );
    }

    #[test]
    fn followup_finishes_after_focus_succeeds_even_if_focus_later_changes() {
        assert_eq!(
            followup(7, 7, false, true, true, true, true, false),
            Followup::Finish
        );
        // Once completed is latched by focus success, a later focus change to a
        // peer window or dialog never triggers another present.
        assert_eq!(
            followup(7, 7, true, true, true, true, false, false),
            Followup::Finish
        );
    }

    #[test]
    fn followup_cancels_when_superseded_hidden_or_modal_open() {
        assert_eq!(
            followup(7, 8, false, true, true, true, false, false),
            Followup::Finish
        );
        assert_eq!(
            followup(7, 7, false, false, false, false, false, false),
            Followup::Finish
        );
        assert_eq!(
            followup(7, 7, false, true, true, true, false, true),
            Followup::Finish
        );
    }
}
