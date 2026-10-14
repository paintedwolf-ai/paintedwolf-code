//! Serialized clipboard operations over a platform adapter.
//!
//! One service serves every window. Operations run one at a time, each
//! bounded by a timeout, and shutdown is terminal: nothing after it can
//! recreate ownership.

use std::fmt;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

use serde::Serialize;
use tokio::sync::{oneshot, Mutex};

pub(crate) const OPERATION_TIMEOUT: Duration = Duration::from_secs(5);

/// Why a clipboard command could not complete.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case", tag = "code", content = "message")]
pub enum ClipboardError {
    /// The service has shut down with the application.
    Stopped,
    /// No native display connection serves the clipboard.
    DisplayUnavailable,
    /// The display did not answer within the operation bound.
    Timeout,
    /// The platform reported a fault.
    Backend(String),
}

impl fmt::Display for ClipboardError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Stopped => f.write_str("Clipboard service has stopped"),
            Self::DisplayUnavailable => f.write_str("Clipboard display is unavailable"),
            Self::Timeout => f.write_str("Clipboard did not respond in time"),
            Self::Backend(message) => f.write_str(message),
        }
    }
}

/// The display protocol the clipboard adapter is attached to.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum DisplayProtocol {
    Wayland,
    X11,
    /// Another GDK backend, such as Broadway.
    Gdk,
    /// The operating system pasteboard on macOS and Windows.
    System,
}

impl DisplayProtocol {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Wayland => "wayland",
            Self::X11 => "x11",
            Self::Gdk => "gdk",
            Self::System => "system",
        }
    }
}

/// Observed clipboard capabilities of the current display connection.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ClipboardFacts {
    pub protocol: DisplayProtocol,
    /// Copied text outlives this process; false without a clipboard manager.
    pub persistence: bool,
}

/// Runs toolkit work on the thread that owns the display connection.
pub(crate) trait MainThread: Sync {
    fn dispatch(&self, task: Box<dyn FnOnce() + Send + 'static>) -> Result<(), ClipboardError>;
}

impl MainThread for tauri::AppHandle {
    fn dispatch(&self, task: Box<dyn FnOnce() + Send + 'static>) -> Result<(), ClipboardError> {
        self.run_on_main_thread(task)
            .map_err(|_| ClipboardError::Stopped)
    }
}

/// The eventual answer of one adapter operation.
pub(crate) type Reply<T> = oneshot::Receiver<Result<T, ClipboardError>>;

pub(crate) trait ClipboardAdapter: Default + Send {
    fn write_text(&mut self, main: &dyn MainThread, text: String) -> Reply<()>;
    fn read_text(&mut self, main: &dyn MainThread) -> Reply<String>;
    /// Hands the selection to the platform before the owner goes away.
    fn release(&mut self, main: &dyn MainThread);
}

pub(crate) struct ClipboardService<A> {
    closed: AtomicBool,
    released: AtomicBool,
    adapter: Mutex<A>,
    timeout: Duration,
}

impl<A: ClipboardAdapter> Default for ClipboardService<A> {
    fn default() -> Self {
        Self::new(A::default(), OPERATION_TIMEOUT)
    }
}

impl<A: ClipboardAdapter> ClipboardService<A> {
    pub(crate) fn new(adapter: A, timeout: Duration) -> Self {
        Self {
            closed: AtomicBool::new(false),
            released: AtomicBool::new(false),
            adapter: Mutex::new(adapter),
            timeout,
        }
    }

    pub(crate) async fn write(
        &self,
        main: &dyn MainThread,
        text: String,
    ) -> Result<(), ClipboardError> {
        self.run(main, move |adapter, main| adapter.write_text(main, text))
            .await
    }

    pub(crate) async fn read(&self, main: &dyn MainThread) -> Result<String, ClipboardError> {
        self.run(main, |adapter, main| adapter.read_text(main)).await
    }

    async fn run<T>(
        &self,
        main: &dyn MainThread,
        operation: impl FnOnce(&mut A, &dyn MainThread) -> Reply<T>,
    ) -> Result<T, ClipboardError> {
        if self.closed.load(Ordering::Acquire) {
            return Err(ClipboardError::Stopped);
        }
        let mut adapter = self.adapter.lock().await;
        if self.closed.load(Ordering::Acquire) {
            return Err(ClipboardError::Stopped);
        }
        let reply = operation(&mut adapter, main);
        let result = match tokio::time::timeout(self.timeout, reply).await {
            Ok(Ok(result)) => result,
            // The owning thread dropped the task without answering.
            Ok(Err(_)) => Err(ClipboardError::Stopped),
            Err(_) => Err(ClipboardError::Timeout),
        };
        // A shutdown that arrived during this operation deferred its release here.
        if self.closed.load(Ordering::Acquire) {
            self.release_once(&mut adapter, main);
        }
        result
    }

