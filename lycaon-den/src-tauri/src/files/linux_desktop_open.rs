//! Opening URIs, files and directories through the desktop session on Linux.
//!
//! The OpenURI portal receives the request with the window's identity so the
//! chooser or the launched application is parented to it. Without a running
//! portal the default handler from the desktop's MIME database launches
//! directly, and `xdg-open` remains the last resort for sessions without GIO
//! application support.

use std::path::Path;
use std::process::Command;
use std::sync::{mpsc, Mutex, PoisonError};
use std::time::Duration;

use gtk::glib;
use gtk::glib::variant::Handle;
use gtk::prelude::*;
use gtk::{gdk, gio};

const PORTAL_BUS_NAME: &str = "org.freedesktop.portal.Desktop";
const SESSION_CONNECT_TIMEOUT: Duration = Duration::from_millis(500);
const PORTAL_OBJECT_PATH: &str = "/org/freedesktop/portal/desktop";
const OPEN_URI_INTERFACE: &str = "org.freedesktop.portal.OpenURI";
const FILE_MANAGER_BUS_NAME: &str = "org.freedesktop.FileManager1";
const FILE_MANAGER_OBJECT_PATH: &str = "/org/freedesktop/FileManager1";
const CALL_TIMEOUT_MS: i32 = 5_000;
const IDENTITY_TIMEOUT: Duration = Duration::from_secs(2);

/// Errors meaning no service answered the request: no owner for the name, no
/// such object, interface or method, a backend that exited before replying, or
/// one that did not reply in time. The portal methods return a request handle
/// before any chooser appears and `ShowItems` only activates the file manager,
/// so none of these is a person's decision. Only these let a request continue
/// through another service.
const UNAVAILABLE_ERRORS: [&str; 8] = [
    "org.freedesktop.DBus.Error.ServiceUnknown",
    "org.freedesktop.DBus.Error.NameHasNoOwner",
    "org.freedesktop.DBus.Error.UnknownObject",
    "org.freedesktop.DBus.Error.UnknownInterface",
    "org.freedesktop.DBus.Error.UnknownMethod",
    "org.freedesktop.DBus.Error.NoReply",
    "org.freedesktop.DBus.Error.Timeout",
    "org.freedesktop.DBus.Error.TimedOut",
];

/// Why a desktop service did not complete a request.
#[derive(Debug, PartialEq, Eq)]
enum ServiceError {
    /// No service answered the request; another path may handle it.
    Unavailable,
    /// The service refused or failed; no other path may bypass it.
    Failed(String),
}

impl ServiceError {
    fn from_error_name(name: Option<&str>, detail: impl Into<String>) -> Self {
        match name {
            Some(name) if UNAVAILABLE_ERRORS.contains(&name) => Self::Unavailable,
            Some(name) => Self::Failed(format!("{name}: {}", detail.into())),
            None => Self::Failed(detail.into()),
        }
    }

    fn from_call(error: &glib::Error) -> Self {
        let name = remote_error_name(error);
        if name.is_none() && error.matches(gio::IOErrorEnum::TimedOut) {
            return Self::Unavailable;
        }
        Self::from_error_name(name.as_deref(), error.message())
    }
}

/// The D-Bus error name a reply carried, absent for errors raised locally.
fn remote_error_name(error: &glib::Error) -> Option<String> {
    use glib::translate::{from_glib_full, ToGlibPtr};
    let name: Option<glib::GString> = unsafe {
        from_glib_full(gio::ffi::g_dbus_error_get_remote_error(
            error.to_glib_none().0,
        ))
    };
    name.map(|name| name.to_string())
}

/// The service that handled an open request, for diagnostics.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Via {
    Portal,
    FileManager,
    DefaultHandler,
    XdgOpen,
}

impl std::fmt::Display for Via {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(match self {
            Via::Portal => "desktop portal",
            Via::FileManager => "file manager",
            Via::DefaultHandler => "default handler",
            Via::XdgOpen => "xdg-open",
        })
    }
}

