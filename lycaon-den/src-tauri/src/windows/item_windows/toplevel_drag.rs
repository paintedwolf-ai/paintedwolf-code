//! Moves a torn-off file tab's window with the pointer on Wayland compositors
//! that offer `xdg_toplevel_drag_v1` (see `toplevel_drag/toplevel_drag.c`).
//!
//! With a toplevel attached, a release ends in `drop-performed` wherever it
//! lands, and Escape cancels without it, so the source can tell them apart.
//! Mutter then cancels an unreceived drop while KWin finishes it, so the
//! outcome comes from the drop and from whether a Den strip received the tab,
//! never from GTK's drag result.

use std::ffi::c_void;

use gtk::gdk;
use gtk::glib::translate::ToGlibPtr;
use gtk::prelude::*;

extern "C" {
    fn pw_toplevel_drag_init(display: *mut c_void) -> libc::c_int;
    fn pw_toplevel_drag_arm_source();
    fn pw_toplevel_drag_arm_toplevel(x_offset: i32, y_offset: i32);
    fn pw_toplevel_drag_active() -> libc::c_int;
    fn pw_toplevel_drag_attached() -> libc::c_int;
    fn pw_toplevel_drag_finish();
}

/// Whether drags on `display` can carry a live window. Binds the compositor
/// global once per process.
pub(crate) fn supported(display: &gdk::Display) -> bool {
    if display.type_().name() != "GdkWaylandDisplay" {
        return false;
    }
    let raw: *mut gdk::ffi::GdkDisplay = display.to_glib_none().0;
    // SAFETY: the display is a live GdkWaylandDisplay owned by GTK on this thread.
    let wl_display = unsafe { gdk_wayland_sys::gdk_wayland_display_get_wl_display(raw.cast()) };
    !wl_display.is_null() && unsafe { pw_toplevel_drag_init(wl_display) } == 1
}

/// The next drag GTK starts on this thread gets a toplevel drag.
pub(crate) fn arm_source() {
    unsafe { pw_toplevel_drag_arm_source() }
}

/// The next window GTK maps on this thread is attached to the running drag,
/// with the pointer at `offset` within it.
pub(crate) fn arm_window(offset: (i32, i32)) {
    unsafe { pw_toplevel_drag_arm_toplevel(offset.0, offset.1) }
}

/// A toplevel drag was created for the running GTK drag.
pub(crate) fn active() -> bool {
    unsafe { pw_toplevel_drag_active() == 1 }
}

/// A window is moving with the running drag.
pub(crate) fn attached() -> bool {
    unsafe { pw_toplevel_drag_attached() == 1 }
}

/// Releases the toplevel drag once GTK reports the drag ended.
pub(crate) fn finish() {
    unsafe { pw_toplevel_drag_finish() }
}

/// How a drag that carried a live window settles.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum LiveOutcome {
    /// A Den strip took the tab; the carried window closes.
    Delivered,
    /// Released over no Den strip; the carried window keeps the tab.
    TornOff,
    /// Escape or a compositor abort; the carried window closes and the tab stays.
    Reverted,
}

pub(crate) fn live_outcome(dropped: bool, received: bool) -> LiveOutcome {
    match (dropped, received) {
        (_, true) => LiveOutcome::Delivered,
        (true, false) => LiveOutcome::TornOff,
        (false, false) => LiveOutcome::Reverted,
    }
}

#[cfg(test)]
mod tests {
    use super::{live_outcome, LiveOutcome};

    #[test]
    fn only_a_drop_tears_off_and_a_receiving_strip_wins() {
        assert_eq!(live_outcome(true, false), LiveOutcome::TornOff);
        assert_eq!(live_outcome(true, true), LiveOutcome::Delivered);
        assert_eq!(live_outcome(false, false), LiveOutcome::Reverted);
    }
}
