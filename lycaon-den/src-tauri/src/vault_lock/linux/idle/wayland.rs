//! The compositor's `ext_idle_notifier_v1` on a client connection and thread
//! of its own, separate from GTK's.

use std::io;
use std::os::fd::{AsFd, AsRawFd};
use std::os::unix::net::UnixStream;
use std::path::Path;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{mpsc, Arc, Mutex};
use std::thread;
use std::time::Duration;

use gtk::glib;
use wayland_client::globals::{registry_queue_init, GlobalListContents};
use wayland_client::protocol::{wl_registry, wl_seat};
use wayland_client::{Connection, Dispatch, Proxy, QueueHandle};
use wayland_protocols::ext::idle_notify::v1::client::{
    ext_idle_notification_v1, ext_idle_notifier_v1,
};

use super::{IdleFacts, IdleSource, IdleUnavailable, Observer, SETUP_TIMEOUT};

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
