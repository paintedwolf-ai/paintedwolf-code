//! WebKit features the shell turns on for its web views.
//!
//! WebKit ships `requestIdleCallback` behind a feature flag, so a `WKWebView`
//! never exposes it by default. A document fixes its exposed API when it is
//! created, so the flag goes on the configuration every window is built with.

use tauri::utils::config::WindowConfig;
use tauri::{AppHandle, Manager, Runtime, WebviewWindowBuilder};

/// The window the static configuration leaves for the shell to create.
pub const MAIN_WINDOW_LABEL: &str = "main";

/// WebKit experimental features enabled for every Den web view.
#[cfg(target_os = "macos")]
const WEBKIT_FEATURES: &[&str] = &["RequestIdleCallbackEnabled"];

/// Creates the main window from its static configuration with the shell's web view configuration.
pub fn create_main_window<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<()> {
    let config = main_window_config(app)?;
    let builder = WebviewWindowBuilder::from_config(app, &config)?;
    let builder = with_shell_configuration(builder);
    builder.build()?;
    Ok(())
}

fn main_window_config<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<WindowConfig> {
    app.config()
        .app
        .windows
        .iter()
        .find(|window| window.label == MAIN_WINDOW_LABEL)
        .cloned()
        .ok_or_else(|| tauri::Error::WindowNotFound)
}

/// Every web view the shell builds shares the same enabled features.
pub fn with_shell_configuration<'a, R: Runtime, M: Manager<R>>(
    builder: WebviewWindowBuilder<'a, R, M>,
) -> WebviewWindowBuilder<'a, R, M> {
    #[cfg(target_os = "macos")]
    {
        match webview_configuration() {
            Some(configuration) => builder.with_webview_configuration(configuration),
            None => builder,
        }
    }
    #[cfg(not(target_os = "macos"))]
    {
        builder
    }
}

/// A web view configuration with the shell's WebKit features enabled; `None` off the main thread.
#[cfg(target_os = "macos")]
fn webview_configuration() -> Option<objc2::rc::Retained<objc2_web_kit::WKWebViewConfiguration>> {
    use objc2_foundation::MainThreadMarker;
    let Some(mtm) = MainThreadMarker::new() else {
        // A view built off the main thread keeps WebKit's defaults; the editor's timer fallback covers it.
        eprintln!("web view configuration requested off the main thread; WebKit features stay at their defaults");
        return None;
    };
    let configuration = unsafe { objc2_web_kit::WKWebViewConfiguration::new(mtm) };
    let preferences = unsafe { configuration.preferences() };
    for feature in WEBKIT_FEATURES {
        enable_feature(&preferences, feature);
    }
    Some(configuration)
}

/// The experimental feature object WebKit offers under `key`, when this WebKit lists and can set features.
#[cfg(target_os = "macos")]
fn experimental_feature(
    preferences: &objc2_web_kit::WKPreferences,
    key: &str,
) -> Result<objc2::rc::Retained<objc2::runtime::AnyObject>, String> {
    use objc2::rc::Retained;
    use objc2::runtime::{AnyObject, NSObjectProtocol};
    use objc2::{msg_send, sel, ClassType};
    use objc2_foundation::{NSArray, NSString};

    let class = objc2_web_kit::WKPreferences::class();
    if !class.metaclass().responds_to(sel!(_experimentalFeatures)) {
        return Err(format!(
            "this WebKit lists no experimental features; {key} stays at its default"
        ));
    }
    if !preferences.respondsToSelector(sel!(_setEnabled:forFeature:)) {
        return Err(format!(
            "this WebKit cannot set experimental features; {key} stays at its default"
        ));
    }
    unsafe {
        let features: Retained<NSArray<AnyObject>> = msg_send![class, _experimentalFeatures];
        for feature in features.iter() {
            let name: Retained<NSString> = msg_send![&*feature, key];
            if name.to_string() == key {
                return Ok(feature);
            }
        }
    }
    Err(format!(
        "this WebKit does not offer {key}; it stays at its default"
    ))
}

/// Turns on one experimental WebKit feature.
#[cfg(target_os = "macos")]
fn enable_feature(preferences: &objc2_web_kit::WKPreferences, key: &str) {
    use objc2::msg_send;
    match experimental_feature(preferences, key) {
        Ok(feature) => unsafe {
            let _: () = msg_send![preferences, _setEnabled: true, forFeature: &*feature];
        },
        Err(reason) => {
            eprintln!("WebKit feature unavailable: {reason}; idle work falls back to timers")
        }
    }
}

/// Whether each shell feature can be enabled on this WebKit.
#[cfg(target_os = "macos")]
pub fn probe_features() -> Vec<(String, Result<(), String>)> {
    use objc2_foundation::MainThreadMarker;
    let Some(mtm) = MainThreadMarker::new() else {
        return WEBKIT_FEATURES
            .iter()
            .map(|key| (key.to_string(), Err("not on the main thread".to_string())))
            .collect();
    };
    let configuration = unsafe { objc2_web_kit::WKWebViewConfiguration::new(mtm) };
    let preferences = unsafe { configuration.preferences() };
    WEBKIT_FEATURES
        .iter()
        .map(|key| {
            (
                key.to_string(),
                experimental_feature(&preferences, key).map(|_| ()),
            )
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The static window list describes the main window; the shell creates it.
    #[test]
    fn main_window_is_created_by_the_shell_in_every_platform_config() {
        for name in [
            "tauri.conf.json",
            "tauri.macos.conf.json",
            "tauri.linux.conf.json",
        ] {
            let raw = std::fs::read_to_string(
                concat!(env!("CARGO_MANIFEST_DIR"), "/").to_string() + name,
            )
            .unwrap_or_else(|e| panic!("read {name}: {e}"));
            let parsed: serde_json::Value =
                serde_json::from_str(&raw).unwrap_or_else(|e| panic!("parse {name}: {e}"));
            let windows = parsed["app"]["windows"]
                .as_array()
                .unwrap_or_else(|| panic!("{name} has no app.windows"));
            let main = windows
                .iter()
                .find(|w| w["label"] == MAIN_WINDOW_LABEL)
                .unwrap_or_else(|| panic!("{name} has no main window"));
            assert_eq!(
                main["create"],
                serde_json::Value::Bool(false),
                "{name}: the shell creates the main window with its web view configuration"
            );
        }
    }
}
