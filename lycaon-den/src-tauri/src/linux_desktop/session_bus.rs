use std::time::Duration;

use gtk::{gio, glib};

pub(crate) fn connect(context: &glib::MainContext) -> Result<gio::DBusConnection, String> {
    let address = session_address(
        std::env::var("DBUS_SESSION_BUS_ADDRESS").ok().as_deref(),
        std::env::var("XDG_RUNTIME_DIR").ok().as_deref(),
        unsafe { libc::getuid() },
    )?;
    context
        .block_on(glib::future_with_timeout(
            Duration::from_millis(500),
            gio::DBusConnection::for_address_future(
                &address,
                gio::DBusConnectionFlags::AUTHENTICATION_CLIENT
                    | gio::DBusConnectionFlags::MESSAGE_BUS_CONNECTION,
                None,
            ),
        ))
        .map_err(|_| "Desktop session connection timed out".to_string())?
        .map_err(|error| error.to_string())
}

fn session_address(
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn session_selection_preserves_explicit_and_native_runtime_identity() {
        assert_eq!(
            session_address(
                Some("unix:abstract=chosen;unix:path=/other"),
                Some("/ignored"),
                42
            )
            .unwrap(),
            "unix:abstract=chosen;unix:path=/other"
        );
        assert_eq!(
            session_address(None, None, 42).unwrap(),
            "unix:path=/run/user/42/bus"
        );
        assert_eq!(
            session_address(None, Some("/runtime/alias/../person /"), 42).unwrap(),
            "unix:path=/runtime/alias/../person%20/bus"
        );
        assert_eq!(
            session_address(None, Some("/"), 42).unwrap(),
            "unix:path=/bus"
        );
    }

    #[test]
    fn session_selection_rejects_remote_unbounded_and_invalid_addresses() {
        for address in [
            "tcp:host=localhost,port=1",
            "autolaunch:",
            "unix:path=/a;tcp:host=localhost,port=1",
            "unix:path=/a\n",
        ] {
            assert!(session_address(Some(address), None, 42).is_err());
        }
        assert!(session_address(Some(&"x".repeat((64 << 10) + 1)), None, 42).is_err());
        assert!(session_address(Some(&vec!["unix:path=/a"; 17].join(";")), None, 42).is_err());
        for runtime in ["relative", "/a\0b", "/a\rb"] {
            assert!(session_address(None, Some(runtime), 42).is_err());
        }
    }
}
