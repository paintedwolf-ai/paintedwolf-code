//! Window button layout for client-side decorations on Linux.
//!
//! The desktop publishes its layout as `org.gnome.desktop.wm.preferences
//! button-layout` through the Settings portal. The shell draws the buttons
//! the desktop lists, where it lists them, and falls back to the full set on
//! the right when no desktop answers.

use serde::Serialize;

/// One control the desktop places in a title bar.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum DecorationControl {
    Appmenu,
    Minimize,
    Maximize,
    Close,
}

impl DecorationControl {
    fn parse(token: &str) -> Option<Self> {
        match token.trim() {
            "appmenu" => Some(Self::Appmenu),
            "minimize" => Some(Self::Minimize),
            "maximize" => Some(Self::Maximize),
            "close" => Some(Self::Close),
            _ => None,
        }
    }
}

/// Where the fact came from, so the shell can report a desktop that answered.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum DecorationLayoutSource {
    Portal,
    Default,
}

/// Controls on each side of the title bar, in display order.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct DecorationLayout {
    pub left: Vec<DecorationControl>,
    pub right: Vec<DecorationControl>,
    pub source: DecorationLayoutSource,
}

impl DecorationLayout {
    /// Minimize, maximize, and close on the right.
    pub fn default_layout() -> Self {
        Self {
            left: Vec::new(),
            right: vec![
                DecorationControl::Minimize,
                DecorationControl::Maximize,
                DecorationControl::Close,
            ],
            source: DecorationLayoutSource::Default,
        }
    }
}

/// Parses a `button-layout` value such as `appmenu:minimize,maximize,close`.
///
/// Tokens the shell does not draw (`icon`, `spacer`) are dropped; a repeated
/// control keeps its first position. A value naming no control at all is not
/// a layout, so the default stands.
pub fn parse_button_layout(raw: &str) -> Option<DecorationLayout> {
    let (left, right) = match raw.split_once(':') {
        Some((left, right)) => (left, right),
        None => ("", raw),
    };
    let mut seen = Vec::new();
    let mut side = |spec: &str| -> Vec<DecorationControl> {
        spec.split(',')
            .filter_map(DecorationControl::parse)
            .filter(|control| {
                if seen.contains(control) {
                    return false;
                }
                seen.push(*control);
                true
            })
            .collect()
    };
    let left = side(left);
    let right = side(right);
    if left.is_empty() && right.is_empty() {
        return None;
    }
    Some(DecorationLayout {
        left,
        right,
        source: DecorationLayoutSource::Portal,
    })
}

/// Event carrying the desktop's layout after `button-layout` changes.
pub const LAYOUT_CHANGED_EVENT: &str = "linux-decoration-layout-changed";

/// The desktop's layout when the Settings portal exposes one, else the default.
#[tauri::command]
pub async fn linux_decoration_layout() -> DecorationLayout {
    tauri::async_runtime::spawn_blocking(current_layout)
        .await
        .unwrap_or_else(|_| DecorationLayout::default_layout())
}

/// A layout from a raw `button-layout`; a value naming no control falls back to the default.
fn layout_from_raw(raw: &str) -> DecorationLayout {
    parse_button_layout(raw).unwrap_or_else(DecorationLayout::default_layout)
}

#[cfg(target_os = "linux")]
fn current_layout() -> DecorationLayout {
    match portal::read_button_layout() {
        Ok(raw) => layout_from_raw(&raw),
        Err(reason) => {
            eprintln!("window button layout unavailable: {reason}; drawing the default");
            DecorationLayout::default_layout()
        }
    }
}

#[cfg(not(target_os = "linux"))]
fn current_layout() -> DecorationLayout {
    DecorationLayout::default_layout()
}

