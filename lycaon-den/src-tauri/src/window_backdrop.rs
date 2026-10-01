//! Native fill behind the page.
//!
//! Stored fills apply before the document paints.

use std::collections::BTreeSet;
use std::fs;
use std::io::Write;
use std::path::Path;
use std::sync::Mutex;

use serde::Deserialize;
use tauri::window::Color;
use tauri::{Theme, WebviewWindow};

use crate::den_state_dir;

/// Pinned to the `index.html` fallbacks.
const LIGHT_BACKGROUND: Color = Color(0xff, 0xff, 0xff, 0xff);
const DARK_BACKGROUND: Color = Color(0x19, 0x18, 0x17, 0xff);

const MEMO_FILE: &str = "window-backdrop.json";
const MEMO_TMP: &str = "window-backdrop.json.tmp";

static MEMO: Mutex<Option<BackdropMemo>> = Mutex::new(None);

/// Painted windows; others stay transparent.
static PAINTED: Mutex<BTreeSet<String>> = Mutex::new(BTreeSet::new());

/// Records the first document paint for a window.
pub fn mark_painted(label: &str) -> bool {
    let mut guard = match PAINTED.lock() {
        Ok(guard) => guard,
        Err(poisoned) => poisoned.into_inner(),
    };
    guard.insert(label.to_string())
}

/// Reports whether the document has painted.
fn has_painted(label: &str) -> bool {
    match PAINTED.lock() {
        Ok(guard) => guard.contains(label),
        Err(poisoned) => poisoned.into_inner().contains(label),
    }
}

