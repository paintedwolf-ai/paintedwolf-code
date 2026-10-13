//! Bounded native Linux bus transport: the system bus through its fixed
//! socket, and the person's own session bus. Every call runs on a glib main
//! context the caller owns, so a hung bus costs the caller a timeout, never
//! the shell.

use std::time::Duration;

use futures_util::future::{select, Either};
use gtk::{gio, glib};

/// The system bus socket every distribution installs. The environment's
/// `DBUS_SYSTEM_BUS_ADDRESS` is not consulted: a redirected system bus would
/// answer presence checks and lock signals with whatever it liked.
pub(crate) const SYSTEM_BUS_ADDRESS: &str = "unix:path=/run/dbus/system_bus_socket";

const CONNECT_TIMEOUT: Duration = Duration::from_millis(1500);

/// Connects to a bus. The connection's asynchronous work completes on
/// `context`, which must be the thread-default context of the caller.
pub(crate) fn connect(
    context: &glib::MainContext,
    address: &str,
) -> Result<gio::DBusConnection, String> {
    context.block_on(async {
        let connect = gio::DBusConnection::for_address_future(
            address,
            gio::DBusConnectionFlags::AUTHENTICATION_CLIENT
                | gio::DBusConnectionFlags::MESSAGE_BUS_CONNECTION,
            None,
        );
        match select(connect, glib::timeout_future(CONNECT_TIMEOUT)).await {
            Either::Left((result, _)) => result.map_err(|error| error.to_string()),
            Either::Right(_) => Err("the bus connection timed out".to_string()),
        }
    })
}

/// Calls one method and waits for its reply on `context`.
pub(crate) fn call(
    context: &glib::MainContext,
    connection: &gio::DBusConnection,
    bus_name: Option<&str>,
    object_path: &str,
    interface: &str,
    method: &str,
    parameters: Option<&glib::Variant>,
    timeout: Duration,
) -> Result<glib::Variant, glib::Error> {
    let timeout = i32::try_from(timeout.as_millis()).unwrap_or(i32::MAX);
    context.block_on(connection.call_future(
        bus_name,
        object_path,
        interface,
        method,
        parameters,
        None,
        gio::DBusCallFlags::NONE,
        timeout,
    ))
}

/// The session bus address: the environment's Unix-only address, or the
/// runtime directory's bus socket.
pub(crate) fn session_address() -> Result<String, String> {
    session_address_from(
        std::env::var("DBUS_SESSION_BUS_ADDRESS").ok().as_deref(),
        std::env::var("XDG_RUNTIME_DIR").ok().as_deref(),
        // SAFETY: getuid has no preconditions and cannot fail.
        unsafe { libc::getuid() },
    )
}

fn session_address_from(
    address: Option<&str>,
    runtime: Option<&str>,
    uid: u32,
) -> Result<String, String> {
    if let Some(address) = address.filter(|address| !address.is_empty()) {
        if address.len() > 64 << 10 || address.contains(['\0', '\r', '\n']) {
            return Err("The desktop session address is invalid".into());
        }
        let addresses: Vec<_> = address.split(';').collect();
        if addresses.len() > 16
            || addresses
                .iter()
                .any(|address| !address.starts_with("unix:"))
        {
            return Err("Desktop session services require a local Unix transport".into());
        }
        return Ok(address.into());
    }
    let fallback = format!("/run/user/{uid}");
    let runtime = runtime
        .filter(|runtime| !runtime.is_empty())
        .unwrap_or(&fallback);
    if !runtime.starts_with('/') || runtime.contains(['\0', '\r', '\n']) {
        return Err("The desktop runtime directory is invalid".into());
    }
    let path = format!("{}/bus", runtime.trim_end_matches('/'));
    let mut encoded = String::new();
    for byte in path.bytes() {
        if byte.is_ascii_alphanumeric() || b"-_/.*".contains(&byte) {
            encoded.push(byte as char);
        } else {
            use std::fmt::Write;
            let _ = write!(encoded, "%{byte:02x}");
        }
    }
    Ok(format!("unix:path={encoded}"))
}

/// The start time of this process in clock ticks since boot, as polkit and
/// logind identify a `unix-process` subject.
pub(crate) fn own_start_time() -> Result<u64, String> {
    let stat = std::fs::read_to_string("/proc/self/stat")
        .map_err(|error| format!("read /proc/self/stat: {error}"))?;
    start_time_from_stat(&stat).ok_or_else(|| "the process start time is unreadable".to_string())
}

/// Field 22 of a stat line, after the parenthesized command name.
pub(crate) fn start_time_from_stat(stat: &str) -> Option<u64> {
    let rest = &stat[stat.rfind(')')? + 1..];
    rest.split_whitespace().nth(19)?.parse().ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_system_bus_is_the_fixed_socket() {
        assert_eq!(SYSTEM_BUS_ADDRESS, "unix:path=/run/dbus/system_bus_socket");
    }

    #[test]
    fn session_selection_preserves_explicit_and_native_runtime_identity() {
        assert_eq!(
            session_address_from(Some("unix:abstract=chosen;unix:path=/other"), Some("/ignored"), 42)
                .unwrap(),
            "unix:abstract=chosen;unix:path=/other"
        );
        assert_eq!(
            session_address_from(None, None, 42).unwrap(),
            "unix:path=/run/user/42/bus"
        );
        assert_eq!(
            session_address_from(None, Some("/runtime/alias/../person /"), 42).unwrap(),
            "unix:path=/runtime/alias/../person%20/bus"
        );
    }

    #[test]
    fn session_selection_rejects_remote_and_invalid_addresses() {
        for address in [
            "tcp:host=localhost,port=1",
            "autolaunch:",
            "unix:path=/a;tcp:host=localhost,port=1",
            "unix:path=/a\n",
        ] {
            assert!(session_address_from(Some(address), None, 42).is_err());
        }
        assert!(session_address_from(Some(&vec!["unix:path=/a"; 17].join(";")), None, 42).is_err());
        for runtime in ["relative", "/a\0b", "/a\rb"] {
            assert!(session_address_from(None, Some(runtime), 42).is_err());
        }
    }

    #[test]
    fn start_time_is_field_22_after_the_command_name() {
        let stat = "4242 (painted (wolf) code) S 1 4242 4242 0 -1 4194560 1 0 0 0 0 0 0 0 20 0 1 0 987654 1 1 1";
        assert_eq!(start_time_from_stat(stat), Some(987_654));
        assert_eq!(start_time_from_stat("4242 (x) S 1"), None);
        assert_eq!(start_time_from_stat("no parentheses"), None);
        assert!(own_start_time().is_ok());
    }
}
