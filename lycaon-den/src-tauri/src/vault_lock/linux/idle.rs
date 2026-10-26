//! The display server's account of the person's input, which bounds how long
//! an unlock survives when no lock signal arrives.
//!
//! A locker that never tells logind or a screen saver service (swaylock,
//! hyprlock, i3lock) leaves the lock sources silent, so they only make a lock
//! take effect sooner. Coverage is input idle as the display server reports
//! it: the Wayland compositor's `ext_idle_notifier_v1`, GNOME Shell's idle
//! monitor where the compositor is GNOME Shell, or the X server's
//! MIT-SCREEN-SAVER extension.

use std::path::{Path, PathBuf};
use std::time::Duration;

use gtk::glib::ToVariant;
use gtk::{gio, glib};
use serde::Serialize;
use x11rb::connection::RequestConnection;
use x11rb::protocol::screensaver::{self, ConnectionExt as _};
use x11rb::rust_connection::RustConnection;

use crate::linux_desktop::bus;

mod wayland;
use wayland::{Wayland, WaylandStart};

/// How long the person's input may stay idle before they count as stepped
/// away where no lock is observable: the engine's unlock idle period, from
/// the one file both embed.
pub(crate) fn unlock_idle() -> Duration {
    static IDLE: std::sync::LazyLock<Duration> = std::sync::LazyLock::new(|| {
        let lifetime: serde_json::Value = serde_json::from_str(include_str!(
            "../../../../../lycaon/internal/presence/unlock-lifetime.json"
        ))
        .expect("embedded unlock lifetime");
        Duration::from_secs(
            lifetime["idle_seconds"]
                .as_u64()
                .filter(|seconds| *seconds > 0)
                .expect("positive unlock idle period"),
        )
    });
    *IDLE
}

const SETUP_TIMEOUT: Duration = Duration::from_secs(2);
const CALL_TIMEOUT: Duration = Duration::from_secs(2);
const MUTTER_NAME: &str = "org.gnome.Mutter.IdleMonitor";
const MUTTER_PATH: &str = "/org/gnome/Mutter/IdleMonitor/Core";

/// Which display-server facility observes input idle.
#[derive(Debug, Clone, Copy, Serialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub(crate) enum IdleSource {
    /// `get_input_idle_notification`, which idle inhibitors do not hold off.
    WaylandInputIdle,
    /// `get_idle_notification`; an idle inhibitor, such as a playing video,
    /// holds it off.
    WaylandIdle,
    MutterIdleMonitor,
    X11ScreenSaver,
}

/// Why no observer covers this session.
#[derive(Debug, Clone, Copy, Serialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub(crate) enum IdleUnavailable {
    NoDisplaySession,
    WaylandUnreachable,
    NoIdleNotifier,
    MutterNotCompositor,
    X11Unreachable,
    X11NoScreenSaver,
    /// An observer that was running stopped answering.
    Lost,
}

/// What the idle observer reports.
#[derive(Debug, Clone, Default, Serialize, PartialEq, Eq)]
pub(crate) struct IdleFacts {
    pub source: Option<IdleSource>,
    pub error: Option<IdleUnavailable>,
    /// Input stayed idle for the unlock idle period.
    pub idle: bool,
}

impl IdleFacts {
    pub(super) fn unavailable(error: IdleUnavailable) -> Self {
        Self {
            source: None,
            error: Some(error),
            idle: false,
        }
    }
}

/// Where the display servers listen. Production reads the session's
/// `WAYLAND_DISPLAY` and `DISPLAY`.
#[derive(Clone, Default)]
pub(crate) struct DisplayTargets {
    pub wayland: Option<PathBuf>,
    pub x11: Option<String>,
}

