//! Linux step-away signals. logind on the system bus announces the coming
//! sleep and this session's lock; the session bus's screensaver interface
//! is the fallback for desktops whose lock never reaches logind.

use std::cell::RefCell;

use gtk::glib::ToVariant;
use gtk::{gio, glib};
use tauri::AppHandle;

use super::{lock_in_background, lock_reason_for_signal};
use crate::linux_desktop::bus as linux_bus;

const LOGIND_NAME: &str = "org.freedesktop.login1";
const LOGIND_PATH: &str = "/org/freedesktop/login1";
const LOGIND_MANAGER: &str = "org.freedesktop.login1.Manager";
const LOGIND_SESSION: &str = "org.freedesktop.login1.Session";
const LOGIND_USER: &str = "org.freedesktop.login1.User";
const SCREENSAVER: &str = "org.freedesktop.ScreenSaver";
const CALL_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(5);
const BUS_CONNECT_TIMEOUT: std::time::Duration = std::time::Duration::from_millis(1500);

thread_local! {
    // Subscriptions live as long as their connections; the main thread owns both.
    static CONNECTIONS: RefCell<Vec<gio::DBusConnection>> = const { RefCell::new(Vec::new()) };
}

/// Which step-away sources this session can observe. A missing source is a
/// capability fact: the idle period and the ceiling still end unlocks.
#[derive(Debug, Default)]
struct Observed {
    sleep: bool,
    session_lock: bool,
    screensaver: bool,
}

pub(super) fn setup_linux(app: AppHandle) -> Result<(), String> {
    let context = glib::MainContext::default();
    let mut observed = Observed::default();
    match linux_bus::connect(&context, linux_bus::SYSTEM_BUS_ADDRESS, BUS_CONNECT_TIMEOUT) {
        Ok(system) => {
            observed.sleep = subscribe(
                &system,
                &app,
                Some(LOGIND_NAME),
                LOGIND_MANAGER,
                "PrepareForSleep",
                Some(LOGIND_PATH),
            );
            match own_session_path(&context, &system) {
                Ok(path) => {
                    observed.session_lock = subscribe(
                        &system,
                        &app,
                        Some(LOGIND_NAME),
                        LOGIND_SESSION,
                        "Lock",
                        Some(&path),
                    ) && subscribe_locked_hint(&system, &app, &path);
                }
                Err(error) => {
                    eprintln!("Stored values are not locked by this session's lock signal: {error}")
                }
            }
            CONNECTIONS.with(|connections| connections.borrow_mut().push(system));
        }
        Err(error) => eprintln!("Stored values are not locked by sleep or session lock: {error}"),
    }
    match linux_bus::connect_session(&context, BUS_CONNECT_TIMEOUT) {
        Ok(session) => {
            observed.screensaver =
                subscribe(&session, &app, None, SCREENSAVER, "ActiveChanged", None);
            CONNECTIONS.with(|connections| connections.borrow_mut().push(session));
        }
        Err(error) => eprintln!("Stored values are not locked by the screensaver signal: {error}"),
    }
    eprintln!(
        "Step-away lock sources: sleep={} session_lock={} screensaver={}",
        observed.sleep, observed.session_lock, observed.screensaver
    );
    Ok(())
}

/// Subscribes to one signal and locks on it. The first argument, when the
/// signal has one, says whether the state became active.
fn subscribe(
    connection: &gio::DBusConnection,
    app: &AppHandle,
    sender: Option<&str>,
    interface: &str,
    member: &str,
    object_path: Option<&str>,
) -> bool {
    let app = app.clone();
    connection.signal_subscribe(
        sender,
        Some(interface),
        Some(member),
        object_path,
        None,
        gio::DBusSignalFlags::NONE,
        move |_, _, _, interface, member, parameters| {
            let active = if parameters.n_children() > 0 {
                parameters.child_value(0).get::<bool>().unwrap_or(false)
            } else {
                true
            };
            if let Some(reason) = lock_reason_for_signal(interface, member, active) {
                lock_in_background(&app, reason);
            }
        },
    );
    true
}

/// Locks when the session's `LockedHint` becomes true. Desktops that lock
/// their own screen, such as GNOME Shell, report the lock this way rather
/// than through logind's `Lock` signal, which only `LockSession` emits.
fn subscribe_locked_hint(connection: &gio::DBusConnection, app: &AppHandle, path: &str) -> bool {
    let app = app.clone();
    connection.signal_subscribe(
        Some(LOGIND_NAME),
        Some("org.freedesktop.DBus.Properties"),
        Some("PropertiesChanged"),
        Some(path),
        Some(LOGIND_SESSION),
        gio::DBusSignalFlags::NONE,
        move |_, _, _, _, _, parameters| {
            if locked_hint_set(parameters) {
                lock_in_background(&app, super::LockReason::ScreenLocked);
            }
        },
    );
    true
}