/// The window identity the portal parents its dialogs to, in the
/// `x11:<xid>` or `wayland:<handle>` form the OpenURI interface defines.
/// Empty when the display server exposes none within the budget.
pub struct ParentWindow {
    identity: String,
    exported: Option<tauri::WebviewWindow>,
}

impl ParentWindow {
    pub fn none() -> Self {
        Self {
            identity: String::new(),
            exported: None,
        }
    }

    pub fn of(window: &tauri::WebviewWindow) -> Self {
        let (sender, receiver) = mpsc::channel();
        let target = window.clone();
        if window
            .run_on_main_thread(move || identify_on_main_thread(&target, sender))
            .is_err()
        {
            return Self::none();
        }
        match receiver.recv_timeout(IDENTITY_TIMEOUT) {
            Ok(identity) if identity.starts_with("wayland:") => Self {
                identity,
                exported: Some(window.clone()),
            },
            Ok(identity) => Self {
                identity,
                exported: None,
            },
            Err(_) => Self::none(),
        }
    }

    pub fn identity(&self) -> &str {
        &self.identity
    }
}

impl Drop for ParentWindow {
    fn drop(&mut self) {
        if let Some(window) = self.exported.take() {
            let exported = window.clone();
            let _ = window.run_on_main_thread(move || unexport_on_main_thread(&exported));
        }
    }
}

fn gdk_window(window: &tauri::WebviewWindow) -> Option<gdk::Window> {
    window.gtk_window().ok()?.window()
}

fn identify_on_main_thread(window: &tauri::WebviewWindow, sender: mpsc::Sender<String>) {
    let Some(surface) = gdk_window(window) else {
        let _ = sender.send(String::new());
        return;
    };
    match surface.display().type_().name() {
        "GdkX11Display" => {
            let identity = surface
                .downcast_ref::<gdkx11::X11Window>()
                .map(|x11| format!("x11:{:x}", x11.xid()))
                .unwrap_or_default();
            let _ = sender.send(identity);
        }
        "GdkWaylandDisplay" => {
            if !export_wayland_handle(&surface, sender.clone()) {
                let _ = sender.send(String::new());
            }
        }
        _ => {
            let _ = sender.send(String::new());
        }
    }
}

// The compositor answers the export asynchronously on the GTK main loop; the
// callback hands the handle to the waiting worker.
fn export_wayland_handle(surface: &gdk::Window, sender: mpsc::Sender<String>) -> bool {
    use glib::translate::ToGlibPtr;
    unsafe extern "C" fn exported(
        _window: *mut gdk_wayland_sys::GdkWaylandWindow,
        handle: *const std::os::raw::c_char,
        user_data: *mut libc::c_void,
    ) {
        let sender = &*(user_data as *const mpsc::Sender<String>);
        let handle = std::ffi::CStr::from_ptr(handle).to_string_lossy();
        let _ = sender.send(format!("wayland:{handle}"));
    }
    unsafe extern "C" fn release(user_data: *mut libc::c_void) {
        drop(Box::from_raw(user_data as *mut mpsc::Sender<String>));
    }
    let user_data = Box::into_raw(Box::new(sender)) as *mut libc::c_void;
    let window: *mut gdk::ffi::GdkWindow = surface.to_glib_none().0;
    let started = unsafe {
        gdk_wayland_sys::gdk_wayland_window_export_handle(
            window as *mut gdk_wayland_sys::GdkWaylandWindow,
            Some(exported),
            user_data,
            Some(release),
        )
    };
    if started == glib::ffi::GFALSE {
        unsafe { release(user_data) };
        return false;
    }
    true
}

fn unexport_on_main_thread(window: &tauri::WebviewWindow) {
    use glib::translate::ToGlibPtr;
    let Some(surface) = gdk_window(window) else {
        return;
    };
    if surface.display().type_().name() != "GdkWaylandDisplay" {
        return;
    }
    let window: *mut gdk::ffi::GdkWindow = surface.to_glib_none().0;
    unsafe {
        gdk_wayland_sys::gdk_wayland_window_unexport_handle(
            window as *mut gdk_wayland_sys::GdkWaylandWindow,
        );
    }
}