/// Clears paint state before rebuilding a window.
pub fn forget_painted(label: &str) {
    match PAINTED.lock() {
        Ok(mut guard) => guard.remove(label),
        Err(poisoned) => poisoned.into_inner().remove(label),
    };
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Scheme {
    Light,
    Dark,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum AppearanceMode {
    Light,
    Dark,
    System,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BackdropMemo {
    mode: AppearanceMode,
    light: Option<Color>,
    dark: Option<Color>,
}

impl Default for BackdropMemo {
    fn default() -> Self {
        Self {
            mode: AppearanceMode::System,
            light: None,
            dark: None,
        }
    }
}

impl Scheme {
    pub fn from_theme(theme: Theme) -> Self {
        match theme {
            Theme::Dark => Scheme::Dark,
            _ => Scheme::Light,
        }
    }

    fn parse(raw: &str) -> Result<Self, String> {
        match raw {
            "dark" => Ok(Scheme::Dark),
            "light" => Ok(Scheme::Light),
            other => Err(format!("unknown backdrop scheme: {other}")),
        }
    }
}

impl AppearanceMode {
    fn parse(raw: &str) -> Result<Self, String> {
        match raw {
            "light" => Ok(AppearanceMode::Light),
            "dark" => Ok(AppearanceMode::Dark),
            "system" => Ok(AppearanceMode::System),
            other => Err(format!("unknown appearance mode: {other}")),
        }
    }

    fn as_str(self) -> &'static str {
        match self {
            AppearanceMode::Light => "light",
            AppearanceMode::Dark => "dark",
            AppearanceMode::System => "system",
        }
    }
}

fn stock_backdrop(scheme: Scheme) -> Color {
    match scheme {
        Scheme::Dark => DARK_BACKGROUND,
        Scheme::Light => LIGHT_BACKGROUND,
    }
}

/// Forced mode wins; `system` follows the OS.
fn scheme_for_boot(mode: AppearanceMode, os: Theme) -> Scheme {
    match mode {
        AppearanceMode::Light => Scheme::Light,
        AppearanceMode::Dark => Scheme::Dark,
        AppearanceMode::System => Scheme::from_theme(os),
    }
}

pub fn color_from_memo(memo: &BackdropMemo, scheme: Scheme) -> Color {
    let stored = match scheme {
        Scheme::Light => memo.light,
        Scheme::Dark => memo.dark,
    };
    stored.unwrap_or_else(|| stock_backdrop(scheme))
}

/// Forced light/dark ignores OS `ThemeChanged`.
pub fn follows_os_appearance(memo: &BackdropMemo) -> bool {
    memo.mode == AppearanceMode::System
}

fn parse_hex(raw: &str) -> Option<Color> {
    let body = raw.trim().strip_prefix('#')?;
    if body.len() != 6 {
        return None;
    }
    let r = u8::from_str_radix(&body[0..2], 16).ok()?;
    let g = u8::from_str_radix(&body[2..4], 16).ok()?;
    let b = u8::from_str_radix(&body[4..6], 16).ok()?;
    Some(Color(r, g, b, 0xff))
}

fn color_to_hex(Color(r, g, b, _): Color) -> String {
    format!("#{r:02x}{g:02x}{b:02x}")
}

#[derive(Clone, Copy, Debug, Deserialize, PartialEq, Eq)]
#[serde(deny_unknown_fields)]
pub struct BackdropColor {
    r: u8,
    g: u8,
    b: u8,
}

impl From<BackdropColor> for Color {
    fn from(value: BackdropColor) -> Self {
        Color(value.r, value.g, value.b, 0xff)
    }
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct MemoFile {
    mode: String,
    light: String,
    dark: String,
}

fn load_memo(dir: &Path) -> BackdropMemo {
    let raw = match fs::read_to_string(dir.join(MEMO_FILE)) {
        Ok(raw) => raw,
        Err(_) => return BackdropMemo::default(),
    };
    let parsed: MemoFile = match serde_json::from_str(&raw) {
        Ok(parsed) => parsed,
        Err(_) => return BackdropMemo::default(),
    };
    let Some(light) = parse_hex(&parsed.light) else {
        return BackdropMemo::default();
    };
    let Some(dark) = parse_hex(&parsed.dark) else {
        return BackdropMemo::default();
    };
    let Ok(mode) = AppearanceMode::parse(&parsed.mode) else {
        return BackdropMemo::default();
    };
    BackdropMemo {
        mode,
        light: Some(light),
        dark: Some(dark),
    }
}

fn store_memo(dir: &Path, memo: &BackdropMemo) -> Result<(), String> {
    crate::config_dir::ensure_private_dir(dir).map_err(|e| e.to_string())?;
    let body = serde_json::json!({
        "mode": memo.mode.as_str(),
        "light": color_to_hex(memo.light.unwrap_or(LIGHT_BACKGROUND)),
        "dark": color_to_hex(memo.dark.unwrap_or(DARK_BACKGROUND)),
    });
    let raw = serde_json::to_string(&body).map_err(|e| e.to_string())?;
    let tmp = dir.join(MEMO_TMP);
    let result = (|| {
        let mut file = fs::File::create(&tmp).map_err(|e| e.to_string())?;
        file.write_all(raw.as_bytes()).map_err(|e| e.to_string())?;
        file.sync_all().map_err(|e| e.to_string())?;
        drop(file);
        crate::atomic_file::replace(&tmp, &dir.join(MEMO_FILE), true).map_err(|e| e.to_string())
    })();
    if result.is_err() {
        let _ = fs::remove_file(&tmp);
    }
    result
}

fn memo_lock() -> std::sync::MutexGuard<'static, Option<BackdropMemo>> {
    MEMO.lock().unwrap_or_else(|err| err.into_inner())
}

pub fn current_memo() -> BackdropMemo {
    let mut guard = memo_lock();
    if guard.is_none() {
        *guard = Some(match den_state_dir() {
            Some(dir) => load_memo(&dir),
            None => BackdropMemo::default(),
        });
    }
    guard.clone().unwrap_or_default()
}

fn palette_memo(mode: AppearanceMode, light: Color, dark: Color) -> BackdropMemo {
    BackdropMemo {
        mode,
        light: Some(light),
        dark: Some(dark),
    }
}

fn persist_palette(mode: AppearanceMode, light: Color, dark: Color) -> Result<(), String> {
    let next = palette_memo(mode, light, dark);
    {
        let mut guard = memo_lock();
        *guard = Some(next.clone());
    }
    let dir =
        den_state_dir().ok_or_else(|| "window backdrop state directory unavailable".to_string())?;
    store_memo(&dir, &next)
}

pub fn backdrop_for_os_theme(os: Theme) -> Color {
    let memo = current_memo();
    color_from_memo(&memo, scheme_for_boot(memo.mode, os))
}

pub fn apply_to_window(window: &WebviewWindow, color: Color) -> Result<(), String> {
    let window_result = window
        .set_background_color(Some(color))
        .map_err(|err| format!("window background color: {err}"));
    let webview_result = apply_webview_backdrop(window, color);
    window_result.and(webview_result)
}

fn apply_webview_backdrop(window: &WebviewWindow, color: Color) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        let Color(red, green, blue, alpha) = color;
        let opaque = has_painted(window.label());
        return window
            .with_webview(move |webview| {
                apply_wkwebview_backdrop(webview.inner(), red, green, blue, alpha, opaque);
            })
            .map_err(|err| format!("webview backdrop: {err}"));
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = (window, color);
        Ok(())
    }
}