impl DisplayTargets {
    pub(crate) fn native() -> Self {
        Self {
            wayland: wayland_socket(
                std::env::var("WAYLAND_DISPLAY").ok().as_deref(),
                std::env::var("XDG_RUNTIME_DIR").ok().as_deref(),
            ),
            x11: std::env::var("DISPLAY")
                .ok()
                .filter(|display| !display.is_empty()),
        }
    }
}

/// `WAYLAND_DISPLAY` names a socket in the runtime directory unless absolute.
fn wayland_socket(display: Option<&str>, runtime: Option<&str>) -> Option<PathBuf> {
    let display = display.filter(|display| !display.is_empty())?;
    if display.starts_with('/') {
        return Some(PathBuf::from(display));
    }
    let runtime = runtime.filter(|runtime| runtime.starts_with('/'))?;
    Some(Path::new(runtime).join(display))
}

/// An observer polled on the monitor's context.
pub(super) trait Observer {
    fn facts(&mut self, context: &glib::MainContext) -> IdleFacts;
}

/// Starts the observer the session's display server offers. `desktop` is
/// the session bus, for GNOME Shell's idle monitor.
pub(super) fn start(
    session_type: &str,
    targets: &DisplayTargets,
    context: &glib::MainContext,
    desktop: Option<&gio::DBusConnection>,
) -> Result<Box<dyn Observer>, IdleUnavailable> {
    match session_type {
        "wayland" => {
            let socket = targets
                .wayland
                .as_ref()
                .ok_or(IdleUnavailable::WaylandUnreachable)?;
            match Wayland::start(socket, unlock_idle()) {
                Ok(observer) => Ok(Box::new(observer)),
                Err(WaylandStart::NoNotifier { compositor }) => {
                    let desktop = desktop.ok_or(IdleUnavailable::MutterNotCompositor)?;
                    Ok(Box::new(Mutter::verify(context, desktop, compositor)?))
                }
                Err(WaylandStart::Unreachable) => Err(IdleUnavailable::WaylandUnreachable),
            }
        }
        "x11" => {
            let display = targets
                .x11
                .as_deref()
                .ok_or(IdleUnavailable::X11Unreachable)?;
            Ok(Box::new(X11::connect(display)?))
        }
        _ => Err(IdleUnavailable::NoDisplaySession),
    }
}

/// GNOME Shell's idle monitor, accepted only from the compositor's process:
/// any session-bus client could claim the name where GNOME Shell does not.
pub(super) struct Mutter {
    connection: gio::DBusConnection,
    owner: String,
}

impl Mutter {
    pub(super) fn verify(
        context: &glib::MainContext,
        desktop: &gio::DBusConnection,
        compositor: u32,
    ) -> Result<Self, IdleUnavailable> {
        let refused = || IdleUnavailable::MutterNotCompositor;
        let owner = bus::call(
            context,
            desktop,
            Some("org.freedesktop.DBus"),
            "/org/freedesktop/DBus",
            "org.freedesktop.DBus",
            "GetNameOwner",
            Some(&(MUTTER_NAME,).to_variant()),
            CALL_TIMEOUT,
        )
        .ok()
        .and_then(|reply| reply.child_value(0).str().map(str::to_owned))
        .ok_or_else(refused)?;
        let pid = bus::call(
            context,
            desktop,
            Some("org.freedesktop.DBus"),
            "/org/freedesktop/DBus",
            "org.freedesktop.DBus",
            "GetConnectionUnixProcessID",
            Some(&(owner.as_str(),).to_variant()),
            CALL_TIMEOUT,
        )
        .ok()
        .and_then(|reply| reply.child_value(0).get::<u32>())
        .ok_or_else(refused)?;
        if pid != compositor {
            return Err(refused());
        }
        Ok(Self {
            connection: desktop.clone(),
            owner,
        })
    }
}