/// One connection serves every request; its bounded handshake runs on a
/// private context so a command on the main thread never dispatches the
/// shell's loop, and a closed connection is replaced on the next request.
fn session_bus() -> Result<gio::DBusConnection, String> {
    static BUS: Mutex<Option<gio::DBusConnection>> = Mutex::new(None);
    let mut bus = BUS.lock().unwrap_or_else(PoisonError::into_inner);
    if let Some(open) = bus.as_ref().filter(|connection| !connection.is_closed()) {
        return Ok(open.clone());
    }
    let connection = crate::linux_desktop::bus::connect_session(
        &glib::MainContext::new(),
        SESSION_CONNECT_TIMEOUT,
    )
    .map_err(|error| format!("The desktop session bus is unavailable: {error}"))?;
    *bus = Some(connection.clone());
    Ok(connection)
}

/// Whether a portal service currently owns its bus name on this session.
pub fn portal_running() -> bool {
    session_bus().is_ok_and(|bus| portal_owned(&bus))
}

/// A portal counts only while a service owns its bus name; a merely
/// activatable portal would start into a session that has no backend for it.
fn portal_owned(bus: &gio::DBusConnection) -> bool {
    bus.call_sync(
        Some("org.freedesktop.DBus"),
        "/org/freedesktop/DBus",
        "org.freedesktop.DBus",
        "NameHasOwner",
        Some(&(PORTAL_BUS_NAME,).to_variant()),
        Some(glib::VariantTy::new("(b)").expect("static signature")),
        gio::DBusCallFlags::NONE,
        CALL_TIMEOUT_MS,
        gio::Cancellable::NONE,
    )
    .ok()
    .and_then(|reply| reply.child_value(0).get::<bool>())
    .unwrap_or(false)
}

fn portal_options() -> glib::Variant {
    glib::VariantDict::new(None).end()
}

fn portal_open_uri(bus: &gio::DBusConnection, parent: &str, uri: &str) -> Result<(), ServiceError> {
    bus.call_sync(
        Some(PORTAL_BUS_NAME),
        PORTAL_OBJECT_PATH,
        OPEN_URI_INTERFACE,
        "OpenURI",
        Some(&glib::Variant::tuple_from_iter([
            parent.to_variant(),
            uri.to_variant(),
            portal_options(),
        ])),
        Some(glib::VariantTy::new("(o)").expect("static signature")),
        gio::DBusCallFlags::NONE,
        CALL_TIMEOUT_MS,
        gio::Cancellable::NONE,
    )
    .map(|_| ())
    .map_err(|error| ServiceError::from_call(&error))
}

/// `OpenFile` and `OpenDirectory` take the entry as a file descriptor, so the
/// portal opens exactly the entry this process could open. The request goes
/// out as a message because the reply carries no descriptors back.
fn portal_open_fd(
    bus: &gio::DBusConnection,
    method: &str,
    parent: &str,
    path: &Path,
) -> Result<(), ServiceError> {
    let file = std::fs::File::open(path)
        .map_err(|error| ServiceError::Failed(format!("{}: {error}", path.display())))?;
    let fd_list = gio::UnixFDList::new();
    let index = fd_list
        .append(file)
        .map_err(|error| ServiceError::Failed(error.to_string()))?;
    let message = gio::DBusMessage::new_method_call(
        Some(PORTAL_BUS_NAME),
        PORTAL_OBJECT_PATH,
        Some(OPEN_URI_INTERFACE),
        method,
    );
    message.set_body(&glib::Variant::tuple_from_iter([
        parent.to_variant(),
        Handle(index).to_variant(),
        portal_options(),
    ]));
    message.set_unix_fd_list(Some(&fd_list));
    let (reply, _serial) = bus
        .send_message_with_reply_sync(
            &message,
            gio::DBusSendMessageFlags::NONE,
            CALL_TIMEOUT_MS,
            gio::Cancellable::NONE,
        )
        .map_err(|error| ServiceError::from_call(&error))?;
    // A method return has nothing to convert; only an error reply names a cause.
    if reply.message_type() == gio::DBusMessageType::Error {
        return Err(ServiceError::from_error_name(
            reply.error_name().as_deref(),
            "the portal refused the request",
        ));
    }
    Ok(())
}

