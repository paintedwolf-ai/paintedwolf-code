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
    committing: bool,
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

    fn preserving(&mut self, request_id: u64, labels: &BTreeSet<String>) -> bool {
        !self.committing
            && !self.approved
            && self
                .pending
                .is_some_and(|pending| pending.request_id == request_id)
            && (self.opening > 0 || !self.missing(labels).is_empty())
    }

    fn cancel(&mut self, request_id: u64) -> bool {
        if !self
            .pending
            .is_some_and(|request| request.request_id == request_id)
        {
            return false;
        }
        self.pending = None;
        self.committing = false;
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
    async fn wait(&self, prompt: bool) -> Result<(), String> {
        let app = self.app.clone();
        let request = self.request;
        if prompt {
            tauri::async_runtime::spawn(async move {
                loop {
                    tokio::time::sleep(Duration::from_secs(5)).await;
                    let labels = app.webview_windows().keys().cloned().collect();
                    let waiting = app
                        .state::<ExitCoordinator>()
                        .0
                        .lock()
                        .is_ok_and(|mut state| state.preserving(request.request_id, &labels));
                    if !waiting {
                        return;
                    }
                    let result = rfd::AsyncMessageDialog::new()
                .set_title("Preserving your work")
                .set_description("Painted Wolf Code is still preserving work before exit. You can keep waiting or return to your workspace.")
                .set_buttons(rfd::MessageButtons::OkCancelCustom("Keep waiting".into(), "Keep working".into()))
                .show().await;
                    if result == rfd::MessageDialogResult::Custom("Keep working".into()) {
                        let _ = cancel_app_exit(app.clone(), request.request_id);
                        return;
                    }
                }
            });
        }
        loop {
            let (missing, opening) = {
                let coordinator = self.app.state::<ExitCoordinator>();
                let mut state = coordinator
                    .0
                    .lock()
                    .map_err(|_| "Application preservation lock failed.")?;
                if !state
                    .pending
                    .is_some_and(|request| request.request_id == self.request.request_id)
                {
                    return Err("Application exit was cancelled.".into());
                }
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

    fn commit_boundary(&self) -> Result<(), String> {
        let coordinator = self.app.state::<ExitCoordinator>();
        let mut state = coordinator
            .0
            .lock()
            .map_err(|_| "Application preservation lock failed.")?;
        if !state
            .pending
            .is_some_and(|request| request.request_id == self.request.request_id)
        {
            return Err("Application exit was cancelled.".into());
        }
        state.committing = true;
        Ok(())
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

pub async fn install_update(
    app: &AppHandle,
    expected: Option<&str>,
    launch: bool,
) -> Result<bool, crate::update_service::UpdateError> {
    use crate::update_service::{transaction, UpdateError, UpdateErrorCode, UpdateService};
    if expected.is_none() && !app.state::<UpdateService>().automatic_install_ready().await {
        return Ok(false);
    }
    let prepared = begin(app)
        .map_err(|e| UpdateError::new(UpdateErrorCode::StateUnavailable, e))?
        .ok_or(UpdateError::from(UpdateErrorCode::InvalidTransition))?;
    let wait = async {
        prepared
            .wait(!launch)
            .await
            .map_err(|e| UpdateError::new(UpdateErrorCode::Cancelled, e))
    };
    if launch {
        tokio::time::timeout(Duration::from_secs(30), wait)
            .await
            .map_err(|_| {
                UpdateError::new(
                    UpdateErrorCode::Cancelled,
                    "Startup update deferred because windows did not finish preserving work.",
                )
            })??;
    } else {
        wait.await?;
    }
    let Some(activation) = transaction::prepare_exit(app, expected, true).await? else {
        return Ok(false);
    };
    activation.revalidate(app).await?;
    prepared
        .commit_boundary()
        .map_err(|e| UpdateError::new(UpdateErrorCode::Cancelled, e))?;
    if let Err(error) = stop_and_activate(app, activation).await {
        drop(prepared);
        transaction::resume(app, Some(error.clone())).await;
        app.state::<UpdateService>().finish_startup(app).await;
        if !launch {
            let _ = app.emit("update-resume-engine", ());
        }
        return Err(error);
    }
    prepared
        .exit(0)
        .map_err(|e| UpdateError::new(UpdateErrorCode::StateUnavailable, e))?;
    Ok(true)
}
async fn stop_and_activate(
    app: &AppHandle,
    activation: crate::update_service::transaction::Activation,
) -> Result<(), crate::update_service::UpdateError> {
    use crate::update_service::{UpdateError, UpdateErrorCode};
    app.state::<crate::update_service::UpdateService>()
        .close_startup()
        .await;
    let handle = app.clone();
    let stopped = tauri::async_runtime::spawn_blocking(move || {
        let state = handle.state::<crate::SidecarState>();
        crate::vault_lock::lock_now(&state, crate::vault_lock::LockReason::AppQuit);
        crate::sidecar::stop_for_update(&state)
    })
    .await
    .map_err(|e| UpdateError::new(UpdateErrorCode::EngineStopFailed, e))?;
    if !stopped {
        return Err(UpdateErrorCode::EngineStopFailed.into());
    }
    activation.commit()
}
#[tauri::command]
pub fn cancel_app_exit(app: AppHandle, request_id: u64) -> Result<(), String> {
    let coordinator = app.state::<ExitCoordinator>();
    let mut state = coordinator
        .0
        .lock()
        .map_err(|_| "Application preservation lock failed.")?;
    if !state.approved && !state.committing && state.cancel(request_id) {
        let _ = app.emit(CANCEL, ExitRequest { request_id });
    }
    Ok(())
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
            if prepared.wait(true).await.is_err() {
                return;
            }
            match crate::update_service::transaction::prepare_exit(&prepared.app, None, false).await
            {
                Ok(Some(activation)) => {
                    if prepared.commit_boundary().is_err() {
                        crate::update_service::transaction::resume(
                            &prepared.app,
                            Some(crate::update_service::UpdateErrorCode::Cancelled.into()),
                        )
                        .await;
                        return;
                    }
                    if let Err(error) = stop_and_activate(&prepared.app, activation).await {
                        crate::update_service::transaction::resume(&prepared.app, Some(error))
                            .await;
                    }
                }
                Err(error) => {
                    crate::update_service::transaction::resume(&prepared.app, Some(error)).await
                }
                Ok(None) => {}
            }
            let _ = prepared.exit(code.unwrap_or(0));
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
    fn preservation_prompt_stops_after_acknowledgements_and_ignores_old_requests() {
        let mut state = ExitState::default();
        let request = state.begin().unwrap();
        let labels = BTreeSet::from(["main".into()]);
        assert!(state.preserving(request.request_id, &labels));
        state.acknowledge("main", request.request_id);
        assert!(!state.preserving(request.request_id, &labels));
        state.opening = 1;
        assert!(state.preserving(request.request_id, &labels));
        state.cancel(request.request_id);
        state.begin().unwrap();
        assert!(!state.preserving(request.request_id, &labels));
    }

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
