//! Native Linux desktop notification ownership.

mod delivery;
mod identity;

use std::{
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

pub(super) struct NativeNotifications {
    sender: mpsc::SyncSender<Request>,
    stopped: Arc<AtomicBool>,
    worker: Mutex<Option<thread::JoinHandle<()>>>,
}

pub(super) fn setup(app: AppHandle) -> Result<(), String> {
    let (sender, receiver) = mpsc::sync_channel(16);
    let stopped = Arc::new(AtomicBool::new(false));
    let stop = stopped.clone();
    let target = app.clone();
    let worker = thread::Builder::new()
        .name("desktop-notifications".into())
        .spawn(move || {
            let context = glib::MainContext::new();
            let _ = context.with_thread_default(|| run(&target, &context, receiver, stop));
        })
        .map_err(|error| error.to_string())?;
    app.manage(NativeNotifications {
        sender,
        stopped,
        worker: Mutex::new(Some(worker)),
    });
    Ok(())
}

pub(super) fn shutdown(app: &AppHandle) {
    if let Some(state) = app.try_state::<NativeNotifications>() {
        state.stopped.store(true, Ordering::Release);
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
    let sender = state.sender.clone();
    tauri::async_runtime::spawn_blocking(move || {
        let (reply, receiver) = mpsc::sync_channel(1);
        sender
            .try_send(Request {
                command,
                deadline: Instant::now() + REQUEST_TIMEOUT,
                reply,
            })
            .map_err(|_| "Desktop notifications are busy or unavailable".to_string())?;
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

pub(super) async fn send(
    app: &AppHandle,
    title: String,
    body: String,
    session_id: Option<String>,
) -> Result<(), String> {
    request(
        app,
        Command::Send {
            title,
            body,
            session_id,
        },
    )
    .await
    .map(|_| ())
}

pub(super) async fn cancel(app: &AppHandle, sessions: Vec<String>) -> Result<(), String> {
    request(app, Command::Cancel(sessions)).await.map(|_| ())
}

fn run(
    app: &AppHandle,
    context: &glib::MainContext,
    requests: mpsc::Receiver<Request>,
    stopped: Arc<AtomicBool>,
) {
    let mut delivery: Option<Delivery> = None;
    while !stopped.load(Ordering::Acquire) {
        for _ in 0..64 {
            if !context.pending() || stopped.load(Ordering::Acquire) {
                break;
            }
            context.iteration(false);
        }
        let request = match requests.recv_timeout(Duration::from_millis(25)) {
            Ok(request) => request,
            Err(mpsc::RecvTimeoutError::Timeout) => continue,
            Err(mpsc::RecvTimeoutError::Disconnected) => break,
        };
        let deadline = request.deadline;
        if stopped.load(Ordering::Acquire) || deadline <= Instant::now() {
            let _ = request
                .reply
                .send(Err("The desktop notification request expired".into()));
            continue;
        }
        if delivery
            .as_ref()
            .is_some_and(|delivery| !delivery.current(deadline))
        {
            delivery = None;
        }
        if delivery.is_none() {
            delivery = crate::linux_desktop::connect(context)
                .and_then(|connection| Delivery::new(connection, app, deadline))
                .ok();
        }
        let result = match (&request.command, &delivery) {
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
                .map(|_| String::new()),
            (Command::Cancel(sessions), Some(delivery)) => {
                delivery.cancel(sessions, deadline).map(|_| String::new())
            }
            _ => Err("The desktop notification service is unavailable".into()),
        };
        let _ = request.reply.send(result);
    }
}
