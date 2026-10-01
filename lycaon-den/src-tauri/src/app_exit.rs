//! Every live window preserves its work before application exit or installation.
use serde::Serialize;
use std::collections::BTreeSet;
use std::sync::Mutex;
use std::time::Duration;
use tauri::{AppHandle, Emitter, Manager, WebviewWindow};

const REQUEST: &str = "app-exit-requested";
const CANCEL: &str = "app-exit-cancelled";

#[derive(Clone, Copy, Serialize)]
pub struct ExitRequest {
    request_id: u64,
}

#[derive(Default)]
struct ExitState {
    next: u64,
    pending: Option<ExitRequest>,
    ready: BTreeSet<String>,
    approved: bool,
    opening: usize,
}

impl ExitState {
    fn start_window(&mut self) -> bool {
        if self.pending.is_some() || self.approved {
            return false;
        }
        self.opening += 1;
        true
    }

    fn begin(&mut self) -> Option<ExitRequest> {
        if self.pending.is_some() || self.approved {
            return None;
        }
        self.next += 1;
        let request = ExitRequest {
            request_id: self.next,
        };
        self.pending = Some(request);
        self.ready.clear();
        Some(request)
    }

    fn acknowledge(&mut self, label: &str, request_id: u64) {
        if self
            .pending
            .is_some_and(|request| request.request_id == request_id)
        {
            self.ready.insert(label.into());
        }
    }

    fn missing(&mut self, labels: &BTreeSet<String>) -> Vec<String> {
        self.ready.retain(|label| labels.contains(label));
        labels.difference(&self.ready).cloned().collect()
    }

    fn cancel(&mut self, request_id: u64) -> bool {
        if !self
            .pending
            .is_some_and(|request| request.request_id == request_id)
        {
            return false;
        }
        self.pending = None;
        self.ready.clear();
        true
    }
}

#[derive(Default)]
pub struct ExitCoordinator(Mutex<ExitState>);

pub struct WindowCreation(AppHandle);

impl Drop for WindowCreation {
    fn drop(&mut self) {
        if let Ok(mut state) = self.0.state::<ExitCoordinator>().0.lock() {
            state.opening -= 1;
        }
    }
}

// Admission closes before the barrier snapshots windows. A creation already in
// flight must finish and join preservation before exit can be approved.
pub fn begin_window(app: &AppHandle) -> Result<WindowCreation, String> {
    if !app
        .state::<ExitCoordinator>()
        .0
        .lock()
        .map_err(|_| "Application preservation lock failed.")?
        .start_window()
    {
        return Err("The application is preserving work before exit.".into());
    }
    Ok(WindowCreation(app.clone()))
}

// A failed installation automatically gives the windows their resources and
// input back. Success consumes the barrier only when restart is committed.
pub struct PreparedExit {
    app: AppHandle,
    request: ExitRequest,
    committed: bool,
}

impl PreparedExit {
    async fn wait(&self) -> Result<(), String> {
        loop {
            let (missing, opening) = {
                let coordinator = self.app.state::<ExitCoordinator>();
                let mut state = coordinator
                    .0
                    .lock()
                    .map_err(|_| "Application preservation lock failed.")?;
                let labels = self.app.webview_windows().keys().cloned().collect();
                (state.missing(&labels), state.opening)
            };
            if missing.is_empty() && opening == 0 {
                return Ok(());
            }
            for label in missing {
                // A listener may still be starting. Repeated requests and the
                // query command cover that race without an unsafe exit timeout.
                let _ = self.app.emit_to(label, REQUEST, self.request);
            }
            tokio::time::sleep(Duration::from_secs(1)).await;
        }
    }

    fn approve(&mut self) -> Result<(), String> {
        let coordinator = self.app.state::<ExitCoordinator>();
        let mut state = coordinator
            .0
            .lock()
            .map_err(|_| "Application preservation lock failed.")?;
        if !state
            .pending
            .is_some_and(|request| request.request_id == self.request.request_id)
        {
            return Err("Application preservation request changed.".into());
        }
        state.approved = true;
        self.committed = true;
        Ok(())
    }

    pub fn restart(mut self) -> Result<(), String> {
        self.approve()?;
        self.app.request_restart();
        Ok(())
    }

    fn exit(mut self, code: i32) -> Result<(), String> {
        self.approve()?;
        self.app.exit(code);
        Ok(())
    }
}

