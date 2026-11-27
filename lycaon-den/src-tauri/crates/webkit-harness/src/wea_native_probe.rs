//! TB02 / D12 macOS native Den owned-window WKWebView probe.
//!
//! Opens a real owned `NSWindow` + ephemeral `WKWebView` pointed at the live harness Den URL,
//! connects via `window.__harness`, focuses a known landmark, dispatches a real native `Tab`
//! `NSEvent` through `NSApplication::sendEvent:`, verifies the DOM focus transition and live
//! API readback, captures a native PNG screenshot of the owned `WKWebView`, and prints a
//! single structured JSON receipt to stdout.

use std::path::Path;
use std::time::Duration;

use objc2::MainThreadMarker;
use objc2_foundation::NSSize;
use serde_json::{json, Value};

use crate::driver::Driver;

const RETURNING_PERSON: &str = r#"try {
  localStorage.setItem("paintedwolf.app-state.v1.onboarding", JSON.stringify({ completedSetupRevision: 2, completedLocalModelStepRevision: 2 }));
  localStorage.setItem("paintedwolf.app-state.v1.firstTimeTips", JSON.stringify({ enabled: false }));
} catch {}"#;

const WINDOW: NSSize = NSSize::new(1280.0, 800.0);

const SETUP_EXPR: &str = r#"(async () => {
  for (let i = 0; i < 240 && !window.__harness; i++) {
    await new Promise((r) => setTimeout(r, 250));
  }
  if (!window.__harness) {
    return { ok: false, error: "harness API missing" };
  }
  for (let i = 0; i < 480 && !(__harness.state().connected && __harness.state().view !== "loading"); i++) {
    await new Promise((r) => setTimeout(r, 250));
  }
  const st = __harness.state();
  if (!st.connected) {
    return { ok: false, error: "harness not connected", state: st };
  }
  const opened = await __harness.openProject("Harness");
  if (opened?.ok !== true) {
    return { ok: false, error: "openProject failed", detail: opened };
  }
  const session = await __harness.newSession();
  if (session?.ok !== true) {
    return { ok: false, error: "newSession failed", detail: session };
  }
  await new Promise((r) => setTimeout(r, 500));
  const composer = document.querySelector("textarea, [contenteditable='true'], input[type='text']");
  if (composer && typeof composer.focus === "function") {
    composer.focus();
  }
  await new Promise((r) => setTimeout(r, 150));
  return {
    ok: true,
    state: __harness.state(),
    sessionId: session.sessionId || __harness.state().sessionId || "",
  };
})()"#;

const FOCUS_EXPR: &str = r#"(async () => {
  const el = document.activeElement;
  const tagName = el ? (el.tagName || "") : "";
  const testId = el && el.getAttribute ? (el.getAttribute("data-testid") || "") : "";
  const role = el && el.getAttribute ? (el.getAttribute("role") || "") : "";
  const ariaLabel = el && el.getAttribute ? (el.getAttribute("aria-label") || "") : "";
  return {
    tagName,
    testId,
    role,
    ariaLabel,
    key: `${tagName}|${testId}|${role}|${ariaLabel}`,
  };
})()"#;

pub fn run(url: &str, screenshot_path: &str) -> i32 {
    let mtm = MainThreadMarker::new().expect("wea-native-probe runs on the main thread");
    let driver = Driver::open(mtm, WINDOW, Some(RETURNING_PERSON));
    let owned_window_number = driver.window.windowNumber();
    let pid = std::process::id();

    driver.load_url(url);
    driver.spin(Duration::from_millis(800));

    let setup_raw = match driver.eval_async(SETUP_EXPR, Duration::from_secs(90)) {
        Ok(raw) => raw,
        Err(err) => {
            let receipt = json!({
                "schema": "wea-native-den-macos-wkwebview-v1",
                "tier": "native-den-wkwebview",
                "owned_window_number": owned_window_number,
                "pid": pid,
                "passed": false,
                "error": format!("setup failed: {err}"),
            });
            println!("{receipt}");
            return 1;
        }
    };
    let setup_val: Value = serde_json::from_str(&setup_raw).unwrap_or_else(|_| json!({"ok": false}));
    if setup_val.get("ok").and_then(Value::as_bool) != Some(true) {
        let receipt = json!({
            "schema": "wea-native-den-macos-wkwebview-v1",
            "tier": "native-den-wkwebview",
            "owned_window_number": owned_window_number,
            "pid": pid,
            "passed": false,
            "setup": setup_val,
            "error": "harness setup returned ok != true",
        });
        println!("{receipt}");
        return 1;
    }

    let focus_before_raw = driver
        .eval_async(FOCUS_EXPR, Duration::from_secs(5))
        .unwrap_or_else(|_| "{}".into());
    let focus_before: Value = serde_json::from_str(&focus_before_raw).unwrap_or_else(|_| json!({}));

    // Dispatch a real native Tab NSEvent (macOS virtual keyCode 48) through NSApplication::sendEvent:.
    driver.send_key(48, "\t");
    driver.spin(Duration::from_millis(300));

    let mut focus_after_raw = driver
        .eval_async(FOCUS_EXPR, Duration::from_secs(5))
        .unwrap_or_else(|_| "{}".into());
    let mut focus_after: Value = serde_json::from_str(&focus_after_raw).unwrap_or_else(|_| json!({}));

    let before_key = focus_before.get("key").and_then(Value::as_str).unwrap_or("");
    let mut after_key = focus_after.get("key").and_then(Value::as_str).unwrap_or("");
    if after_key == before_key {
        driver.send_key(48, "\t");
        driver.spin(Duration::from_millis(300));
        focus_after_raw = driver
            .eval_async(FOCUS_EXPR, Duration::from_secs(5))
            .unwrap_or_else(|_| "{}".into());
        focus_after = serde_json::from_str(&focus_after_raw).unwrap_or_else(|_| json!({}));
        after_key = focus_after.get("key").and_then(Value::as_str).unwrap_or("");
    }

    let focus_changed = !before_key.is_empty() && !after_key.is_empty() && after_key != before_key;

    let state_after_raw = driver
        .eval_async("window.__harness ? window.__harness.state() : null", Duration::from_secs(5))
        .unwrap_or_else(|_| "null".into());
    let state_after: Value = serde_json::from_str(&state_after_raw).unwrap_or(Value::Null);

    let png_path = Path::new(screenshot_path);
    let (screenshot_bytes, screenshot_error) = match driver.capture_png(png_path) {
        Ok(n) => (n, Value::Null),
        Err(err) => (0, Value::String(err)),
    };

    let passed = focus_changed && screenshot_bytes > 0;
    let receipt = json!({
        "schema": "wea-native-den-macos-wkwebview-v1",
        "tier": "native-den-wkwebview",
        "owned_window_number": owned_window_number,
        "pid": pid,
        "session_id": setup_val.get("sessionId").cloned().unwrap_or(Value::String(String::new())),
        "focus_before": focus_before,
        "focus_after": focus_after,
        "focus_changed": focus_changed,
        "api_readback": state_after,
        "screenshot_path": screenshot_path,
        "screenshot_bytes": screenshot_bytes,
        "screenshot_error": screenshot_error,
        "passed": passed,
    });
    println!("{receipt}");
    if passed { 0 } else { 1 }
}