fn launch_default_handler(uri: &str) -> Result<(), String> {
    gio::AppInfo::launch_default_for_uri(uri, gio::AppLaunchContext::NONE)
        .map_err(|error| format!("No application is registered for this item: {error}"))
}

fn xdg_open(target: &str) -> Result<(), String> {
    let status = Command::new("xdg-open")
        .arg(target)
        .status()
        .map_err(|error| format!("xdg-open: {error}"))?;
    if status.success() {
        Ok(())
    } else {
        Err(format!("xdg-open exited with {status}"))
    }
}

fn file_uri(path: &Path) -> String {
    gio::File::for_path(path).uri().to_string()
}

/// Runs the portal request when a portal is up, otherwise the desktop's own
/// handler; the failure of the last alternative names what the person can fix.
/// A portal that refuses is final, so its decision is never bypassed.
fn through_portal(
    parent: &ParentWindow,
    portal: impl FnOnce(&gio::DBusConnection, &str) -> Result<(), ServiceError>,
    uri: &str,
) -> Result<Via, String> {
    if let Ok(bus) = session_bus() {
        if portal_owned(&bus) {
            if let Some(outcome) = settled(
                portal(&bus, parent.identity()),
                Via::Portal,
                "The desktop portal",
            ) {
                return outcome;
            }
        }
    }
    match launch_default_handler(uri) {
        Ok(()) => Ok(Via::DefaultHandler),
        Err(handler) => xdg_open(uri)
            .map(|()| Via::XdgOpen)
            .map_err(|open| format!("{handler}; {open}")),
    }
}

/// Opens a web or mail URI with the default application.
pub fn open_uri(parent: &ParentWindow, uri: &str) -> Result<Via, String> {
    through_portal(
        parent,
        |bus, identity| portal_open_uri(bus, identity, uri),
        uri,
    )
}

/// Opens a local file with the application registered for its type.
pub fn open_file(parent: &ParentWindow, path: &Path) -> Result<Via, String> {
    let uri = file_uri(path);
    through_portal(
        parent,
        |bus, identity| portal_open_fd(bus, "OpenFile", identity, path),
        &uri,
    )
}

/// Opens a directory in the file manager.
pub fn open_directory(parent: &ParentWindow, path: &Path) -> Result<Via, String> {
    let uri = file_uri(path);
    through_portal(
        parent,
        |bus, identity| portal_open_fd(bus, "OpenDirectory", identity, path),
        &uri,
    )
}

/// The final outcome of a service request, or `None` when no service
/// implements it and the next alternative should run.
fn settled(
    result: Result<(), ServiceError>,
    via: Via,
    service: &str,
) -> Option<Result<Via, String>> {
    match result {
        Ok(()) => Some(Ok(via)),
        Err(ServiceError::Unavailable) => None,
        Err(ServiceError::Failed(reason)) => {
            Some(Err(format!("{service} did not open the item: {reason}")))
        }
    }
}

/// Selects the entry in the file manager, falling back to opening its parent
/// directory where no file manager implements the selection interface.
pub fn show_item(parent: &ParentWindow, path: &Path) -> Result<Via, String> {
    if let Ok(bus) = session_bus() {
        let items = vec![file_uri(path)];
        let shown = bus.call_sync(
            Some(FILE_MANAGER_BUS_NAME),
            FILE_MANAGER_OBJECT_PATH,
            FILE_MANAGER_BUS_NAME,
            "ShowItems",
            Some(&(items, "").to_variant()),
            None,
            gio::DBusCallFlags::NONE,
            CALL_TIMEOUT_MS,
            gio::Cancellable::NONE,
        );
        let shown = shown
            .map(|_| ())
            .map_err(|error| ServiceError::from_call(&error));
        if let Some(outcome) = settled(shown, Via::FileManager, "The file manager") {
            return outcome;
        }
    }
    let directory = if path.is_dir() {
        path
    } else {
        path.parent().unwrap_or(path)
    };
    open_directory(parent, directory)
}

