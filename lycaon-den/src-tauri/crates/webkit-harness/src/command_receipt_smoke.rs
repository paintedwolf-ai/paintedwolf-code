//! Real macOS WKWebView command activity, receipt identity, and reload smoke.
//!
//! Drives the harness Den in a real macOS WKWebView via `Driver`, executes a live
//! Portable Command turn against the real sidecar, checks rendered DOM + receipt
//! identity, performs a full WKWebView `load_url` reload, and re-verifies rendered
//! receipt identity after reload.

use std::time::Duration;

use objc2::MainThreadMarker;
use objc2_foundation::NSSize;
use serde::{Deserialize, Serialize};

use crate::driver::Driver;

const PAGE: &str = include_str!("command_receipt_page.js");
const RETURNING_PERSON: &str = r#"try {
  localStorage.setItem("paintedwolf.app-state.v1.onboarding", JSON.stringify({ completedSetupRevision: 2, completedLocalModelStepRevision: 2 }));
  localStorage.setItem("paintedwolf.app-state.v1.firstTimeTips", JSON.stringify({ enabled: false }));
} catch {}"#;
const WINDOW: NSSize = NSSize::new(1100.0, 720.0);

#[derive(Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
struct ReceiptSnapshot {
    session_id: String,
    tool_call_id: String,
    program_id: String,
    command_state: String,
    receipt_identity_matched: bool,
    dom_has_assistant_reply: bool,
    dom_disclosure_count: usize,
    dom_has_command_text: bool,
    user_agent: String,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct SmokeEvidence {
    harness: &'static str,
    scope: &'static str,
    before_reload: ReceiptSnapshot,
    after_reload: ReceiptSnapshot,
    survived_reload: bool,
}

fn wait_for_harness(den: &Driver, timeout: Duration) -> Result<(), String> {
    let deadline = std::time::Instant::now() + timeout;
    while std::time::Instant::now() < deadline {
        if den.eval("window.__harness ? 'ready' : ''").as_deref() == Some("ready") {
            return Ok(());
        }
        den.spin(Duration::from_millis(250));
    }
    let href = den.eval("String(location.href)").unwrap_or_default();
    let ready = den.eval("String(document.readyState)").unwrap_or_default();
    Err(format!("window.__harness did not appear within {timeout:?} (href={href}, readyState={ready})"))
}

pub fn run(url: &str) -> i32 {
    let token = match std::env::var("LYCAON_E2E_TOKEN") {
        Ok(t) if !t.is_empty() => t,
        _ => {
            eprintln!("FAIL: LYCAON_E2E_TOKEN must be set");
            return 2;
        }
    };
    let mtm = MainThreadMarker::new().expect("main thread");
    let den = Driver::open(mtm, WINDOW, Some(RETURNING_PERSON));
    den.load_url(url);
    if let Err(err) = wait_for_harness(&den, Duration::from_secs(60)) {
        eprintln!("FAIL: WKWebView initial boot: {err}");
        return 1;
    }
    den.eval(PAGE);

    let tok_json = serde_json::to_string(&token).expect("encode token");
    let before_raw = match den.eval_async(
        &format!("window.__wkReceiptSmoke.runBeforeReload({tok_json})"),
        Duration::from_secs(120),
    ) {
        Ok(raw) => raw,
        Err(err) => {
            let step = den.eval("String(window.__wkReceiptSmoke && window.__wkReceiptSmoke.step)").unwrap_or_default();
            eprintln!("FAIL: WKWebView before-reload smoke failed: {err} (step={step})");
            return 1;
        }
    };
    let before: ReceiptSnapshot = match serde_json::from_str(&before_raw) {
        Ok(s) => s,
        Err(err) => {
            eprintln!("FAIL: decode before-reload snapshot: {err} ({before_raw})");
            return 1;
        }
    };

    // Real WKWebView reload via NSURLRequest.
    den.load_url(url);
    den.spin(Duration::from_secs(1));
    if let Err(err) = wait_for_harness(&den, Duration::from_secs(60)) {
        eprintln!("FAIL: WKWebView reload boot: {err}");
        return 1;
    }
    den.eval(PAGE);

    let sid_json = serde_json::to_string(&before.session_id).expect("encode sid");
    let cid_json = serde_json::to_string(&before.tool_call_id).expect("encode cid");
    let pid_json = serde_json::to_string(&before.program_id).expect("encode pid");
    let after_raw = match den.eval_async(
        &format!("window.__wkReceiptSmoke.runAfterReload({tok_json}, {sid_json}, {cid_json}, {pid_json})"),
        Duration::from_secs(90),
    ) {
        Ok(raw) => raw,
        Err(err) => {
            let step = den.eval("String(window.__wkReceiptSmoke && window.__wkReceiptSmoke.step)").unwrap_or_default();
            eprintln!("FAIL: WKWebView after-reload smoke failed: {err} (step={step})");
            return 1;
        }
    };
    let after: ReceiptSnapshot = match serde_json::from_str(&after_raw) {
        Ok(s) => s,
        Err(err) => {
            eprintln!("FAIL: decode after-reload snapshot: {err} ({after_raw})");
            return 1;
        }
    };

    let survived = before.receipt_identity_matched
        && after.receipt_identity_matched
        && !before.tool_call_id.is_empty()
        && before.tool_call_id == after.tool_call_id
        && before.program_id == after.program_id
        && before.dom_has_command_text
        && after.dom_has_command_text
        && after.dom_has_assistant_reply;

    let evidence = SmokeEvidence {
        harness: "webkit-harness (real macOS WKWebView)",
        scope: "WKWebView+Vite+real-sidecar receipt identity and reload; shipped Tauri shell and OS notifications remain separate",
        before_reload: before,
        after_reload: after,
        survived_reload: survived,
    };
    println!(
        "WKWEBVIEW_COMMAND_RECEIPT_EVIDENCE:{}",
        serde_json::to_string(&evidence).expect("encode evidence")
    );
    if survived { 0 } else { 1 }
}