#[cfg(target_os = "macos")]
fn apply_wkwebview_backdrop(
    inner: *mut std::ffi::c_void,
    red: u8,
    green: u8,
    blue: u8,
    alpha: u8,
    opaque: bool,
) {
    if inner.is_null() {
        return;
    }
    use objc2::runtime::NSObjectProtocol;
    use objc2::{msg_send, sel};
    use objc2_foundation::{ns_string, NSNumber, NSObjectNSKeyValueCoding};

    unsafe {
        let view: &objc2_web_kit::WKWebView = &*inner.cast();
        let color = objc2_app_kit::NSColor::colorWithSRGBRed_green_blue_alpha(
            red as f64 / 255.0,
            green as f64 / 255.0,
            blue as f64 / 255.0,
            alpha as f64 / 255.0,
        );
        // A view that draws before the first paint shows its own fill, not the window's.
        view.setValue_forKey(
            Some(&NSNumber::numberWithBool(opaque)),
            ns_string!("drawsBackground"),
        );
        view.setUnderPageBackgroundColor(Some(&color));
        // Fills area the page has not reached, including new area during a resize.
        // Unset, a drawing view falls back to the system control background.
        if view.respondsToSelector(sel!(_setBackgroundColor:)) {
            let _: () = msg_send![view, _setBackgroundColor: &*color];
        } else {
            static REPORTED: std::sync::Once = std::sync::Once::new();
            REPORTED.call_once(|| {
                eprintln!("web view fill unavailable; a resize exposes the system background");
            });
        }
    }
}

