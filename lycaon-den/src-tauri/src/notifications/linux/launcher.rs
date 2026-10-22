//! The waiting count on the application's launcher icon.
//!
//! Docks and task managers that show counts (Plasma's task manager, Ubuntu
//! Dock, Dash to Dock) read the `com.canonical.Unity.LauncherEntry` protocol:
//! an `Update` signal carrying the desktop entry's URI and its `count`, and a
//! `Query` method answering the current state to a launcher that starts later.
//! The shell publishes it directly on the session bus. GNOME Shell's own dash
//! shows no counts; there the waiting count stays in notifications.

use std::cell::RefCell;
use std::collections::HashMap;
use std::sync::atomic::{AtomicI64, Ordering};
use std::sync::Arc;

use gtk::gio;
use gtk::glib::{self, ToVariant};

const APP_URI: &str = "application://dev.paintedwolf.code.desktop";
const PATH: &str = "/dev/paintedwolf/code/LauncherEntry";
const INTERFACE: &str = "com.canonical.Unity.LauncherEntry";
const XML: &str = "<node><interface name='com.canonical.Unity.LauncherEntry'>\
<method name='Query'><arg type='s' direction='out'/><arg type='a{sv}' direction='out'/></method>\
<signal name='Update'><arg type='s'/><arg type='a{sv}'/></signal></interface></node>";

fn properties(count: i64) -> HashMap<&'static str, glib::Variant> {
    HashMap::from([("count", count.to_variant()), ("count-visible", (count > 0).to_variant())])
}

/// The published entry; lives on the main context it was created on.
pub(crate) struct LauncherEntry {
    connection: gio::DBusConnection,
    count: Arc<AtomicI64>,
    registration: Option<gio::RegistrationId>,
}

impl LauncherEntry {
    pub(crate) fn new(connection: gio::DBusConnection) -> Result<Self, String> {
        let info = gio::DBusNodeInfo::for_xml(XML)
            .map_err(|error| error.to_string())?
            .lookup_interface(INTERFACE)
            .ok_or("launcher entry interface is missing")?;
        let count = Arc::new(AtomicI64::new(0));
        let state = count.clone();
        let registration = connection
            .register_object(
                PATH,
                &info,
                move |_, _, _, _, method, _, invocation| {
                    if method == "Query" {
                        invocation.return_value(Some(&(APP_URI, properties(state.load(Ordering::Relaxed))).to_variant()));
                    } else {
                        invocation.return_dbus_error("org.freedesktop.DBus.Error.UnknownMethod", "unknown method");
                    }
                },
                |_, _, _, _, _| false.to_variant(),
                |_, _, _, _, _, _| false,
            )
            .map_err(|error| error.to_string())?;
        Ok(Self { connection, count, registration: Some(registration) })
    }

    /// Shows `count` on the launcher icon; zero or less clears it.
    pub(crate) fn set(&self, count: i64) -> Result<(), String> {
        let count = count.max(0);
        self.count.store(count, Ordering::Relaxed);
        self.connection
            .emit_signal(None, PATH, INTERFACE, "Update", Some(&(APP_URI, properties(count)).to_variant()))
            .map_err(|error| error.to_string())
    }
}

impl Drop for LauncherEntry {
    fn drop(&mut self) {
        if let Some(registration) = self.registration.take() {
            let _ = self.connection.unregister_object(registration);
        }
    }
}

thread_local! {
    static ENTRY: RefCell<Option<LauncherEntry>> = const { RefCell::new(None) };
}

/// Publishes the count from the GTK main thread on the session bus.
pub(crate) fn set_on_main_thread(count: i64) -> Result<(), String> {
    ENTRY.with(|entry| {
        let mut entry = entry.borrow_mut();
        if entry.is_none() {
            let bus = gio::bus_get_sync(gio::BusType::Session, gio::Cancellable::NONE)
                .map_err(|error| format!("session bus unavailable for the launcher count: {error}"))?;
            *entry = Some(LauncherEntry::new(bus)?);
        }
        entry.as_ref().expect("launcher entry").set(count)
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{BufRead, BufReader, Read};
    use std::process::{Child, Command, Stdio};
    use std::sync::{Arc, Mutex};
    use std::time::{Duration, Instant};

    struct Daemon(Child);
    impl Drop for Daemon {
        fn drop(&mut self) {
            let _ = self.0.kill();
            let _ = self.0.wait();
        }
    }

    fn private_bus() -> (Daemon, String) {
        let mut child = Command::new("dbus-daemon")
            .args(["--session", "--nofork", "--print-address=1"])
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .expect("start private session bus");
        let mut address = String::new();
        BufReader::new(child.stdout.take().unwrap()).take(4096).read_line(&mut address).unwrap();
        (Daemon(child), address.trim_end().to_string())
    }

    fn connect(address: &str) -> gio::DBusConnection {
        gio::DBusConnection::for_address_sync(
            address,
            gio::DBusConnectionFlags::AUTHENTICATION_CLIENT | gio::DBusConnectionFlags::MESSAGE_BUS_CONNECTION,
            None,
            gio::Cancellable::NONE,
        )
        .expect("connect to private bus")
    }

    fn iterate_until(context: &glib::MainContext, mut done: impl FnMut() -> bool) {
        let deadline = Instant::now() + Duration::from_secs(5);
        while !done() {
            assert!(Instant::now() < deadline, "launcher bus exchange timed out");
            context.iteration(false);
            std::thread::sleep(Duration::from_millis(5));
        }
    }

    #[test]
    fn updates_and_queries_carry_the_desktop_entry_count() {
        let (_daemon, address) = private_bus();
        let context = glib::MainContext::new();
        context
            .with_thread_default(|| {
                let shell = connect(&address);
                let launcher = connect(&address);
                let updates: Arc<Mutex<Vec<(String, i64, bool)>>> = Arc::default();
                let seen = updates.clone();
                launcher.signal_subscribe(None, Some(INTERFACE), Some("Update"), Some(PATH), None,
                    gio::DBusSignalFlags::NONE, move |_, _, _, _, _, parameters| {
                        let (uri, properties) = parameters.get::<(String, HashMap<String, glib::Variant>)>().unwrap();
                        seen.lock().unwrap().push((uri, properties["count"].get::<i64>().unwrap(),
                            properties["count-visible"].get::<bool>().unwrap()));
                    });
                let entry = LauncherEntry::new(shell.clone()).unwrap();
                entry.set(3).unwrap();
                entry.set(-2).unwrap();
                iterate_until(&context, || updates.lock().unwrap().len() == 2);
                assert_eq!(*updates.lock().unwrap(), vec![
                    (APP_URI.to_string(), 3, true),
                    (APP_URI.to_string(), 0, false),
                ]);

                entry.set(5).unwrap();
                let name = shell.unique_name().unwrap();
                let reply: Arc<Mutex<Option<(String, i64)>>> = Arc::default();
                let answer = reply.clone();
                launcher.call(Some(&name), PATH, INTERFACE, "Query", None, None, gio::DBusCallFlags::NONE, 2000,
                    gio::Cancellable::NONE, move |result| {
                        let (uri, properties) = result.unwrap().get::<(String, HashMap<String, glib::Variant>)>().unwrap();
                        *answer.lock().unwrap() = Some((uri, properties["count"].get::<i64>().unwrap()));
                    });
                iterate_until(&context, || reply.lock().unwrap().is_some());
                assert_eq!(reply.lock().unwrap().clone(), Some((APP_URI.to_string(), 5)));
            })
            .unwrap();
    }
}
