//! Host text-size category mapping.

use serde::Serialize;
use tauri::{AppHandle, Emitter};

/// Accessibility text-size payload.
#[derive(Debug, Clone, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct TextSizePayload {
    pub category: String,
    pub text_scale: f64,
    pub revision: u64,
}

/// Maps category names to text scale.
pub fn text_scale_for_category(category: &str) -> f64 {
    match category {
        "UICTContentSizeCategoryXS" => 14.0 / 17.0,
        "UICTContentSizeCategoryS" => 15.0 / 17.0,
        "UICTContentSizeCategoryM" => 16.0 / 17.0,
        "UICTContentSizeCategoryL" => 1.0,
        "UICTContentSizeCategoryXL" => 19.0 / 17.0,
        "UICTContentSizeCategoryXXL" => 21.0 / 17.0,
        "UICTContentSizeCategoryXXXL" => 23.0 / 17.0,
        "UICTContentSizeCategoryAccessibilityM" => 28.0 / 17.0,
        "UICTContentSizeCategoryAccessibilityL" => 33.0 / 17.0,
        "UICTContentSizeCategoryAccessibilityXL" => 40.0 / 17.0,
        "UICTContentSizeCategoryAccessibilityXXL" => 47.0 / 17.0,
        "UICTContentSizeCategoryAccessibilityXXXL" => 53.0 / 17.0,
        // Universal Access category values.
        "XXXS" | "XXS" => 14.0 / 17.0,
        "XS" => 15.0 / 17.0,
        "S" => 16.0 / 17.0,
        "DEFAULT" | "M" => 1.0,
        "L" => 19.0 / 17.0,
        "XL" => 21.0 / 17.0,
        "XXL" => 23.0 / 17.0,
        "XXXL" | "AX1" => 28.0 / 17.0,
        "AX2" => 33.0 / 17.0,
        "AX3" => 40.0 / 17.0,
        "AX4" => 47.0 / 17.0,
        "AX5" => 53.0 / 17.0,
        _ => 1.0,
    }
}

pub fn payload_for_category(category: impl Into<String>) -> TextSizePayload {
    let category = category.into();
    let text_scale = text_scale_for_category(&category);
    TextSizePayload {
        category,
        text_scale,
        revision: 0,
    }
}

pub const TEXT_SIZE_CHANGED_EVENT: &str = "accessibility://text-size-changed";

#[tauri::command]
pub fn accessibility_preferred_text_size() -> TextSizePayload {
    #[cfg(target_os = "macos")]
    {
        current_payload()
    }
    #[cfg(not(target_os = "macos"))]
    {
        payload_for_category("default")
    }
}

/// Registers text-size observers.
pub fn setup(app: AppHandle) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        setup_macos(app)
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = app;
        Ok(())
    }
}

#[cfg(target_os = "macos")]
mod macos {
    use super::*;
    use std::sync::Mutex;

    use objc2::rc::Retained;
    use objc2::runtime::AnyObject;
    use objc2::{define_class, msg_send, sel, AnyThread};
    use objc2_foundation::{
        NSDictionary, NSDistributedNotificationCenter, NSNotification, NSObject, NSObjectProtocol,
        NSString, NSUserDefaults,
    };

    static APP_HANDLE: Mutex<Option<AppHandle>> = Mutex::new(None);
    static CURRENT_PAYLOAD: Mutex<Option<TextSizePayload>> = Mutex::new(None);
    // Keep observers alive.
    static OBSERVER: Mutex<Option<Retained<DenTextSizeObserver>>> = Mutex::new(None);
    static UA_DEFAULTS: Mutex<Option<Retained<NSUserDefaults>>> = Mutex::new(None);

    define_class!(
        #[unsafe(super(NSObject))]
        #[name = "DenTextSizeObserver"]
        struct DenTextSizeObserver;

        impl DenTextSizeObserver {
            #[unsafe(method(onPreferredContentSizeCategoryChanged:))]
            fn on_preferred_content_size_category_changed(&self, _notification: &NSNotification) {
                emit_current();
            }

            #[unsafe(method(observeValueForKeyPath:ofObject:change:context:))]
            unsafe fn observe_value_for_key_path(
                &self,
                _key_path: Option<&NSString>,
                _object: Option<&AnyObject>,
                _change: Option<&NSDictionary<NSString, AnyObject>>,
                _context: *mut std::ffi::c_void,
            ) {
                emit_current();
            }
        }

        unsafe impl NSObjectProtocol for DenTextSizeObserver {}
    );

    impl DenTextSizeObserver {
        fn new() -> Retained<Self> {
            unsafe { msg_send![Self::alloc(), init] }
        }
    }

    pub(super) fn read_category() -> String {
        if let Some(cat) = read_ui_preferred_content_size_category() {
            if !cat.is_empty() {
                return cat;
            }
        }
        if let Some(cat) = read_font_size_category_global() {
            if !cat.is_empty() {
                return cat;
            }
        }
        "default".to_string()
    }

