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
    fn pw_toplevel_drag_disarm_toplevel();
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

/// Shows `window` attached to the running drag, with the pointer at `offset`
/// within it. Returns whether the compositor now moves it with the drag.
pub(crate) fn show_attached(window: &gtk::ApplicationWindow, offset: (i32, i32)) -> bool {
    unsafe { pw_toplevel_drag_arm_toplevel(offset.0, offset.1) };
    window.show_all();
    unsafe { pw_toplevel_drag_disarm_toplevel() };
    attached()
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

    extern "C" {
        fn pw_toplevel_drag_test_without_libwayland_entry();
        fn pw_toplevel_drag_test_disabled() -> libc::c_int;
        fn wl_proxy_marshal_flags(proxy: *mut libc::c_void, opcode: u32, interface: *const libc::c_void,
                                  version: u32, flags: u32, ...) -> *mut libc::c_void;
    }

    const MARKER: &str = "PW_TOPLEVEL_DRAG_FAIL_SAFE";

    #[test]
    fn without_libwayland_entry_the_feature_is_off_and_nothing_is_forwarded() {
        if std::env::var_os(MARKER).is_some() {
            unsafe {
                pw_toplevel_drag_test_without_libwayland_entry();
                assert_eq!(super::pw_toplevel_drag_init(std::ptr::null_mut()), 0);
                assert_eq!(pw_toplevel_drag_test_disabled(), 1);
                super::arm_source();
                assert!(!super::active());
                assert!(wl_proxy_marshal_flags(std::ptr::null_mut(), 0, std::ptr::null(), 1, 0, 7u32).is_null());
                super::finish();
            }
            println!("\nfail-safe-ok");
            return;
        }
        // Process-wide state: the check runs in its own process.
        let output = std::process::Command::new(std::env::current_exe().unwrap())
            .args(["--exact", "windows::item_windows::toplevel_drag::tests::without_libwayland_entry_the_feature_is_off_and_nothing_is_forwarded", "--nocapture", "--test-threads=1"])
            .env(MARKER, "1")
            .output()
            .unwrap();
        let stdout = String::from_utf8_lossy(&output.stdout);
        assert!(output.status.success() && stdout.lines().any(|line| line == "fail-safe-ok"),
            "{}\n{stdout}\n{}", output.status, String::from_utf8_lossy(&output.stderr));
    }

    #[test]
    fn only_a_drop_tears_off_and_a_receiving_strip_wins() {
        assert_eq!(live_outcome(true, false), LiveOutcome::TornOff);
        assert_eq!(live_outcome(true, true), LiveOutcome::Delivered);
        assert_eq!(live_outcome(false, false), LiveOutcome::Reverted);
    }
}
