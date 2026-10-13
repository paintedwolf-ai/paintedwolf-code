//! Linux step-away signals. logind on the system bus announces the coming
//! sleep and this session's lock; the session bus's screensaver interface
//! is the fallback for desktops whose lock never reaches logind.

use std::cell::RefCell;

use gtk::{gio, glib};
use tauri::AppHandle;

use super::{lock_in_background, lock_reason_for_signal};
use crate::linux_bus;

const LOGIND_NAME: &str = "org.freedesktop.login1";
const LOGIND_PATH: &str = "/org/freedesktop/login1";
const LOGIND_MANAGER: &str = "org.freedesktop.login1.Manager";
const LOGIND_SESSION: &str = "org.freedesktop.login1.Session";
const SCREENSAVER: &str = "org.freedesktop.ScreenSaver";
const CALL_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(5);

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
    match linux_bus::connect(&context, linux_bus::SYSTEM_BUS_ADDRESS) {
        Ok(system) => {
            observed.sleep = subscribe(&system, &app, Some(LOGIND_NAME), LOGIND_MANAGER, "PrepareForSleep", Some(LOGIND_PATH));
            match own_session_path(&context, &system) {
                Ok(path) => {
                    observed.session_lock = subscribe(&system, &app, Some(LOGIND_NAME), LOGIND_SESSION, "Lock", Some(&path));
                }
                Err(error) => eprintln!("Stored values are not locked by this session's lock signal: {error}"),
            }
            CONNECTIONS.with(|connections| connections.borrow_mut().push(system));
        }
        Err(error) => eprintln!("Stored values are not locked by sleep or session lock: {error}"),
    }
    match linux_bus::session_address().and_then(|address| linux_bus::connect(&context, &address)) {
        Ok(session) => {
            observed.screensaver = subscribe(&session, &app, None, SCREENSAVER, "ActiveChanged", None);
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

/// The logind session this shell runs in.
fn own_session_path(context: &glib::MainContext, system: &gio::DBusConnection) -> Result<String, String> {
    let reply = linux_bus::call(
        context,
        system,
        Some(LOGIND_NAME),
        LOGIND_PATH,
        LOGIND_MANAGER,
        "GetSessionByPID",
        Some(&(std::process::id(),).to_variant()),
        CALL_TIMEOUT,
    )
    .map_err(|error| error.to_string())?;
    reply
        .child_value(0)
        .str()
        .map(str::to_owned)
        .ok_or_else(|| "logind named no session".to_string())
}
