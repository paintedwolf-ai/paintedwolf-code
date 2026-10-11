//! Host display-accessibility and accent preferences the web view cannot read.

use serde::Serialize;
use tauri::{AppHandle, Emitter};

#[derive(Debug, Clone, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SystemAppearancePayload {
    pub increase_contrast: bool,
    pub reduce_transparency: bool,
    /// `#rrggbb` when the person chose an accent color; absent under Multicolor.
    pub accent: Option<String>,
    pub revision: u64,
}

pub const SYSTEM_APPEARANCE_CHANGED_EVENT: &str = "appearance://system-changed";

#[cfg(any(not(target_os = "macos"), test))]
fn default_payload() -> SystemAppearancePayload {
    SystemAppearancePayload {
        increase_contrast: false,
        reduce_transparency: false,
        accent: None,
        revision: 0,
    }
}

#[tauri::command]
pub fn system_appearance() -> SystemAppearancePayload {
    #[cfg(target_os = "macos")]
    {
        macos::current_payload()
    }
    #[cfg(not(target_os = "macos"))]
    {
        default_payload()
    }
}

/// Registers observers for display-option and system-color changes.
pub fn setup(app: AppHandle) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        macos::setup_macos(app)
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = app;
        Ok(())
    }
}

fn hex_channel(value: f64) -> u8 {
    (value.clamp(0.0, 1.0) * 255.0).round() as u8
}

fn hex_color(red: f64, green: f64, blue: f64) -> String {
    format!(
        "#{:02x}{:02x}{:02x}",
        hex_channel(red),
        hex_channel(green),
        hex_channel(blue)
    )
}

#[cfg(target_os = "macos")]
mod macos {
    use super::*;
    use std::sync::Mutex;

    use objc2::rc::Retained;
    use objc2::{define_class, msg_send, sel, AnyThread};
    use objc2_app_kit::{
        NSColor, NSColorSpace, NSSystemColorsDidChangeNotification, NSWorkspace,
        NSWorkspaceAccessibilityDisplayOptionsDidChangeNotification,
    };
    use objc2_foundation::{
        NSNotification, NSNotificationCenter, NSObject, NSObjectProtocol, NSString, NSUserDefaults,
    };

    static APP_HANDLE: Mutex<Option<AppHandle>> = Mutex::new(None);
    static CURRENT_PAYLOAD: Mutex<Option<SystemAppearancePayload>> = Mutex::new(None);
    // Keep the observer alive.
    static OBSERVER: Mutex<Option<Retained<DenSystemAppearanceObserver>>> = Mutex::new(None);

    define_class!(
        #[unsafe(super(NSObject))]
        #[name = "DenSystemAppearanceObserver"]
        struct DenSystemAppearanceObserver;

        impl DenSystemAppearanceObserver {
            #[unsafe(method(onSystemAppearanceChanged:))]
            fn on_system_appearance_changed(&self, _notification: &NSNotification) {
                emit_current();
            }
        }

        unsafe impl NSObjectProtocol for DenSystemAppearanceObserver {}
    );

    impl DenSystemAppearanceObserver {
        fn new() -> Retained<Self> {
            unsafe { msg_send![Self::alloc(), init] }
        }
    }

    fn read_accent() -> Option<String> {
        // Multicolor stores no accent choice; the app keeps its own accent then.
        let defaults = NSUserDefaults::standardUserDefaults();
        defaults.objectForKey(&NSString::from_str("AppleAccentColor"))?;
        let color =
            NSColor::controlAccentColor().colorUsingColorSpace(&NSColorSpace::sRGBColorSpace())?;
        Some(hex_color(
            color.redComponent(),
            color.greenComponent(),
            color.blueComponent(),
        ))
    }

    fn read_payload() -> SystemAppearancePayload {
        let workspace = NSWorkspace::sharedWorkspace();
        SystemAppearancePayload {
            increase_contrast: workspace.accessibilityDisplayShouldIncreaseContrast(),
            reduce_transparency: workspace.accessibilityDisplayShouldReduceTransparency(),
            accent: read_accent(),
            revision: 0,
        }
    }

    pub(super) fn current_payload() -> SystemAppearancePayload {
        let mut guard = CURRENT_PAYLOAD.lock().unwrap_or_else(|e| e.into_inner());
        let mut next = read_payload();
        match guard.as_ref() {
            Some(payload) => {
                let unchanged = payload.increase_contrast == next.increase_contrast
                    && payload.reduce_transparency == next.reduce_transparency
                    && payload.accent == next.accent;
                if unchanged {
                    return payload.clone();
                }
                next.revision = payload.revision + 1;
            }
            None => next.revision = 1,
        }
        *guard = Some(next.clone());
        next
    }

    fn emit_current() {
        let payload = current_payload();
        let guard = APP_HANDLE.lock().unwrap_or_else(|e| e.into_inner());
        if let Some(app) = guard.as_ref() {
            let _ = app.emit(SYSTEM_APPEARANCE_CHANGED_EVENT, &payload);
        }
    }

    pub(super) fn setup_macos(app: AppHandle) -> Result<(), String> {
        {
            let mut guard = APP_HANDLE.lock().unwrap_or_else(|e| e.into_inner());
            *guard = Some(app);
        }

        let observer = DenSystemAppearanceObserver::new();
        unsafe {
            NSWorkspace::sharedWorkspace()
                .notificationCenter()
                .addObserver_selector_name_object(
                    &observer,
                    sel!(onSystemAppearanceChanged:),
                    Some(NSWorkspaceAccessibilityDisplayOptionsDidChangeNotification),
                    None,
                );
            NSNotificationCenter::defaultCenter().addObserver_selector_name_object(
                &observer,
                sel!(onSystemAppearanceChanged:),
                Some(NSSystemColorsDidChangeNotification),
                None,
            );
        }
        {
            let mut guard = OBSERVER.lock().unwrap_or_else(|e| e.into_inner());
            *guard = Some(observer);
        }

        emit_current();
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn payload_uses_camel_case_keys() {
        let json = serde_json::to_string(&default_payload()).expect("serialize");
        assert!(json.contains("\"increaseContrast\""));
        assert!(json.contains("\"reduceTransparency\""));
        assert!(json.contains("\"accent\":null"));
    }

    #[test]
    fn accent_components_format_as_srgb_hex() {
        assert_eq!(hex_color(0.0, 122.0 / 255.0, 1.0), "#007aff");
        assert_eq!(hex_color(-0.1, 1.2, 0.5), "#00ff80");
    }
}