/// Follows `button-layout` through the portal's `SettingChanged` signal.
#[cfg(target_os = "linux")]
pub fn setup(app: tauri::AppHandle) {
    use tauri::Emitter;
    let spawned = std::thread::Builder::new()
        .name("decoration-layout".into())
        .spawn(move || {
            let outcome = portal::watch_button_layout(move |raw| {
                if let Err(err) = app.emit(LAYOUT_CHANGED_EVENT, layout_from_raw(raw)) {
                    eprintln!("window button layout: emit: {err}");
                }
            });
            if let Err(reason) = outcome {
                eprintln!("window button layout changes unavailable: {reason}");
            }
        });
    if let Err(err) = spawned {
        eprintln!("window button layout: watch thread: {err}");
    }
}

#[cfg(not(target_os = "linux"))]
pub fn setup(_app: tauri::AppHandle) {}

#[cfg(target_os = "linux")]
mod portal;

#[cfg(test)]
mod tests {
    use super::*;
    use DecorationControl::*;

    /// The drawn buttons and menu entries call these window operations from
    /// every window that draws its own chrome.
    #[test]
    fn window_capabilities_grant_the_drawn_chrome_operations() {
        let manifest = std::path::Path::new(env!("CARGO_MANIFEST_DIR"));
        for capability in ["capabilities/default.json", "capabilities/items.json"] {
            let text = std::fs::read_to_string(manifest.join(capability)).expect("capability file");
            for permission in [
                "core:window:allow-minimize",
                "core:window:allow-toggle-maximize",
                "core:window:allow-close",
                "core:window:allow-set-fullscreen",
                "allow-linux-decoration-layout",
                "allow-linux-window-frame",
            ] {
                assert!(
                    text.contains(&format!("\"{permission}\"")),
                    "{capability} lacks {permission}"
                );
            }
        }
    }

    fn portal(left: &[DecorationControl], right: &[DecorationControl]) -> DecorationLayout {
        DecorationLayout {
            left: left.to_vec(),
            right: right.to_vec(),
            source: DecorationLayoutSource::Portal,
        }
    }

    #[test]
    fn gnome_default_keeps_only_the_close_button_and_the_app_menu_on_the_left() {
        assert_eq!(
            parse_button_layout("appmenu:close"),
            Some(portal(&[Appmenu], &[Close]))
        );
    }

    #[test]
    fn full_right_layout_keeps_the_desktop_order() {
        assert_eq!(
            parse_button_layout(":minimize,maximize,close"),
            Some(portal(&[], &[Minimize, Maximize, Close]))
        );
        assert_eq!(
            parse_button_layout("close,maximize,minimize:"),
            Some(portal(&[Close, Maximize, Minimize], &[]))
        );
    }

    #[test]
    fn undrawn_tokens_blanks_and_repeats_are_dropped() {
        assert_eq!(
            parse_button_layout("icon, appmenu :spacer,minimize,,close,minimize"),
            Some(portal(&[Appmenu], &[Minimize, Close]))
        );
    }

    #[test]
    fn a_value_without_a_separator_is_the_right_side() {
        assert_eq!(
            parse_button_layout("minimize,close"),
            Some(portal(&[], &[Minimize, Close]))
        );
    }

    #[test]
    fn a_value_naming_no_control_is_not_a_layout() {
        assert_eq!(parse_button_layout(""), None);
        assert_eq!(parse_button_layout(":"), None);
        assert_eq!(parse_button_layout("icon:spacer"), None);
    }

    #[test]
    fn the_default_is_the_full_set_on_the_right_from_no_desktop() {
        let layout = DecorationLayout::default_layout();
        assert!(layout.left.is_empty());
        assert_eq!(layout.right, vec![Minimize, Maximize, Close]);
        assert_eq!(layout.source, DecorationLayoutSource::Default);
    }

    #[test]
    fn a_changed_value_naming_no_control_draws_the_default() {
        assert_eq!(layout_from_raw("icon:"), DecorationLayout::default_layout());
        assert_eq!(layout_from_raw("close:"), portal(&[Close], &[]));
    }

    #[test]
    fn the_wire_shape_uses_lowercase_control_names() {
        let json = serde_json::to_string(&parse_button_layout("appmenu:close").unwrap()).unwrap();
        assert_eq!(
            json,
            r#"{"left":["appmenu"],"right":["close"],"source":"portal"}"#
        );
    }
}
