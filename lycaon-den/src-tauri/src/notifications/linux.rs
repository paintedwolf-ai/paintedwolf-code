//! Native Linux desktop notification ownership.

mod activation;
mod delivery;
mod identity;

use std::{
    cell::RefCell,
    sync::{
        atomic::{AtomicBool, Ordering},
        mpsc, Arc, Mutex,
    },
    thread,
    time::{Duration, Instant},
};

use gtk::glib;
use tauri::{AppHandle, Manager};

use delivery::Delivery;

const REQUEST_TIMEOUT: Duration = Duration::from_secs(2);
const SESSION_CONNECT_TIMEOUT: Duration = Duration::from_millis(500);

enum Command {
    Permission,
    Send {
        title: String,
        body: String,
        session_id: Option<String>,
    },
    Cancel(Vec<String>),
}

struct Request {
    command: Command,
    deadline: Instant,
    reply: mpsc::SyncSender<Result<String, String>>,
}

/// Worker-thread state; requests reach it as main-loop callbacks.
struct Worker {
    app: AppHandle,
    delivery: Option<Delivery>,
}

thread_local! {
    static WORKER: RefCell<Option<Worker>> = const { RefCell::new(None) };
}

pub(super) struct NativeNotifications {
    context: glib::MainContext,
    main_loop: glib::MainLoop,
    stopped: Arc<AtomicBool>,
    worker: Mutex<Option<thread::JoinHandle<()>>>,
}

pub(super) fn setup(app: AppHandle) -> Result<(), String> {
    let context = glib::MainContext::new();
    let main_loop = glib::MainLoop::new(Some(&context), false);
    let stopped = Arc::new(AtomicBool::new(false));
    let target = app.clone();
    let run_context = context.clone();
    let run_loop = main_loop.clone();
    let worker = thread::Builder::new()
        .name("desktop-notifications".into())
        .spawn(move || {
            let _ = run_context.with_thread_default(|| {
                WORKER.with(|worker| {
                    *worker.borrow_mut() = Some(Worker {
                        app: target,
                        delivery: None,
                    });
                });
                run_loop.run();
                // Dropping the delivery closes its bus connection on this context.
                WORKER.with(|worker| worker.borrow_mut().take());
            });
        })
        .map_err(|error| error.to_string())?;
    app.manage(NativeNotifications {
        context,
        main_loop,
        stopped,
        worker: Mutex::new(Some(worker)),
    });
    Ok(())
}

pub(super) fn shutdown(app: &AppHandle) {
    if let Some(state) = app.try_state::<NativeNotifications>() {
        state.stopped.store(true, Ordering::Release);
        state.main_loop.quit();
        if let Some(worker) = state
            .worker
            .lock()
            .unwrap_or_else(|error| error.into_inner())
            .take()
        {
            let _ = worker.join();
        }
    }
}

async fn request(app: &AppHandle, command: Command) -> Result<String, String> {
    let state = app
        .try_state::<NativeNotifications>()
        .ok_or("Desktop notifications are unavailable")?;
    if state.stopped.load(Ordering::Acquire) {
        return Err("Desktop notifications have stopped".into());
    }
    let context = state.context.clone();
    tauri::async_runtime::spawn_blocking(move || {
        let (reply, receiver) = mpsc::sync_channel(1);
        let request = Request {
            command,
            deadline: Instant::now() + REQUEST_TIMEOUT,
            reply,
        };
        // The main loop wakes for the callback and sleeps again afterwards.
        context.invoke(move || handle(request));
        receiver
            .recv_timeout(REQUEST_TIMEOUT + Duration::from_secs(1))
            .map_err(|_| "Desktop notification response timed out".to_string())?
    })
    .await
    .map_err(|error| error.to_string())?
}

pub(super) async fn permission(app: &AppHandle) -> Result<String, String> {
    request(app, Command::Permission).await
}

/// Resolves to the delivery outcome name reported by the service.
pub(super) async fn send(
    app: &AppHandle,
    title: String,
    body: String,
    session_id: Option<String>,
) -> Result<String, String> {
    request(
        app,
        Command::Send {
            title,
            body,
            session_id,
        },
    )
    .await
}

pub(super) async fn cancel(app: &AppHandle, sessions: Vec<String>) -> Result<(), String> {
    request(app, Command::Cancel(sessions)).await.map(|_| ())
}

fn handle(request: Request) {
    let result = WORKER.with(|worker| {
        let mut worker = worker.borrow_mut();
        let Some(worker) = worker.as_mut() else {
            return Err("Desktop notifications have stopped".into());
        };
        worker.serve(&request.command, request.deadline)
    });
    let _ = request.reply.send(result);
}

impl Worker {
    fn serve(&mut self, command: &Command, deadline: Instant) -> Result<String, String> {
        if deadline <= Instant::now() {
            return Err("The desktop notification request expired".into());
        }
        if self
            .delivery
            .as_ref()
            .is_some_and(|delivery| !delivery.current(deadline))
        {
            self.delivery = None;
        }
        if self.delivery.is_none() {
            let target = self.app.clone();
            let context = glib::MainContext::ref_thread_default();
            self.delivery = crate::linux_desktop::bus::connect_session(&context, SESSION_CONNECT_TIMEOUT)
                .and_then(|connection| {
                    Delivery::new(
                        connection,
                        self.app.config().identifier.clone(),
                        move |session, token| activation::activate(&target, session, token),
                        deadline,
                    )
                })
                .ok();
        }
        match (command, &self.delivery) {
            (Command::Permission, Some(_)) => Ok("authorized".into()),
            (Command::Permission, None) => Ok("unavailable".into()),
            (
                Command::Send {
                    title,
                    body,
                    session_id,
                },
                Some(delivery),
            ) => delivery
                .send(title.clone(), body.clone(), session_id.clone(), deadline)
                .map(|outcome| outcome.as_str().to_string()),
            (Command::Cancel(sessions), Some(delivery)) => {
                delivery.cancel(sessions, deadline).map(|_| String::new())
            }
            _ => Err("The desktop notification service is unavailable".into()),
        }
    }
}
