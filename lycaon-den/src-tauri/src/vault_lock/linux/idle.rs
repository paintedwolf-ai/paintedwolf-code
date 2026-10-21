//! The display server's account of the person's input, which bounds how long
//! an unlock survives when no lock signal arrives.
//!
//! A locker that never tells logind or a screen saver service (swaylock,
//! hyprlock, i3lock) leaves the lock sources silent, so they only make a lock
//! take effect sooner. Coverage is input idle as the display server reports
//! it: the Wayland compositor's `ext_idle_notifier_v1`, GNOME Shell's idle
//! monitor where the compositor is GNOME Shell, or the X server's
//! MIT-SCREEN-SAVER extension.

use std::io;
use std::os::fd::{AsFd, AsRawFd};
use std::os::unix::net::UnixStream;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{mpsc, Arc, Mutex};
use std::thread;
use std::time::Duration;

use gtk::glib::ToVariant;
use gtk::{gio, glib};
use serde::Serialize;
use wayland_client::globals::{registry_queue_init, GlobalListContents};
use wayland_client::protocol::{wl_registry, wl_seat};
use wayland_client::{Connection, Dispatch, Proxy, QueueHandle};
use wayland_protocols::ext::idle_notify::v1::client::{
    ext_idle_notification_v1, ext_idle_notifier_v1,
};
use x11rb::connection::RequestConnection;
use x11rb::protocol::screensaver::{self, ConnectionExt as _};
use x11rb::rust_connection::RustConnection;

use crate::linux_desktop::bus;