    fn read_ui_preferred_content_size_category() -> Option<String> {
        let defaults = NSUserDefaults::standardUserDefaults();
        let key = NSString::from_str("UIPreferredContentSizeCategoryName");
        defaults
            .stringForKey(&key)
            .map(|s| s.to_string())
            .filter(|s| !s.is_empty())
    }

    fn read_font_size_category_global() -> Option<String> {
        let suite_name = NSString::from_str("com.apple.universalaccess");
        let defaults =
            NSUserDefaults::initWithSuiteName(NSUserDefaults::alloc(), Some(&suite_name))?;
        let key = NSString::from_str("FontSizeCategory");
        let dict = defaults.dictionaryForKey(&key)?;
        let global_key = NSString::from_str("global");
        let value = dict.objectForKey(&global_key)?;
        // The global category is stored as a string.
        let as_string: Option<Retained<NSString>> = value.downcast().ok();
        as_string.map(|s| s.to_string()).filter(|s| !s.is_empty())
    }

    pub(super) fn current_payload() -> TextSizePayload {
        let mut guard = CURRENT_PAYLOAD.lock().unwrap_or_else(|e| e.into_inner());
        let category = read_category();
        match guard.as_ref() {
            Some(payload) if payload.category == category => payload.clone(),
            Some(payload) => {
                let mut next = payload_for_category(category);
                next.revision = payload.revision + 1;
                *guard = Some(next.clone());
                next
            }
            None => {
                let mut payload = payload_for_category(category);
                payload.revision = 1;
                *guard = Some(payload.clone());
                payload
            }
        }
    }

    fn emit_current() {
        let payload = current_payload();
        let guard = APP_HANDLE.lock().unwrap_or_else(|e| e.into_inner());
        if let Some(app) = guard.as_ref() {
            let _ = app.emit(TEXT_SIZE_CHANGED_EVENT, &payload);
        }
    }

    pub(super) fn setup_macos(app: AppHandle) -> Result<(), String> {
        {
            let mut guard = APP_HANDLE.lock().unwrap_or_else(|e| e.into_inner());
            *guard = Some(app);
        }

        let observer = DenTextSizeObserver::new();

        let center = NSDistributedNotificationCenter::defaultCenter();
        let name = NSString::from_str("com.apple.PreferredContentSizeCategoryChanged");
        unsafe {
            center.addObserver_selector_name_object(
                &observer,
                sel!(onPreferredContentSizeCategoryChanged:),
                Some(&name),
                None,
            );
        }

        // Observe fallback category changes.
        let suite_name = NSString::from_str("com.apple.universalaccess");
        if let Some(ua) =
            NSUserDefaults::initWithSuiteName(NSUserDefaults::alloc(), Some(&suite_name))
        {
            let key_path = NSString::from_str("FontSizeCategory");
            unsafe {
                let _: () = msg_send![
                    &*ua,
                    addObserver: &*observer,
                    forKeyPath: &*key_path,
                    options: 0usize,
                    context: std::ptr::null_mut::<std::ffi::c_void>()
                ];
            }
            let mut ua_guard = UA_DEFAULTS.lock().unwrap_or_else(|e| e.into_inner());
            *ua_guard = Some(ua);
        }

        {
            let mut obs_guard = OBSERVER.lock().unwrap_or_else(|e| e.into_inner());
            *obs_guard = Some(observer);
        }

        // Publish the initial state after registration.
        emit_current();
        Ok(())
    }
}

#[cfg(target_os = "macos")]
use macos::{current_payload, setup_macos};

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn default_and_large_are_baseline() {
        assert_eq!(text_scale_for_category("UICTContentSizeCategoryL"), 1.0);
        assert_eq!(text_scale_for_category("DEFAULT"), 1.0);
        assert_eq!(text_scale_for_category("M"), 1.0);
        assert_eq!(text_scale_for_category("default"), 1.0);
        assert_eq!(text_scale_for_category("unknown"), 1.0);
    }

    #[test]
    fn accessibility_max_matches_locked_ratio() {
        let scale = text_scale_for_category("UICTContentSizeCategoryAccessibilityXXXL");
        assert!((scale - 53.0 / 17.0).abs() < 1e-9);
        assert_eq!(text_scale_for_category("AX5"), scale);
    }

    #[test]
    fn universal_access_categories_match_uict_rows() {
        assert_eq!(
            text_scale_for_category("XXXS"),
            text_scale_for_category("UICTContentSizeCategoryXS")
        );
        assert_eq!(
            text_scale_for_category("AX1"),
            text_scale_for_category("UICTContentSizeCategoryAccessibilityM")
        );
    }

    #[test]
    fn payload_uses_camel_case_keys() {
        let payload = payload_for_category("UICTContentSizeCategoryXL");
        let json = serde_json::to_string(&payload).expect("serialize");
        assert!(json.contains("\"textScale\""));
        assert!(json.contains("\"category\""));
        assert!(!json.contains("text_scale"));
    }

    #[test]
    fn stub_command_shape_on_any_platform() {
        let payload = accessibility_preferred_text_size();
        assert!(payload.text_scale > 0.0);
        assert!(!payload.category.is_empty());
    }
}
