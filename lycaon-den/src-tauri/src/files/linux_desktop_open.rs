//! Ordinary desktop associations run from the protected shell, without a portal proxy.
use std::path::Path;
use std::process::Command;
use std::sync::{Mutex, PoisonError};
use std::time::Duration;

use gtk::{gio, glib};
use gtk::prelude::*;

const FILE_MANAGER: &str = "org.freedesktop.FileManager1";
const CALL_TIMEOUT_MS: i32 = 5_000;
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

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Via { FileManager, DefaultHandler, XdgOpen }

impl std::fmt::Display for Via {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(match self {
            Self::FileManager => "file manager",
            Self::DefaultHandler => "default handler",
            Self::XdgOpen => "xdg-open",
        })
    }
}

fn session_bus() -> Result<gio::DBusConnection, String> {
    static BUS: Mutex<Option<gio::DBusConnection>> = Mutex::new(None);
    let mut bus = BUS.lock().unwrap_or_else(PoisonError::into_inner);
    if let Some(open) = bus.as_ref().filter(|connection| !connection.is_closed()) {
        return Ok(open.clone());
    }
    let connection = crate::linux_desktop::bus::connect_session(
        &glib::MainContext::new(), Duration::from_millis(500),
    ).map_err(|error| format!("The desktop session bus is unavailable: {error}"))?;
    *bus = Some(connection.clone());
    Ok(connection)
}

fn launch(handler: gio::AppInfo, uri: &str) -> Result<Via, String> {
    use glib::translate::{from_glib_full, ToGlibPtr};
    let desktop = handler.downcast::<gio::DesktopAppInfo>()
        .map_err(|_| "The desktop association has no native application entry".to_owned())?;
    let uris: &[&str] = &[uri];
    let mut error = std::ptr::null_mut();
    // GIO may activate an existing D-Bus application instead; that service does
    // not inherit this process's credentials. Every newly spawned handler drops them.
    let accepted = unsafe {
        gio::ffi::g_desktop_app_info_launch_uris_as_manager(
            desktop.to_glib_none().0, uris.to_glib_none().0, std::ptr::null_mut(),
            glib::ffi::G_SPAWN_SEARCH_PATH, Some(crate::child_credentials::desktop_setup),
            std::ptr::null_mut(), None, std::ptr::null_mut(), &mut error,
        )
    };
    if !error.is_null() {
        let error: glib::Error = unsafe { from_glib_full(error) };
        return Err(format!("The desktop application could not open the item: {error}"));
    }
    if accepted == glib::ffi::GFALSE {
        return Err("The desktop application did not accept the item".into());
    }
    Ok(Via::DefaultHandler)
}

fn xdg_open(uri: &str) -> Result<Via, String> {
    let mut command = Command::new("xdg-open");
    command.arg(uri);
    crate::child_credentials::prepare(&mut command);
    let status = command.status().map_err(|error| format!("xdg-open: {error}"))?;
    if status.success() { Ok(Via::XdgOpen) }
    else { Err(format!("xdg-open exited with {status}")) }
}

pub fn open_uri(uri: &str) -> Result<Via, String> {
    let parsed = url::Url::parse(uri).map_err(|error| format!("Invalid destination: {error}"))?;
    match gio::AppInfo::default_for_uri_scheme(parsed.scheme()) {
        Some(handler) => launch(handler, uri),
        None => xdg_open(uri),
    }
}

pub fn open_file(path: &Path) -> Result<Via, String> {
    let file = gio::File::for_path(path);
    let uri = file.uri();
    match file.query_default_handler(gio::Cancellable::NONE) {
        Ok(handler) => launch(handler, &uri),
        Err(error) if error.matches(gio::IOErrorEnum::NotSupported) => xdg_open(&uri),
        Err(error) => Err(format!("No desktop application could open the item: {error}")),
    }
}

fn unanswered(error: &glib::Error) -> bool {
    use glib::translate::{from_glib_full, ToGlibPtr};
    let name: Option<glib::GString> = unsafe {
        from_glib_full(gio::ffi::g_dbus_error_get_remote_error(error.to_glib_none().0))
    };
    name.as_deref().is_some_and(|name| UNAVAILABLE_ERRORS.contains(&name))
        || name.is_none() && error.matches(gio::IOErrorEnum::TimedOut)
}

/// A file-manager refusal is terminal. Only an unanswered interface opens its parent.
pub fn show_item(path: &Path) -> Result<Via, String> {
    if let Ok(bus) = session_bus() {
        let items = vec![gio::File::for_path(path).uri().to_string()];
        match bus.call_sync(Some(FILE_MANAGER), "/org/freedesktop/FileManager1", FILE_MANAGER,
            "ShowItems", Some(&(items, "").to_variant()), None, gio::DBusCallFlags::NONE,
            CALL_TIMEOUT_MS, gio::Cancellable::NONE) {
            Ok(_) => return Ok(Via::FileManager),
            Err(error) if unanswered(&error) => {},
            Err(error) => return Err(format!("The file manager did not open the item: {error}")),
        }
    }
    let parent = if path.is_dir() { path } else { path.parent().unwrap_or(path) };
    open_file(parent)
}

pub fn open_in_default_browser(uri: &str) -> Result<(), String> {
    let browser = gio::AppInfo::default_for_uri_scheme("https")
        .ok_or_else(|| "No default browser is configured.".to_owned())?;
    launch(browser, uri).map(|_| ())
}

#[cfg(test)]
mod tests {
    use super::*;
    fn remote(name: &str) -> glib::Error {
        use glib::translate::{from_glib_full, ToGlibPtr};
        unsafe { from_glib_full(gio::ffi::g_dbus_error_new_for_dbus_error(
            name.to_glib_none().0, "fixture".to_glib_none().0)) }
    }
    #[test]
    fn only_unanswered_file_manager_interfaces_allow_an_alternative() {
        for name in UNAVAILABLE_ERRORS { assert!(unanswered(&remote(name)), "{name}"); }
        for name in ["org.freedesktop.DBus.Error.AccessDenied", "org.freedesktop.DBus.Error.Failed",
                     "org.freedesktop.portal.Error.NotAllowed"] {
            assert!(!unanswered(&remote(name)), "{name}");
        }
        assert!(!unanswered(&glib::Error::new(gio::IOErrorEnum::PermissionDenied, "fixture")));
    }
    #[test]
    fn local_file_uris_preserve_reserved_and_unicode_bytes() {
        assert_eq!(gio::File::for_path("/repo/a #?%é.txt").uri(), "file:///repo/a%20%23%3F%25%C3%A9.txt");
    }
}