impl Observer for Mutter {
    /// Addresses the verified owner's unique name, so a replaced owner
    /// answers nothing and the observer is lost rather than redirected.
    fn facts(&mut self, context: &glib::MainContext) -> IdleFacts {
        let reply = bus::call(
            context,
            &self.connection,
            Some(&self.owner),
            MUTTER_PATH,
            MUTTER_NAME,
            "GetIdletime",
            None,
            CALL_TIMEOUT,
        );
        match reply
            .ok()
            .and_then(|reply| reply.child_value(0).get::<u64>())
        {
            Some(milliseconds) => IdleFacts {
                source: Some(IdleSource::MutterIdleMonitor),
                error: None,
                idle: idle_after(milliseconds, unlock_idle()),
            },
            None => IdleFacts::unavailable(IdleUnavailable::Lost),
        }
    }
}

/// The X server's MIT-SCREEN-SAVER extension. Any X11 client can synthesize
/// input, so this observer is bounded by X11's own same-session trust.
pub(super) struct X11 {
    connection: RustConnection,
    root: u32,
}

impl X11 {
    pub(super) fn connect(display: &str) -> Result<Self, IdleUnavailable> {
        let (connection, screen) =
            RustConnection::connect(Some(display)).map_err(|_| IdleUnavailable::X11Unreachable)?;
        if connection
            .extension_information(screensaver::X11_EXTENSION_NAME)
            .ok()
            .flatten()
            .is_none()
        {
            return Err(IdleUnavailable::X11NoScreenSaver);
        }
        let root = x11rb::connection::Connection::setup(&connection)
            .roots
            .get(screen)
            .ok_or(IdleUnavailable::X11Unreachable)?
            .root;
        Ok(Self { connection, root })
    }
}

impl Observer for X11 {
    fn facts(&mut self, _: &glib::MainContext) -> IdleFacts {
        let reply = self
            .connection
            .screensaver_query_info(self.root)
            .ok()
            .and_then(|cookie| cookie.reply().ok());
        match reply {
            Some(info) => IdleFacts {
                source: Some(IdleSource::X11ScreenSaver),
                error: None,
                idle: idle_after(u64::from(info.ms_since_user_input), unlock_idle()),
            },
            None => IdleFacts::unavailable(IdleUnavailable::Lost),
        }
    }
}

fn idle_after(milliseconds: u64, period: Duration) -> bool {
    u128::from(milliseconds) >= period.as_millis()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_wayland_socket_is_absolute_or_in_the_runtime_directory() {
        assert_eq!(
            wayland_socket(Some("wayland-1"), Some("/run/user/1000")),
            Some(PathBuf::from("/run/user/1000/wayland-1"))
        );
        assert_eq!(
            wayland_socket(Some("/tmp/compositor"), None),
            Some(PathBuf::from("/tmp/compositor"))
        );
        assert_eq!(wayland_socket(Some("wayland-1"), Some("relative")), None);
        assert_eq!(wayland_socket(Some(""), Some("/run/user/1000")), None);
        assert_eq!(wayland_socket(None, Some("/run/user/1000")), None);
    }

    #[test]
    fn input_counts_as_idle_from_the_unlock_idle_period() {
        // The engine's documented unlock idle expiry.
        assert_eq!(unlock_idle(), Duration::from_secs(15 * 60));
        assert!(!idle_after(899_999, unlock_idle()));
        assert!(idle_after(900_000, unlock_idle()));
        assert!(idle_after(u64::MAX, unlock_idle()));
    }

    #[test]
    fn only_display_sessions_have_an_observer() {
        let context = glib::MainContext::new();
        for kind in ["tty", "mir", "unspecified"] {
            assert_eq!(
                start(kind, &DisplayTargets::default(), &context, None).err(),
                Some(IdleUnavailable::NoDisplaySession)
            );
        }
        assert_eq!(
            start("wayland", &DisplayTargets::default(), &context, None).err(),
            Some(IdleUnavailable::WaylandUnreachable)
        );
        assert_eq!(
            start("x11", &DisplayTargets::default(), &context, None).err(),
            Some(IdleUnavailable::X11Unreachable)
        );
    }
}