#[tauri::command]
pub fn den_set_window_backdrop(
    window: WebviewWindow,
    light: BackdropColor,
    dark: BackdropColor,
    scheme: String,
    mode: String,
) -> Result<(), String> {
    let scheme = Scheme::parse(&scheme)?;
    let mode = AppearanceMode::parse(&mode)?;
    let light = Color::from(light);
    let dark = Color::from(dark);
    let persist_result = persist_palette(mode, light, dark);
    let color = match scheme {
        Scheme::Light => light,
        Scheme::Dark => dark,
    };
    let apply_result = apply_to_window(&window, color);
    match (persist_result, apply_result) {
        (Ok(()), Ok(())) => Ok(()),
        (Err(persist), Ok(())) => Err(persist),
        (Ok(()), Err(apply)) => Err(apply),
        (Err(persist), Err(apply)) => Err(format!("{persist}; {apply}")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_support::TempDir;

    #[test]
    fn a_window_is_opaque_only_after_its_document_paints() {
        let label = "paint-transition";
        forget_painted(label);

        assert!(
            !has_painted(label),
            "a window with no painted frame must leave its web view non-opaque"
        );
        assert!(mark_painted(label), "the first paint is the transition");
        assert!(
            has_painted(label),
            "a painted window's web view must draw its own background"
        );
        assert!(
            !mark_painted(label),
            "a dock reopen re-reveals an already painted window and reapplies nothing"
        );

        forget_painted(label);
        assert!(
            !has_painted(label),
            "a destroyed window's label is reusable, and the next document has not painted"
        );
    }

    #[test]
    fn paint_state_is_tracked_per_window() {
        let (main, satellite) = ("paint-main", "paint-satellite");
        forget_painted(main);
        forget_painted(satellite);

        mark_painted(main);
        assert!(has_painted(main));
        assert!(
            !has_painted(satellite),
            "one window painting must not make another window's web view opaque early"
        );

        forget_painted(main);
        forget_painted(satellite);
    }

    #[test]
    fn stock_matches_boot_style_literals() {
        assert_eq!(stock_backdrop(Scheme::Dark), DARK_BACKGROUND);
        assert_eq!(stock_backdrop(Scheme::Light), LIGHT_BACKGROUND);
        assert_eq!(color_to_hex(DARK_BACKGROUND), "#191817");
        assert_eq!(color_to_hex(LIGHT_BACKGROUND), "#ffffff");
    }

    #[test]
    fn hex_round_trips_and_rejects_junk() {
        assert_eq!(parse_hex("#1a1b26"), Some(Color(0x1a, 0x1b, 0x26, 0xff)));
        assert_eq!(parse_hex("  #191817  "), Some(DARK_BACKGROUND));
        assert!(parse_hex("191817").is_none());
        assert!(parse_hex("#gg0000").is_none());
        assert!(parse_hex("#fff").is_none());
        assert!(parse_hex("#faf4edff").is_none());
        assert!(parse_hex("#ffffff80").is_none());
    }

    #[test]
    fn forced_mode_wins_over_os_theme() {
        assert_eq!(
            scheme_for_boot(AppearanceMode::Light, Theme::Dark),
            Scheme::Light
        );
        assert_eq!(
            scheme_for_boot(AppearanceMode::Dark, Theme::Light),
            Scheme::Dark
        );
        assert_eq!(
            scheme_for_boot(AppearanceMode::System, Theme::Dark),
            Scheme::Dark
        );
    }

    #[test]
    fn memo_overrides_stock_per_scheme() {
        let memo = BackdropMemo {
            mode: AppearanceMode::System,
            light: parse_hex("#faf4ed"),
            dark: parse_hex("#1a1b26"),
        };
        assert_eq!(
            color_from_memo(&memo, Scheme::Dark),
            Color(0x1a, 0x1b, 0x26, 0xff)
        );
        assert_eq!(
            color_from_memo(&memo, Scheme::Light),
            Color(0xfa, 0xf4, 0xed, 0xff)
        );
        assert_eq!(
            color_from_memo(&BackdropMemo::default(), Scheme::Dark),
            DARK_BACKGROUND
        );
        assert!(follows_os_appearance(&memo));
        assert!(!follows_os_appearance(&BackdropMemo {
            mode: AppearanceMode::Light,
            ..BackdropMemo::default()
        }));
    }

    #[test]
    fn memo_round_trips_on_disk() {
        let dir = TempDir::new("window-backdrop-roundtrip");
        let memo = BackdropMemo {
            mode: AppearanceMode::Dark,
            light: parse_hex("#fdfcfb"),
            dark: parse_hex("#0b0b10"),
        };
        store_memo(&dir, &memo).expect("store");
        assert_eq!(load_memo(&dir), memo);
        let replacement = BackdropMemo {
            mode: AppearanceMode::Light,
            light: parse_hex("#f8f7f6"),
            dark: parse_hex("#111111"),
        };
        store_memo(&dir, &replacement).expect("replace");
        assert_eq!(load_memo(&dir), replacement);
        assert_eq!(load_memo(&dir.join("missing")), BackdropMemo::default());
    }

    #[test]
    fn incomplete_memo_uses_stock() {
        let dir = TempDir::new("window-backdrop-incomplete");
        fs::write(
            dir.join(MEMO_FILE),
            br##"{"mode":"dark","dark":"#111111"}"##,
        )
        .expect("write incomplete memo");
        assert_eq!(load_memo(&dir), BackdropMemo::default());
    }

    #[test]
    fn palette_replaces_both_schemes_together() {
        let light = Color(0xfa, 0xf4, 0xed, 0xff);
        let dark = Color(0x1a, 0x1b, 0x26, 0xff);
        assert_eq!(
            palette_memo(AppearanceMode::System, light, dark),
            BackdropMemo {
                mode: AppearanceMode::System,
                light: Some(light),
                dark: Some(dark),
            }
        );
    }
}