/// How long the person's input may stay idle before they count as stepped
/// away.
pub(crate) const STEP_AWAY_IDLE: Duration = Duration::from_secs(5 * 60);

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
    /// Input stayed idle for the step-away period.
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
            match Wayland::start(socket, STEP_AWAY_IDLE) {
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

/// The process on the other end of a local socket.
fn peer_pid(stream: &UnixStream) -> io::Result<u32> {
    let mut credentials = libc::ucred {
        pid: 0,
        uid: 0,
        gid: 0,
    };
    let mut size = std::mem::size_of::<libc::ucred>() as libc::socklen_t;
    // SAFETY: the descriptor is open for the call and the buffer is a ucred
    // of the size passed.
    let result = unsafe {
        libc::getsockopt(
            stream.as_raw_fd(),
            libc::SOL_SOCKET,
            libc::SO_PEERCRED,
            (&mut credentials as *mut libc::ucred).cast(),
            &mut size,
        )
    };
    if result != 0 {
        return Err(io::Error::last_os_error());
    }
    u32::try_from(credentials.pid).map_err(|_| io::Error::other("invalid peer pid"))
}

pub(super) enum WaylandStart {
    /// The compositor offers no idle notifier; its process is named so a
    /// session-bus monitor can be bound to it.
    NoNotifier {
        compositor: u32,
    },
    Unreachable,
}

/// `ext_idle_notifier_v1` on its own connection and thread.
pub(super) struct Wayland {
    facts: Arc<Mutex<IdleFacts>>,
    stop: Arc<AtomicBool>,
    worker: Option<thread::JoinHandle<()>>,
}

struct WaylandState {
    facts: Arc<Mutex<IdleFacts>>,
}

impl Dispatch<wl_registry::WlRegistry, GlobalListContents> for WaylandState {
    fn event(
        _: &mut Self,
        _: &wl_registry::WlRegistry,
        _: wl_registry::Event,
        _: &GlobalListContents,
        _: &Connection,
        _: &QueueHandle<Self>,
    ) {
    }
}

impl Dispatch<wl_seat::WlSeat, ()> for WaylandState {
    fn event(
        _: &mut Self,
        _: &wl_seat::WlSeat,
        _: wl_seat::Event,
        _: &(),
        _: &Connection,
        _: &QueueHandle<Self>,
    ) {
    }
}

impl Dispatch<ext_idle_notifier_v1::ExtIdleNotifierV1, ()> for WaylandState {
    fn event(
        _: &mut Self,
        _: &ext_idle_notifier_v1::ExtIdleNotifierV1,
        _: ext_idle_notifier_v1::Event,
        _: &(),
        _: &Connection,
        _: &QueueHandle<Self>,
    ) {
    }
}

impl Dispatch<ext_idle_notification_v1::ExtIdleNotificationV1, ()> for WaylandState {
    fn event(
        state: &mut Self,
        _: &ext_idle_notification_v1::ExtIdleNotificationV1,
        event: ext_idle_notification_v1::Event,
        _: &(),
        _: &Connection,
        _: &QueueHandle<Self>,
    ) {
        let idle = match event {
            ext_idle_notification_v1::Event::Idled => true,
            ext_idle_notification_v1::Event::Resumed => false,
            _ => return,
        };
        state.facts.lock().unwrap().idle = idle;
    }
}

impl Wayland {
    pub(super) fn start(socket: &Path, period: Duration) -> Result<Self, WaylandStart> {
        let stream = UnixStream::connect(socket).map_err(|_| WaylandStart::Unreachable)?;
        let compositor = peer_pid(&stream).map_err(|_| WaylandStart::Unreachable)?;
        let facts = Arc::new(Mutex::new(IdleFacts::default()));
        let stop = Arc::new(AtomicBool::new(false));
        let (ready, started) = mpsc::channel();
        let shared = facts.clone();
        let stopping = stop.clone();
        let timeout = u32::try_from(period.as_millis()).unwrap_or(u32::MAX);
        let worker = thread::spawn(move || {
            let observed = shared.clone();
            let lost = || *observed.lock().unwrap() = IdleFacts::unavailable(IdleUnavailable::Lost);
            let Ok(connection) = Connection::from_socket(stream) else {
                let _ = ready.send(Err(WaylandStart::Unreachable));
                return;
            };
            let Ok((globals, mut queue)) = registry_queue_init::<WaylandState>(&connection) else {
                let _ = ready.send(Err(WaylandStart::Unreachable));
                return;
            };
            let handle = queue.handle();
            let seat = globals.bind::<wl_seat::WlSeat, _, _>(&handle, 1..=1, ());
            let notifier =
                globals.bind::<ext_idle_notifier_v1::ExtIdleNotifierV1, _, _>(&handle, 1..=2, ());
            let (Ok(seat), Ok(notifier)) = (seat, notifier) else {
                let _ = ready.send(Err(WaylandStart::NoNotifier { compositor }));
                return;
            };
            let source = if notifier.version() >= 2 {
                notifier.get_input_idle_notification(timeout, &seat, &handle, ());
                IdleSource::WaylandInputIdle
            } else {
                notifier.get_idle_notification(timeout, &seat, &handle, ());
                IdleSource::WaylandIdle
            };
            shared.lock().unwrap().source = Some(source);
            let mut state = WaylandState {
                facts: shared.clone(),
            };
            if queue.roundtrip(&mut state).is_err() {
                let _ = ready.send(Err(WaylandStart::Unreachable));
                return;
            }
            let _ = ready.send(Ok(()));
            while !stopping.load(Ordering::Acquire) {
                if queue.dispatch_pending(&mut state).is_err() || connection.flush().is_err() {
                    lost();
                    return;
                }
                let Some(guard) = queue.prepare_read() else {
                    continue;
                };
                let mut poll = libc::pollfd {
                    fd: guard.connection_fd().as_fd().as_raw_fd(),
                    events: libc::POLLIN,
                    revents: 0,
                };
                // SAFETY: one pollfd for a descriptor the guard keeps open.
                let ready_count = unsafe { libc::poll(&mut poll, 1, 250) };
                if ready_count < 0 {
                    if io::Error::last_os_error().kind() == io::ErrorKind::Interrupted {
                        continue;
                    }
                    lost();
                    return;
                }
                if ready_count > 0 && guard.read().is_err() {
                    lost();
                    return;
                }
            }
        });
        match started.recv_timeout(SETUP_TIMEOUT) {
            Ok(Ok(())) => Ok(Self {
                facts,
                stop,
                worker: Some(worker),
            }),
            Ok(Err(error)) => {
                let _ = worker.join();
                Err(error)
            }
            Err(_) => {
                stop.store(true, Ordering::Release);
                Err(WaylandStart::Unreachable)
            }
        }
    }
}

impl Observer for Wayland {
    fn facts(&mut self, _: &glib::MainContext) -> IdleFacts {
        self.facts.lock().unwrap().clone()
    }
}

impl Drop for Wayland {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Release);
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
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
                idle: idle_after(milliseconds, STEP_AWAY_IDLE),
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
                idle: idle_after(u64::from(info.ms_since_user_input), STEP_AWAY_IDLE),
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
    fn input_counts_as_idle_from_the_step_away_period() {
        assert!(!idle_after(299_999, STEP_AWAY_IDLE));
        assert!(idle_after(300_000, STEP_AWAY_IDLE));
        assert!(idle_after(u64::MAX, STEP_AWAY_IDLE));
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
