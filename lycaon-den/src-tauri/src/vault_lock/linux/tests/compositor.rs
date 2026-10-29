//! A minimal Wayland compositor: a seat and, unless withheld, the idle
//! notifier at a chosen version, driven by the test.

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{mpsc, Arc, Mutex};
use std::thread;
use std::time::Duration;

use wayland_protocols::ext::idle_notify::v1::server::{
    ext_idle_notification_v1::{self, ExtIdleNotificationV1},
    ext_idle_notifier_v1::{self, ExtIdleNotifierV1},
};
use wayland_server::backend::{ClientData, ClientId, DisconnectReason};
use wayland_server::protocol::wl_seat::{self, WlSeat};
use wayland_server::{
    Client, DataInit, Dispatch, Display, DisplayHandle, GlobalDispatch, ListeningSocket, New,
};

/// One idle notification a client asked for: its timeout and whether it was
/// the input-idle variant.
pub(super) type Requested = (u32, bool);

struct Server {
    notifications: Vec<ExtIdleNotificationV1>,
    requested: Arc<Mutex<Vec<Requested>>>,
}

struct Connected;

impl ClientData for Connected {
    fn initialized(&self, _: ClientId) {}
    fn disconnected(&self, _: ClientId, _: DisconnectReason) {}
}

impl GlobalDispatch<WlSeat, ()> for Server {
    fn bind(
        _: &mut Self,
        _: &DisplayHandle,
        _: &Client,
        resource: New<WlSeat>,
        _: &(),
        data_init: &mut DataInit<'_, Self>,
    ) {
        data_init.init(resource, ());
    }
}

impl Dispatch<WlSeat, ()> for Server {
    fn request(
        _: &mut Self,
        _: &Client,
        _: &WlSeat,
        _: wl_seat::Request,
        _: &(),
        _: &DisplayHandle,
        _: &mut DataInit<'_, Self>,
    ) {
    }
}

impl GlobalDispatch<ExtIdleNotifierV1, ()> for Server {
    fn bind(
        _: &mut Self,
        _: &DisplayHandle,
        _: &Client,
        resource: New<ExtIdleNotifierV1>,
        _: &(),
        data_init: &mut DataInit<'_, Self>,
    ) {
        data_init.init(resource, ());
    }
}

impl Dispatch<ExtIdleNotifierV1, ()> for Server {
    fn request(
        state: &mut Self,
        _: &Client,
        _: &ExtIdleNotifierV1,
        request: ext_idle_notifier_v1::Request,
        _: &(),
        _: &DisplayHandle,
        data_init: &mut DataInit<'_, Self>,
    ) {
        let (id, timeout, input) = match request {
            ext_idle_notifier_v1::Request::GetIdleNotification { id, timeout, .. } => {
                (id, timeout, false)
            }
            ext_idle_notifier_v1::Request::GetInputIdleNotification { id, timeout, .. } => {
                (id, timeout, true)
            }
            _ => return,
        };
        state.notifications.push(data_init.init(id, ()));
        state.requested.lock().unwrap().push((timeout, input));
    }
}

impl Dispatch<ExtIdleNotificationV1, ()> for Server {
    fn request(
        _: &mut Self,
        _: &Client,
        _: &ExtIdleNotificationV1,
        _: ext_idle_notification_v1::Request,
        _: &(),
        _: &DisplayHandle,
        _: &mut DataInit<'_, Self>,
    ) {
    }
}

/// A compositor serving on its own thread until dropped.
pub(super) struct Compositor {
    pub socket: PathBuf,
    pub requested: Arc<Mutex<Vec<Requested>>>,
    idle: mpsc::Sender<bool>,
    stop: Arc<AtomicBool>,
    worker: Option<thread::JoinHandle<()>>,
}

impl Compositor {
    /// `notifier` is the idle notifier version to advertise, or none.
    pub(super) fn start(socket: &Path, notifier: Option<u32>) -> Self {
        let _ = std::fs::remove_file(socket);
        let listener = ListeningSocket::bind_absolute(socket.to_path_buf()).unwrap();
        let requested = Arc::new(Mutex::new(Vec::new()));
        let stop = Arc::new(AtomicBool::new(false));
        let (idle, changes) = mpsc::channel::<bool>();
        let shared = requested.clone();
        let stopping = stop.clone();
        let worker = thread::spawn(move || {
            let mut display = Display::<Server>::new().unwrap();
            let handle = display.handle();
            handle.create_global::<Server, WlSeat, ()>(1, ());
            if let Some(version) = notifier {
                handle.create_global::<Server, ExtIdleNotifierV1, ()>(version, ());
            }
            let mut state = Server {
                notifications: Vec::new(),
                requested: shared,
            };
            while !stopping.load(Ordering::Acquire) {
                if let Ok(Some(stream)) = listener.accept() {
                    let _ = display.handle().insert_client(stream, Arc::new(Connected));
                }
                let _ = display.dispatch_clients(&mut state);
                while let Ok(idle) = changes.try_recv() {
                    for notification in &state.notifications {
                        if idle {
                            notification.idled();
                        } else {
                            notification.resumed();
                        }
                    }
                }
                let _ = display.flush_clients();
                thread::sleep(Duration::from_millis(2));
            }
        });
        Self {
            socket: socket.to_path_buf(),
            requested,
            idle,
            stop,
            worker: Some(worker),
        }
    }

    pub(super) fn report_idle(&self, idle: bool) {
        self.idle.send(idle).unwrap();
    }
}

impl Drop for Compositor {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Release);
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
        let _ = std::fs::remove_file(&self.socket);
    }
}

/// Environment that turns this test binary into a compositor process with no
/// idle notifier, serving at the named socket until killed.
pub(super) const CHILD_SOCKET_ENV: &str = "PW_TEST_COMPOSITOR_SOCKET";

#[test]
fn compositor_child() {
    let Ok(socket) = std::env::var(CHILD_SOCKET_ENV) else {
        // Only a test that starts a separate compositor process runs it.
        return;
    };
    let _compositor = Compositor::start(Path::new(&socket), None);
    loop {
        thread::sleep(Duration::from_secs(1));
    }
}
