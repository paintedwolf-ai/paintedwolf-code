//! Primary fixed Settings observer; the hardened shell never retries a denied portal identity.
use super::{DesktopSettingsState, Emission, SettingValue, SettingsSnapshot};
use crate::windows::accessibility_text_size::{TextSizePayload, TEXT_SIZE_CHANGED_EVENT};
use crate::windows::linux_decorations::{DecorationLayout, LAYOUT_CHANGED_EVENT};
use crate::windows::system_appearance::{SystemAppearancePayload, SYSTEM_APPEARANCE_CHANGED_EVENT};
use desktop_settings_observer::{process, Frame, Setting, Status};
use serde::Serialize;
use std::sync::{
    atomic::{AtomicBool, Ordering},
    Arc, Mutex, MutexGuard,
};
use std::thread::JoinHandle;
use tauri::{AppHandle, Emitter, Manager};
const STATUS_EVENT: &str = "desktop-settings://observation";
struct Observation {
    settings: DesktopSettingsState,
    status: Status,
    generation: u64,
}
static STATE: Mutex<Observation> = Mutex::new(Observation {
    settings: DesktopSettingsState::new(),
    status: Status::Missing,
    generation: 0,
});
fn state() -> MutexGuard<'static, Observation> {
    STATE
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner())
}
#[derive(Clone, Serialize)]
#[serde(rename_all = "snake_case")]
struct ObservationStatus {
    status: Status,
    generation: u64,
}
pub(crate) struct Observer {
    stop: Arc<AtomicBool>,
    worker: Mutex<Option<JoinHandle<()>>>,
}
impl Observer {
    fn shutdown(&self) {
        self.stop.store(true, Ordering::Release);
        if let Some(worker) = self.worker.lock().unwrap_or_else(|p| p.into_inner()).take() {
            let _ = worker.join();
        }
    }
}
impl Drop for Observer {
    fn drop(&mut self) {
        self.shutdown();
    }
}
pub(crate) fn current_text_size_payload() -> TextSizePayload {
    state().settings.text_size_payload()
}
pub(crate) fn current_appearance_payload() -> SystemAppearancePayload {
    state().settings.appearance_payload()
}
pub(crate) fn current_decoration_layout() -> DecorationLayout {
    state().settings.decoration_layout()
}
pub(crate) fn shutdown(app: &AppHandle) {
    if let Some(observer) = app.try_state::<Observer>() {
        observer.shutdown();
    }
}
pub(crate) fn setup(app: AppHandle) -> Result<(), String> {
    let stop = Arc::new(AtomicBool::new(false));
    let requested = stop.clone();
    let running = app.clone();
    let worker = std::thread::Builder::new()
        .name("desktop-settings".into())
        .spawn(move || {
            let shell = match std::env::current_exe() {
                Ok(path) => path,
                Err(_) => {
                    source_status(&running, Status::Failed, false);
                    return;
                }
            };
            let changed = running.clone();
            let failed = running.clone();
            process::watch(
                &shell,
                requested,
                move |frame| apply(&changed, frame),
                move |status| source_status(&failed, status, false),
            );
        })
        .map_err(|error| format!("desktop settings observer: {error}"))?;
    if !app.manage(Observer {
        stop,
        worker: Mutex::new(Some(worker)),
    }) {
        return Err("desktop settings observer already started".into());
    }
    Ok(())
}
fn source_status(app: &AppHandle, status: Status, restarted: bool) {
    let payload = {
        let mut state = state();
        if restarted {
            state.generation = state.generation.saturating_add(1);
        }
        if !restarted && state.status == status {
            return;
        }
        state.status = status;
        ObservationStatus {
            status,
            generation: state.generation,
        }
    };
    if status != Status::Ready {
        eprintln!("desktop settings observation: {status:?}");
    }
    if let Err(error) = app.emit(STATUS_EVENT, &payload) {
        eprintln!("desktop settings status emit: {error}");
    }
}
fn apply(app: &AppHandle, frame: Frame) {
    source_status(app, frame.status, frame.sequence == 1);
    if frame.status != Status::Ready {
        return;
    }
    let mut snapshot = SettingsSnapshot::new();
    for setting in frame.settings {
        let (namespace, key) = setting.identity();
        let value = match setting {
            Setting::Contrast(value) => SettingValue::U32(value),
            Setting::TextScale(value) => SettingValue::F64(value),
            Setting::HighContrast(value) => SettingValue::Bool(value),
            Setting::Accent([red, green, blue]) => SettingValue::Rgb(red, green, blue),
            Setting::KdeAccent(value) | Setting::ButtonLayout(value) => SettingValue::Str(value),
        };
        snapshot
            .entry(namespace.into())
            .or_default()
            .insert(key.into(), value);
    }
    emit(app, state().settings.replace_snapshot(snapshot));
}
fn emit(app: &AppHandle, emissions: Vec<Emission>) {
    for emission in emissions {
        let result = match emission {
            Emission::TextSize(payload) => app.emit(TEXT_SIZE_CHANGED_EVENT, &payload),
            Emission::Appearance(payload) => app.emit(SYSTEM_APPEARANCE_CHANGED_EVENT, &payload),
            Emission::DecorationLayout(layout) => app.emit(LAYOUT_CHANGED_EVENT, &layout),
        };
        if let Err(err) = result {
            eprintln!("desktop settings: emit: {err}");
        }
    }
}
