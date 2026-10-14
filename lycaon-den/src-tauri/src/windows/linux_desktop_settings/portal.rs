//! Session-bus reads and change tracking for `org.freedesktop.portal.Settings`.

use std::sync::{Mutex, MutexGuard};
use std::time::Duration;

use gtk::gio;
use gtk::glib::{self, ToVariant, Variant, VariantTy};
use tauri::{AppHandle, Emitter};

use super::{DesktopSettingsState, Emission, SettingValue, SettingsSnapshot, NAMESPACES};
use crate::windows::accessibility_text_size::{TextSizePayload, TEXT_SIZE_CHANGED_EVENT};
use crate::windows::system_appearance::{SystemAppearancePayload, SYSTEM_APPEARANCE_CHANGED_EVENT};

const PORTAL_BUS_NAME: &str = "org.freedesktop.portal.Desktop";
const PORTAL_OBJECT_PATH: &str = "/org/freedesktop/portal/desktop";
const SETTINGS_INTERFACE: &str = "org.freedesktop.portal.Settings";
const BUS_TIMEOUT: Duration = Duration::from_millis(1500);

static STATE: Mutex<DesktopSettingsState> = Mutex::new(DesktopSettingsState::new());

fn state() -> MutexGuard<'static, DesktopSettingsState> {
    STATE.lock().unwrap_or_else(|poisoned| poisoned.into_inner())
}

pub(crate) fn current_text_size_payload() -> TextSizePayload {
    state().text_size_payload()
}

pub(crate) fn current_appearance_payload() -> SystemAppearancePayload {
    state().appearance_payload()
}

/// Reads the portal on its own thread; the main loop keeps the GTK thread free.
pub(crate) fn setup(app: AppHandle) -> Result<(), String> {
    std::thread::Builder::new()
        .name("desktop-settings".into())
        .spawn(move || watch(app))
        .map(|_| ())
        .map_err(|err| format!("desktop settings thread: {err}"))
}

fn watch(app: AppHandle) {
    let context = glib::MainContext::new();
    let outcome = context.with_thread_default(|| {
        let connection = connect(&context)?;
        let snapshot = read_all(&context, &connection)?;
        emit(&app, state().replace_snapshot(snapshot));
        let app = app.clone();
        connection.signal_subscribe(
            Some(PORTAL_BUS_NAME),
            Some(SETTINGS_INTERFACE),
            Some("SettingChanged"),
            Some(PORTAL_OBJECT_PATH),
            None,
            gio::DBusSignalFlags::NONE,
            move |_, _, _, _, _, parameters| {
                if let Some((namespace, key, value)) = parse_setting_changed(parameters) {
                    emit(&app, state().apply_change(&namespace, &key, value));
                }
            },
        );
        glib::MainLoop::new(Some(&context), false).run();
        Ok::<(), String>(())
    });
    match outcome {
        Ok(Ok(())) => {}
        Ok(Err(err)) => eprintln!("desktop settings: {err}"),
        Err(err) => eprintln!("desktop settings: main context: {err}"),
    }
}

fn connect(context: &glib::MainContext) -> Result<gio::DBusConnection, String> {
    context
        .block_on(glib::future_with_timeout(
            BUS_TIMEOUT,
            gio::bus_get_future(gio::BusType::Session),
        ))
        .map_err(|_| "session bus connection timed out".to_string())?
        .map_err(|err| format!("session bus: {err}"))
}

fn read_all(
    context: &glib::MainContext,
    connection: &gio::DBusConnection,
) -> Result<SettingsSnapshot, String> {
    let namespaces: Vec<&str> = NAMESPACES.to_vec();
    let reply = context
        .block_on(glib::future_with_timeout(
            BUS_TIMEOUT,
            connection.call_future(
                Some(PORTAL_BUS_NAME),
                PORTAL_OBJECT_PATH,
                SETTINGS_INTERFACE,
                "ReadAll",
                Some(&(namespaces,).to_variant()),
                Some(VariantTy::new("(a{sa{sv}})").map_err(|err| err.to_string())?),
                gio::DBusCallFlags::NONE,
                BUS_TIMEOUT.as_millis() as i32,
            ),
        ))
        .map_err(|_| "settings portal read timed out".to_string())?
        .map_err(|err| format!("settings portal: {err}"))?;
    Ok(snapshot_from_variant(&reply.child_value(0)))
}

/// Flattens `a{sa{sv}}` into the snapshot map.
fn snapshot_from_variant(namespaces: &Variant) -> SettingsSnapshot {
    let mut snapshot = SettingsSnapshot::new();
    for entry in namespaces.iter() {
        let Some(namespace) = entry.child_value(0).str().map(str::to_string) else {
            continue;
        };
        let keys = snapshot.entry(namespace).or_default();
        for setting in entry.child_value(1).iter() {
            let Some(key) = setting.child_value(0).str().map(str::to_string) else {
                continue;
            };
            keys.insert(key, setting_value(&setting.child_value(1)));
        }
    }
    snapshot
}

/// `SettingChanged` carries `(ssv)`.
fn parse_setting_changed(parameters: &Variant) -> Option<(String, String, SettingValue)> {
    if parameters.n_children() != 3 {
        return None;
    }
    let namespace = parameters.child_value(0).str()?.to_string();
    let key = parameters.child_value(1).str()?.to_string();
    Some((namespace, key, setting_value(&parameters.child_value(2))))
}

/// Unwraps a boxed `v` and maps the few types the facts read.
fn setting_value(boxed: &Variant) -> SettingValue {
    let value = boxed.as_variant().unwrap_or_else(|| boxed.clone());
    match value.type_().as_str() {
        "b" => value.get::<bool>().map_or(SettingValue::Unsupported, SettingValue::Bool),
        "u" => value.get::<u32>().map_or(SettingValue::Unsupported, SettingValue::U32),
        "d" => value.get::<f64>().map_or(SettingValue::Unsupported, SettingValue::F64),
        "s" => value
            .str()
            .map_or(SettingValue::Unsupported, |text| SettingValue::Str(text.to_string())),
        "(ddd)" => match value.get::<(f64, f64, f64)>() {
            Some((red, green, blue)) => SettingValue::Rgb(red, green, blue),
            None => SettingValue::Unsupported,
        },
        _ => SettingValue::Unsupported,
    }
}

fn emit(app: &AppHandle, emissions: Vec<Emission>) {
    for emission in emissions {
        let result = match emission {
            Emission::TextSize(payload) => app.emit(TEXT_SIZE_CHANGED_EVENT, &payload),
            Emission::Appearance(payload) => app.emit(SYSTEM_APPEARANCE_CHANGED_EVENT, &payload),
        };
        if let Err(err) = result {
            eprintln!("desktop settings: emit: {err}");
        }
    }
}