    /// Ends the service; an operation in flight releases when it settles.
    pub(crate) fn shutdown(&self, main: &dyn MainThread) {
        if self.closed.swap(true, Ordering::AcqRel) {
            return;
        }
        if let Ok(mut adapter) = self.adapter.try_lock() {
            self.release_once(&mut adapter, main);
        }
    }

    fn release_once(&self, adapter: &mut A, main: &dyn MainThread) {
        if !self.released.swap(true, Ordering::AcqRel) {
            adapter.release(main);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::{Arc, Mutex as StdMutex};

    struct Inline;

    impl MainThread for Inline {
        fn dispatch(
            &self,
            task: Box<dyn FnOnce() + Send + 'static>,
        ) -> Result<(), ClipboardError> {
            task();
            Ok(())
        }
    }

    #[derive(Default)]
    struct Recorded {
        text: String,
        calls: Vec<String>,
        held: Vec<oneshot::Sender<Result<(), ClipboardError>>>,
        hold_writes: bool,
        drop_replies: bool,
        fail_with: Option<ClipboardError>,
        released: usize,
    }

    #[derive(Default, Clone)]
    struct Fake(Arc<StdMutex<Recorded>>);

    impl Fake {
        fn state(&self) -> std::sync::MutexGuard<'_, Recorded> {
            self.0.lock().unwrap()
        }

        fn calls(&self) -> Vec<String> {
            self.state().calls.clone()
        }

        fn answer_held(&self) {
            for reply in self.state().held.drain(..) {
                let _ = reply.send(Ok(()));
            }
        }
    }

    fn settled<T>(result: Result<T, ClipboardError>) -> Reply<T> {
        let (tx, rx) = oneshot::channel();
        let _ = tx.send(result);
        rx
    }

    impl ClipboardAdapter for Fake {
        fn write_text(&mut self, _main: &dyn MainThread, text: String) -> Reply<()> {
            let mut state = self.state();
            state.calls.push(format!("write {text}"));
            if let Some(error) = state.fail_with.clone() {
                return settled(Err(error));
            }
            let (tx, rx) = oneshot::channel();
            if state.drop_replies {
                drop(tx);
            } else if state.hold_writes {
                state.text = text;
                state.held.push(tx);
            } else {
                state.text = text;
                let _ = tx.send(Ok(()));
            }
            rx
        }

        fn read_text(&mut self, _main: &dyn MainThread) -> Reply<String> {
            let mut state = self.state();
            state.calls.push("read".into());
            match state.fail_with.clone() {
                Some(error) => settled(Err(error)),
                None => settled(Ok(state.text.clone())),
            }
        }

        fn release(&mut self, _main: &dyn MainThread) {
            let mut state = self.state();
            state.calls.push("release".into());
            state.released += 1;
        }
    }

    fn service(fake: &Fake, timeout: Duration) -> Arc<ClipboardService<Fake>> {
        Arc::new(ClipboardService::new(fake.clone(), timeout))
    }

    fn block_on<F: std::future::Future>(future: F) -> F::Output {
        tauri::async_runtime::block_on(future)
    }

    async fn settle() {
        tokio::time::sleep(Duration::from_millis(40)).await;
    }

    #[test]
    fn writes_and_reads_pass_through_the_adapter() {
        let fake = Fake::default();
        let service = service(&fake, OPERATION_TIMEOUT);
        block_on(async {
            service.write(&Inline, "copied λ text".into()).await.unwrap();
            assert_eq!(service.read(&Inline).await.unwrap(), "copied λ text");
        });
        assert_eq!(fake.calls(), ["write copied λ text", "read"]);
        assert_eq!(fake.state().released, 0);
    }

    #[test]
    fn operations_from_several_windows_run_one_at_a_time() {
        let fake = Fake::default();
        fake.state().hold_writes = true;
        let service = service(&fake, OPERATION_TIMEOUT);
        block_on(async {
            let first = tauri::async_runtime::spawn({
                let service = service.clone();
                async move { service.write(&Inline, "first".into()).await }
            });
            settle().await;
            let second = tauri::async_runtime::spawn({
                let service = service.clone();
                async move { service.write(&Inline, "second".into()).await }
            });
            settle().await;
            assert_eq!(fake.calls(), ["write first"]);
            fake.answer_held();
            first.await.unwrap().unwrap();
            settle().await;
            assert_eq!(fake.calls(), ["write first", "write second"]);
            fake.answer_held();
            second.await.unwrap().unwrap();
        });
    }

    #[test]
    fn unanswered_operations_time_out_and_leave_the_service_usable() {
        let fake = Fake::default();
        fake.state().hold_writes = true;
        let service = service(&fake, Duration::from_millis(30));
        block_on(async {
            let result = service.write(&Inline, "slow".into()).await;
            assert_eq!(result, Err(ClipboardError::Timeout));
            fake.state().hold_writes = false;
            service.write(&Inline, "quick".into()).await.unwrap();
            assert_eq!(service.read(&Inline).await.unwrap(), "quick");
        });
    }

    #[test]
    fn dropped_replies_report_the_service_as_stopped() {
        let fake = Fake::default();
        fake.state().drop_replies = true;
        let service = service(&fake, OPERATION_TIMEOUT);
        block_on(async {
            assert_eq!(
                service.write(&Inline, "lost".into()).await,
                Err(ClipboardError::Stopped)
            );
        });
    }

    #[test]
    fn adapter_faults_pass_through_without_closing_the_service() {
        let fake = Fake::default();
        fake.state().fail_with = Some(ClipboardError::DisplayUnavailable);
        let service = service(&fake, OPERATION_TIMEOUT);
        block_on(async {
            assert_eq!(
                service.read(&Inline).await,
                Err(ClipboardError::DisplayUnavailable)
            );
            fake.state().fail_with = None;
            service.write(&Inline, "later".into()).await.unwrap();
        });
    }

    #[test]
    fn shutdown_is_terminal_idempotent_and_releases_once() {
        let fake = Fake::default();
        let service = service(&fake, OPERATION_TIMEOUT);
        service.shutdown(&Inline);
        service.shutdown(&Inline);
        block_on(async {
            assert_eq!(
                service.write(&Inline, "after exit".into()).await,
                Err(ClipboardError::Stopped)
            );
            assert_eq!(service.read(&Inline).await, Err(ClipboardError::Stopped));
        });
        assert_eq!(fake.calls(), ["release"]);
    }

    #[test]
    fn shutdown_during_an_operation_releases_when_it_settles() {
        let fake = Fake::default();
        fake.state().hold_writes = true;
        let service = service(&fake, OPERATION_TIMEOUT);
        block_on(async {
            let pending = tauri::async_runtime::spawn({
                let service = service.clone();
                async move { service.write(&Inline, "in flight".into()).await }
            });
            settle().await;
            service.shutdown(&Inline);
            assert_eq!(fake.state().released, 0);
            fake.answer_held();
            pending.await.unwrap().unwrap();
            assert_eq!(fake.state().released, 1);
            service.shutdown(&Inline);
            assert_eq!(fake.state().released, 1);
            assert_eq!(
                service.write(&Inline, "too late".into()).await,
                Err(ClipboardError::Stopped)
            );
        });
        assert_eq!(fake.calls(), ["write in flight", "release"]);
    }

    #[test]
    fn errors_serialize_as_structured_values_and_protocols_name_themselves() {
        assert_eq!(
            serde_json::to_value(ClipboardError::Stopped).unwrap(),
            serde_json::json!({ "code": "stopped" })
        );
        assert_eq!(
            serde_json::to_value(ClipboardError::DisplayUnavailable).unwrap(),
            serde_json::json!({ "code": "display_unavailable" })
        );
        assert_eq!(
            serde_json::to_value(ClipboardError::Backend("fault".into())).unwrap(),
            serde_json::json!({ "code": "backend", "message": "fault" })
        );
        assert_eq!(
            [
                DisplayProtocol::Wayland,
                DisplayProtocol::X11,
                DisplayProtocol::Gdk,
                DisplayProtocol::System,
            ]
            .map(DisplayProtocol::as_str),
            ["wayland", "x11", "gdk", "system"]
        );
        assert_eq!(
            ClipboardError::Timeout.to_string(),
            "Clipboard did not respond in time"
        );
    }
}