impl Drop for PreparedExit {
    fn drop(&mut self) {
        if self.committed {
            return;
        }
        let coordinator = self.app.state::<ExitCoordinator>();
        let cancelled = coordinator
            .0
            .lock()
            .is_ok_and(|mut state| state.cancel(self.request.request_id));
        if cancelled {
            let _ = self.app.emit(CANCEL, self.request);
        }
    }
}

fn begin(app: &AppHandle) -> Result<Option<PreparedExit>, String> {
    let request = app
        .state::<ExitCoordinator>()
        .0
        .lock()
        .map_err(|_| "Application preservation lock failed.")?
        .begin();
    Ok(request.map(|request| PreparedExit {
        app: app.clone(),
        request,
        committed: false,
    }))
}

// The updater calls this after download, before the installer can terminate the
// process (including Windows installers that do not emit a Tauri exit event).
pub async fn prepare_install(app: &AppHandle) -> Result<PreparedExit, String> {
    let prepared = begin(app)?.ok_or("An application exit is already in progress.")?;
    prepared.wait().await?;
    Ok(prepared)
}

pub fn allow_exit(app: &AppHandle, code: Option<i32>) -> bool {
    if app
        .state::<ExitCoordinator>()
        .0
        .lock()
        .is_ok_and(|state| state.approved)
    {
        return true;
    }
    if let Ok(Some(prepared)) = begin(app) {
        tauri::async_runtime::spawn(async move {
            if prepared.wait().await.is_ok() {
                let _ = prepared.exit(code.unwrap_or(0));
            }
        });
    }
    false
}

#[tauri::command]
pub fn pending_app_exit(app: AppHandle) -> Result<Option<ExitRequest>, String> {
    let coordinator = app.state::<ExitCoordinator>();
    let state = coordinator
        .0
        .lock()
        .map_err(|_| "Application preservation lock failed.")?;
    Ok(state.pending)
}

#[tauri::command]
pub fn acknowledge_app_exit(
    app: AppHandle,
    window: WebviewWindow,
    request_id: u64,
) -> Result<(), String> {
    app.state::<ExitCoordinator>()
        .0
        .lock()
        .map_err(|_| "Application preservation lock failed.")?
        .acknowledge(window.label(), request_id);
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn every_current_window_must_acknowledge_the_current_request() {
        let mut state = ExitState::default();
        let first = state.begin().expect("begin exit");
        let labels = BTreeSet::from(["main".into(), "file:one".into()]);
        state.acknowledge("main", first.request_id + 1);
        assert_eq!(state.missing(&labels).len(), 2);
        state.acknowledge("main", first.request_id);
        assert_eq!(state.missing(&labels), vec!["file:one"]);
        state.acknowledge("file:one", first.request_id);
        assert!(state.missing(&labels).is_empty());
        let expanded = BTreeSet::from(["main".into(), "file:one".into(), "file:two".into()]);
        assert_eq!(state.missing(&expanded), vec!["file:two"]);
    }

    #[test]
    fn window_creation_admission_closes_when_preservation_begins() {
        let mut state = ExitState::default();
        assert!(state.start_window());
        assert_eq!(state.opening, 1);
        let request = state.begin().expect("begin while a window is opening");
        assert!(!state.start_window());
        assert_eq!(state.opening, 1);
        assert!(state.cancel(request.request_id));
        assert!(state.start_window());
    }

    #[test]
    fn cancelled_installation_cannot_reuse_old_acknowledgements() {
        let mut state = ExitState::default();
        let first = state.begin().expect("begin install");
        assert!(state.begin().is_none());
        state.acknowledge("main", first.request_id);
        assert!(state.cancel(first.request_id));
        let second = state.begin().expect("begin next exit");
        state.acknowledge("main", first.request_id);
        assert!(!state.cancel(first.request_id));
        assert_eq!(
            state.missing(&BTreeSet::from(["main".into()])),
            vec!["main"]
        );
        state.acknowledge("main", second.request_id);
        assert!(state.missing(&BTreeSet::from(["main".into()])).is_empty());
    }

    #[test]
    fn destroyed_windows_do_not_hold_exit_or_lend_their_acknowledgement() {
        let mut state = ExitState::default();
        let request = state.begin().expect("begin exit");
        state.acknowledge("file:one", request.request_id);
        assert!(state.missing(&BTreeSet::new()).is_empty());
        assert_eq!(
            state.missing(&BTreeSet::from(["file:one".into()])),
            vec!["file:one"]
        );
    }
}