/// Opens a URI with the browser registered for `https`, whatever its scheme.
pub fn open_in_default_browser(uri: &str) -> Result<(), String> {
    let browser = gio::AppInfo::default_for_uri_scheme("https")
        .ok_or_else(|| "No default browser is configured.".to_string())?;
    browser
        .launch_uris(&[uri], gio::AppLaunchContext::NONE)
        .map_err(|error| format!("The default browser could not open the item: {error}"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn file_uris_encode_reserved_and_unicode_bytes() {
        assert_eq!(
            file_uri(Path::new("/repo/a #?%é.txt")),
            "file:///repo/a%20%23%3F%25%C3%A9.txt"
        );
    }

    #[test]
    fn missing_parent_identity_is_empty() {
        assert_eq!(ParentWindow::none().identity(), "");
    }

    #[test]
    fn fd_requests_refuse_missing_entries_before_touching_the_bus() {
        let Ok(bus) = session_bus() else {
            return;
        };
        let missing = Path::new("/nonexistent/painted-wolf/entry");
        let Err(ServiceError::Failed(error)) = portal_open_fd(&bus, "OpenFile", "", missing) else {
            panic!("a missing entry must fail without reaching the portal");
        };
        assert!(error.contains("/nonexistent/painted-wolf/entry"), "{error}");
    }

    #[test]
    fn only_unanswered_requests_continue_to_another_path() {
        for name in UNAVAILABLE_ERRORS {
            let error = ServiceError::from_error_name(Some(name), "absent");
            assert_eq!(error, ServiceError::Unavailable, "{name}");
            assert_eq!(
                settled(Err(error), Via::Portal, "The desktop portal"),
                None,
                "{name}"
            );
        }
    }

    #[test]
    fn refusals_are_final() {
        for name in [
            Some("org.freedesktop.portal.Error.NotAllowed"),
            Some("org.freedesktop.DBus.Error.AccessDenied"),
            Some("org.freedesktop.DBus.Error.Failed"),
            None,
        ] {
            let error = ServiceError::from_error_name(name, "refused");
            assert!(matches!(error, ServiceError::Failed(_)), "{name:?}");
            let outcome = settled(Err(error), Via::FileManager, "The file manager");
            assert!(
                matches!(outcome, Some(Err(ref reason)) if reason.contains("refused")),
                "{name:?}: {outcome:?}"
            );
        }
        assert_eq!(
            settled(Ok(()), Via::Portal, "The desktop portal"),
            Some(Ok(Via::Portal))
        );
    }

    #[test]
    fn reply_error_names_are_read_from_the_error() {
        let remote = |name: &str| -> glib::Error {
            use glib::translate::{from_glib_full, ToGlibPtr};
            unsafe {
                from_glib_full(gio::ffi::g_dbus_error_new_for_dbus_error(
                    name.to_glib_none().0,
                    "reply".to_glib_none().0,
                ))
            }
        };
        assert_eq!(
            ServiceError::from_call(&remote("org.freedesktop.DBus.Error.UnknownMethod")),
            ServiceError::Unavailable
        );
        assert_eq!(
            ServiceError::from_call(&remote("org.freedesktop.DBus.Error.Timeout")),
            ServiceError::Unavailable
        );
        assert!(matches!(
            ServiceError::from_call(&remote("org.freedesktop.portal.Error.NotAllowed")),
            ServiceError::Failed(reason) if reason.starts_with("org.freedesktop.portal.Error.NotAllowed")
        ));
        let timeout = glib::Error::new(gio::IOErrorEnum::TimedOut, "timed out");
        assert_eq!(ServiceError::from_call(&timeout), ServiceError::Unavailable);
        let closed = glib::Error::new(gio::IOErrorEnum::Closed, "closed");
        assert!(matches!(
            ServiceError::from_call(&closed),
            ServiceError::Failed(_)
        ));
    }
}