/// Whether a session `PropertiesChanged` signal, `(sa{sv}as)`, sets
/// `LockedHint` to true.
fn locked_hint_set(parameters: &glib::Variant) -> bool {
    if !parameters.is_type(glib::VariantTy::new("(sa{sv}as)").expect("valid type")) {
        return false;
    }
    if parameters.child_value(0).str() != Some(LOGIND_SESSION) {
        return false;
    }
    glib::VariantDict::new(Some(&parameters.child_value(1)))
        .lookup::<bool>("LockedHint")
        .ok()
        .flatten()
        .unwrap_or(false)
}

/// The logind session whose lock this shell honors: the session it runs
/// in, or, when the desktop started it in the user's service manager and
/// it belongs to no session, the user's display session.
fn own_session_path(
    context: &glib::MainContext,
    system: &gio::DBusConnection,
) -> Result<String, String> {
    let by_pid = linux_bus::call(
        context,
        system,
        Some(LOGIND_NAME),
        LOGIND_PATH,
        LOGIND_MANAGER,
        "GetSessionByPID",
        Some(&(std::process::id(),).to_variant()),
        CALL_TIMEOUT,
    );
    if let Ok(reply) = by_pid {
        return reply
            .child_value(0)
            .str()
            .map(str::to_owned)
            .ok_or_else(|| "logind named no session".to_string());
    }
    // SAFETY: getuid has no preconditions and cannot fail.
    let uid = unsafe { libc::getuid() };
    let user = linux_bus::call(
        context,
        system,
        Some(LOGIND_NAME),
        LOGIND_PATH,
        LOGIND_MANAGER,
        "GetUser",
        Some(&(uid,).to_variant()),
        CALL_TIMEOUT,
    )
    .map_err(|error| error.to_string())?;
    let user_path = user
        .child_value(0)
        .str()
        .map(str::to_owned)
        .ok_or_else(|| "logind named no user".to_string())?;
    let display = linux_bus::call(
        context,
        system,
        Some(LOGIND_NAME),
        &user_path,
        "org.freedesktop.DBus.Properties",
        "Get",
        Some(&(LOGIND_USER, "Display").to_variant()),
        CALL_TIMEOUT,
    )
    .map_err(|error| error.to_string())?;
    display_session_path(&display).ok_or_else(|| "this user has no display session".to_string())
}

/// The session path in a `Properties.Get` reply for `User.Display`, whose
/// value is `(so)`; logind names `/` when the user has no display session.
fn display_session_path(reply: &glib::Variant) -> Option<String> {
    let value = reply.child_value(0).as_variant()?;
    if !value.is_type(glib::VariantTy::new("(so)").ok()?) {
        return None;
    }
    let path = value.child_value(1).str()?.to_owned();
    (path != "/").then_some(path)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn display_reply(id: &str, path: &str) -> glib::Variant {
        let value = glib::Variant::tuple_from_iter([
            id.to_variant(),
            glib::variant::ObjectPath::try_from(path)
                .unwrap()
                .to_variant(),
        ]);
        glib::Variant::tuple_from_iter([glib::Variant::from_variant(&value)])
    }

    fn properties_changed(interface: &str, hint: Option<bool>) -> glib::Variant {
        let changed = glib::VariantDict::new(None);
        if let Some(hint) = hint {
            changed.insert("LockedHint", hint);
        }
        changed.insert("IdleHint", true);
        glib::Variant::tuple_from_iter([
            interface.to_variant(),
            changed.end(),
            Vec::<String>::new().to_variant(),
        ])
    }

    #[test]
    fn a_set_locked_hint_locks() {
        assert!(locked_hint_set(&properties_changed(
            LOGIND_SESSION,
            Some(true)
        )));
        assert!(!locked_hint_set(&properties_changed(
            LOGIND_SESSION,
            Some(false)
        )));
        assert!(!locked_hint_set(&properties_changed(LOGIND_SESSION, None)));
        assert!(!locked_hint_set(&properties_changed(
            "org.example.Other",
            Some(true)
        )));
        assert!(!locked_hint_set(&"LockedHint".to_variant()));
    }

    #[test]
    fn the_display_session_is_read_from_the_user_property() {
        assert_eq!(
            display_session_path(&display_reply("8", "/org/freedesktop/login1/session/_38")),
            Some("/org/freedesktop/login1/session/_38".to_string())
        );
        assert_eq!(display_session_path(&display_reply("", "/")), None);
        let wrong =
            glib::Variant::tuple_from_iter([glib::Variant::from_variant(&"8".to_variant())]);
        assert_eq!(display_session_path(&wrong), None);
    }
}
