//! Native user presence for values a person stored.
//!
//! The engine issues a challenge whose signed payload names its purpose and
//! subject. This module asks the operating system to verify the person and
//! signs only a payload whose purpose and subject match the command that
//! asked, so neither command can sign for the other and the webview never
//! handles a payload or a signature.

use std::time::Duration;

use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine;
use serde::{Deserialize, Serialize};
use tauri::Manager;

use crate::sidecar::SidecarState;

const HTTP_TIMEOUT: Duration = Duration::from_secs(10);

#[derive(Debug, Deserialize)]
struct Challenge {
    challenge_id: String,
    proof_payload: String,
    prompt: String,
}

/// The fields this shell checks before signing a challenge payload.
#[derive(Debug, Deserialize)]
struct SignedPayload {
    protocol: String,
    purpose: String,
    window_label: String,
    subject: serde_json::Value,
}

/// What a command expects the engine to have bound.
struct Expectation<'a> {
    purpose: &'static str,
    subject: &'a [(&'static str, &'a str)],
}

/// Plaintext stays out of debug output.
#[derive(Deserialize, Serialize)]
pub struct ManagedSecretReveal {
    secret_value: String,
    version: i64,
    revealed_at: String,
    remask_after_ms: i64,
}

impl Drop for ManagedSecretReveal {
    fn drop(&mut self) {
        // SAFETY: zero bytes are valid UTF-8, so the string stays well-formed.
        unsafe { self.secret_value.as_mut_vec() }.fill(0);
    }
}

#[derive(Debug, Deserialize)]
struct HostError {
    code: String,
    message: String,
    title: Option<String>,
    suggested_action: Option<String>,
    #[serde(default)]
    actions: Vec<String>,
    scope: Option<String>,
    resolution: Option<String>,
}

#[derive(Debug, Serialize)]
pub struct PresenceCommandError {
    code: String,
    message: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    title: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    suggested_action: Option<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    actions: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    scope: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    resolution: Option<String>,
}

impl PresenceCommandError {
    fn unavailable(message: impl Into<String>) -> Self {
        Self {
            code: "presence_unavailable".into(),
            message: message.into(),
            title: Some("Confirmation is unavailable".into()),
            suggested_action: Some(
                "Restart the installed app with its bundled engine and try again.".into(),
            ),
            actions: Vec::new(),
            scope: Some("project".into()),
            resolution: None,
        }
    }

    fn with_suggested_action(mut self, action: impl Into<String>) -> Self {
        self.suggested_action = Some(action.into());
        self
    }

    fn denied(message: impl Into<String>) -> Self {
        Self {
            code: "presence_denied".into(),
            message: message.into(),
            title: Some("You were not confirmed".into()),
            suggested_action: Some("Try again and complete the operating-system prompt.".into()),
            actions: Vec::new(),
            scope: Some("project".into()),
            resolution: None,
        }
    }

    fn failed(message: impl Into<String>) -> Self {
        Self {
            code: "presence_failed".into(),
            message: message.into(),
            title: Some("This could not be confirmed".into()),
            suggested_action: Some(
                "Try again. If it keeps happening, report it from Settings → Advanced → Diagnostics."
                    .into(),
            ),
            actions: Vec::new(),
            scope: Some("project".into()),
            resolution: None,
        }
    }
}

impl From<HostError> for PresenceCommandError {
    fn from(host: HostError) -> Self {
        Self {
            code: host.code,
            message: host.message,
            title: host.title,
            suggested_action: host.suggested_action,
            actions: host.actions,
            scope: host.scope,
            resolution: host.resolution,
        }
    }
}

#[tauri::command(rename = "reveal_managed_secret")]
pub async fn ipc_reveal_managed_secret(
    window: tauri::Window,
    project_id: String,
    secret_id: String,
) -> Result<ManagedSecretReveal, PresenceCommandError> {
    let app = window.app_handle().clone();
    tauri::async_runtime::spawn_blocking(move || {
        let state = app.state::<SidecarState>();
        reveal_managed_secret(&state, &window, &project_id, &secret_id)
    })
    .await
    .map_err(|error| PresenceCommandError::failed(format!("reveal task join: {error}")))?
}

/// Approves one checkpoint option that sends values a person stored. While
/// the chat is locked the operating system confirms the person first, which
/// unlocks the chat; while it is unlocked the option is answered directly.
/// Returns the engine's checkpoint answer.
#[tauri::command(rename = "resolve_checkpoint_with_presence")]
pub async fn ipc_resolve_checkpoint_with_presence(
    window: tauri::Window,
    session_id: String,
    checkpoint_id: String,
    option_id: String,
) -> Result<serde_json::Value, PresenceCommandError> {
    let app = window.app_handle().clone();
    tauri::async_runtime::spawn_blocking(move || {
        let state = app.state::<SidecarState>();
        resolve_checkpoint_with_presence(&state, &window, &session_id, &checkpoint_id, &option_id)
    })
    .await
    .map_err(|error| PresenceCommandError::failed(format!("approval task join: {error}")))?
}

fn reveal_managed_secret(
    state: &SidecarState,
    window: &tauri::Window,
    project_id: &str,
    secret_id: &str,
) -> Result<ManagedSecretReveal, PresenceCommandError> {
    let project_id = parse_uuid(project_id)?;
    let secret_id = parse_uuid(secret_id)?;
    let engine = EngineClient::connect(state)?;
    let begin_url = format!(
        "{}/v1/projects/{project_id}/secrets/{secret_id}/reveal-challenges",
        engine.base
    );
    attest_and_send(
        state,
        window,
        &engine,
        &begin_url,
        serde_json::json!({"window_label": window.label()}),
        Expectation {
            purpose: "reveal",
            subject: &[("project_id", project_id.as_str()), ("secret_id", secret_id.as_str())],
        },
        |challenge, authenticator, signature| {
            (
                format!("{begin_url}/{}/complete", challenge.challenge_id),
                serde_json::json!({"authenticator": authenticator, "signature": signature}),
            )
        },
    )
}

fn resolve_checkpoint_with_presence(
    state: &SidecarState,
    window: &tauri::Window,
    session_id: &str,
    checkpoint_id: &str,
    option_id: &str,
) -> Result<serde_json::Value, PresenceCommandError> {
    let session_id = parse_uuid(session_id)?;
    let checkpoint_id = parse_uuid(checkpoint_id)?;
    let option_id = parse_option_id(option_id)?;
    let engine = EngineClient::connect(state)?;
    let checkpoint_url = format!(
        "{}/v1/sessions/{session_id}/checkpoints/{checkpoint_id}",
        engine.base
    );
    let attested = attest_and_send(
        state,
        window,
        &engine,
        &format!("{checkpoint_url}/unlock-challenges"),
        serde_json::json!({"option_id": option_id, "window_label": window.label()}),
        Expectation {
            purpose: "unlock",
            subject: &[
                ("session_id", session_id.as_str()),
                ("checkpoint_id", checkpoint_id.as_str()),
                ("option_id", option_id.as_str()),
            ],
        },
        |challenge, authenticator, signature| {
            (
                checkpoint_url.clone(),
                serde_json::json!({
                    "kind": "tool_approval",
                    "action": "approve",
                    "option_id": option_id,
                    "presence": {
                        "challenge_id": challenge.challenge_id,
                        "authenticator": authenticator,
                        "signature": signature,
                    },
                }),
            )
        },
    );
    match attested {
        // The chat is already unlocked, so the option is an ordinary choice.
        Err(error) if error.code == "presence_not_required" => send_json(
            engine.agent.post(&checkpoint_url),
            &engine.api_token,
            serde_json::json!({"kind": "tool_approval", "action": "approve", "option_id": option_id}),
        ),
        other => other,
    }
}

/// The app-started engine and a client bound to it.
struct EngineClient {
    agent: ureq::Agent,
    base: String,
    api_token: String,
}

impl EngineClient {
    fn connect(state: &SidecarState) -> Result<Self, PresenceCommandError> {
        let sidecar = state.cached_info().ok_or_else(|| {
            if crate::sidecar::attach_only_requested() {
                return PresenceCommandError::unavailable(
                    "Development attach mode cannot confirm you because this app did not start and bind the engine's native proof key.",
                )
                .with_suggested_action(
                    "Run `./task den:app -- --debug` and launch that app with a scratch LYCAON_CONFIG_DIR; release provisioning is not required.",
                );
            }
            PresenceCommandError::unavailable(
                "The app-started engine is not running, so it cannot confirm you.",
            )
        })?;
        let agent = ureq::AgentBuilder::new()
            .timeout_connect(HTTP_TIMEOUT)
            .timeout_read(HTTP_TIMEOUT)
            .timeout_write(HTTP_TIMEOUT)
            .build();
        Ok(Self {
            agent,
            base: format!("http://127.0.0.1:{}", sidecar.port),
            api_token: sidecar.api_token,
        })
    }
}

/// Begins a challenge, checks what it binds, confirms the person, signs, and
/// sends the completion `complete` builds from the signed challenge.
fn attest_and_send<T, F>(
    state: &SidecarState,
    window: &tauri::Window,
    engine: &EngineClient,
    begin_url: &str,
    begin_body: serde_json::Value,
    expect: Expectation<'_>,
    complete: F,
) -> Result<T, PresenceCommandError>
where
    T: for<'de> Deserialize<'de>,
    F: FnOnce(&Challenge, &str, &str) -> (String, serde_json::Value),
{
    let _gate = state
        .presence_gate
        .lock()
        .map_err(|_| PresenceCommandError::failed("confirmation lock is unavailable"))?;
    require_active_window(window)?;
    let challenge: Challenge = send_json(
        engine.agent.post(begin_url),
        &engine.api_token,
        begin_body,
    )?;
    check_payload(&challenge.proof_payload, window.label(), &expect)?;
    let authenticator = verify_user_presence(window, &challenge.prompt)?;
    // Closing the native prompt can delay focus returning to this window.
    let deadline = std::time::Instant::now() + Duration::from_millis(500);
    while !window.is_focused().unwrap_or(false) && std::time::Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(20));
    }
    require_active_window(window)?;
    let signature = state.sign_presence(&challenge.proof_payload, authenticator);
    let (url, body) = complete(&challenge, authenticator, &signature);
    let result = send_json(engine.agent.post(&url), &engine.api_token, body)?;
    require_active_window(window)?;
    Ok(result)
}

/// Refuses to sign a payload that binds anything but what the command asked.
fn check_payload(
    encoded: &str,
    window_label: &str,
    expect: &Expectation<'_>,
) -> Result<(), PresenceCommandError> {
    let invalid = || PresenceCommandError::failed("the engine issued a challenge this app will not sign");
    let raw = URL_SAFE_NO_PAD.decode(encoded).map_err(|_| invalid())?;
    let payload: SignedPayload = serde_json::from_slice(&raw).map_err(|_| invalid())?;
    if payload.protocol != "painted-wolf-presence-v1"
        || payload.purpose != expect.purpose
        || payload.window_label != window_label
    {
        return Err(invalid());
    }
    for (field, want) in expect.subject {
        if payload.subject.get(*field).and_then(|value| value.as_str()) != Some(*want) {
            return Err(invalid());
        }
    }
    Ok(())
}

fn require_active_window(window: &tauri::Window) -> Result<(), PresenceCommandError> {
    if window.is_visible().unwrap_or(false)
        && window.is_focused().unwrap_or(false)
        && !window.is_minimized().unwrap_or(true)
    {
        return Ok(());
    }
    Err(PresenceCommandError::denied(
        "The originating window is no longer active. Return to it and try again.",
    ))
}

fn parse_uuid(raw: &str) -> Result<String, PresenceCommandError> {
    uuid::Uuid::parse_str(raw.trim())
        .map(|value| value.to_string())
        .map_err(|_| PresenceCommandError::failed("the confirmation target is invalid"))
}

fn parse_option_id(raw: &str) -> Result<String, PresenceCommandError> {
    let raw = raw.trim();
    let valid = !raw.is_empty()
        && raw.len() <= 256
        && raw
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || matches!(c, '_' | '.' | ':' | '-'));
    if valid {
        Ok(raw.to_string())
    } else {
        Err(PresenceCommandError::failed("the approval option is invalid"))
    }
}

fn send_json<T: for<'de> Deserialize<'de>>(
    request: ureq::Request,
    api_token: &str,
    body: serde_json::Value,
) -> Result<T, PresenceCommandError> {
    let encoded = serde_json::to_string(&body)
        .map_err(|_| PresenceCommandError::failed("could not encode the request"))?;
    let response = request
        .set("Authorization", &format!("Bearer {api_token}"))
        .set("Content-Type", "application/json")
        .send_string(&encoded);
    match response {
        Ok(response) => response
            .into_string()
            .ok()
            .and_then(|raw| serde_json::from_str::<T>(&raw).ok())
            .ok_or_else(|| PresenceCommandError::failed("the engine returned an invalid response")),
        Err(ureq::Error::Status(_, response)) => {
            let host = response
                .into_string()
                .ok()
                .and_then(|raw| serde_json::from_str::<HostError>(&raw).ok());
            Err(match host {
                Some(host) => host.into(),
                None => PresenceCommandError::failed("the engine refused the request"),
            })
        }
        Err(_) => Err(PresenceCommandError::unavailable(
            "The engine became unavailable while confirming you.",
        )),
    }
}

#[cfg(target_os = "macos")]
fn verify_user_presence(
    _window: &tauri::Window,
    prompt: &str,
) -> Result<&'static str, PresenceCommandError> {
    use std::sync::mpsc;

    use block2::RcBlock;
    use objc2::runtime::Bool;
    use objc2_foundation::{NSError, NSString};
    use objc2_local_authentication::{LAContext, LAPolicy};

    let context = unsafe { LAContext::new() };
    unsafe { context.canEvaluatePolicy_error(LAPolicy::DeviceOwnerAuthentication) }.map_err(
        |_| {
            PresenceCommandError::unavailable(
                "Set up Touch ID or a login password so this Mac can confirm you.",
            )
            .with_suggested_action(
                "Set up Touch ID or a login password in System Settings, then try again.",
            )
        },
    )?;
    let reason = NSString::from_str(prompt);
    let (tx, rx) = mpsc::channel();
    let reply = RcBlock::new(move |success: Bool, _error: *mut NSError| {
        let _ = tx.send(success.as_bool());
    });
    unsafe {
        context.evaluatePolicy_localizedReason_reply(
            LAPolicy::DeviceOwnerAuthentication,
            &reason,
            &reply,
        )
    };
    match rx.recv_timeout(Duration::from_secs(120)) {
        Ok(true) => Ok("macos_user_presence"),
        Ok(false) => Err(PresenceCommandError::denied(
            "Authentication was canceled or did not succeed.",
        )),
        Err(_) => {
            unsafe { context.invalidate() };
            Err(PresenceCommandError::denied(
                "Authentication did not finish in time.",
            ))
        }
    }
}

#[cfg(target_os = "windows")]
fn verify_user_presence(
    window: &tauri::Window,
    prompt: &str,
) -> Result<&'static str, PresenceCommandError> {
    use windows::core::{factory, HSTRING};
    use windows::Foundation::IAsyncOperation;
    use windows::Security::Credentials::UI::{UserConsentVerificationResult, UserConsentVerifier};
    use windows::Win32::System::WinRT::IUserConsentVerifierInterop;

    let hello_action =
        || "Set up Windows Hello in Windows Settings, restart the app, and try again.";
    let _apartment = windows::core::initialize_mta().map_err(|_| {
        PresenceCommandError::unavailable("Windows Hello could not be initialized.")
            .with_suggested_action(hello_action())
    })?;
    let interop = factory::<UserConsentVerifier, IUserConsentVerifierInterop>().map_err(|_| {
        PresenceCommandError::unavailable("Windows Hello is not available on this device.")
            .with_suggested_action(hello_action())
    })?;
    let hwnd = window.hwnd().map_err(|_| {
        PresenceCommandError::unavailable(
            "The current app window cannot host a Windows Hello prompt.",
        )
        .with_suggested_action("Close other app windows, reopen this one, and try again.")
    })?;
    let operation: IAsyncOperation<UserConsentVerificationResult> =
        unsafe { interop.RequestVerificationForWindowAsync(hwnd, &HSTRING::from(prompt)) }
            .map_err(|_| {
                PresenceCommandError::unavailable("Windows Hello could not start.")
                    .with_suggested_action(hello_action())
            })?;
    match operation.get() {
        Ok(result) if result == UserConsentVerificationResult::Verified => {
            Ok("windows_user_presence")
        }
        Ok(_) => Err(PresenceCommandError::denied(
            "Windows Hello was canceled or did not succeed.",
        )),
        Err(_) => Err(PresenceCommandError::denied(
            "Windows Hello could not confirm you.",
        )),
    }
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
fn verify_user_presence(
    _window: &tauri::Window,
    _prompt: &str,
) -> Result<&'static str, PresenceCommandError> {
    Err(PresenceCommandError::unavailable(
        "This operating system cannot confirm you.",
    )
    .with_suggested_action(
        "Use the installed desktop app on macOS or Windows for values you gave Painted Wolf Code.",
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn payload(purpose: &str, window: &str, subject: serde_json::Value) -> String {
        URL_SAFE_NO_PAD.encode(
            serde_json::to_vec(&serde_json::json!({
                "protocol": "painted-wolf-presence-v1",
                "purpose": purpose,
                "challenge_id": "00000000-0000-4000-8000-000000000001",
                "nonce": "n",
                "window_label": window,
                "expires_at": "2026-10-01T00:00:00Z",
                "subject": subject,
            }))
            .unwrap(),
        )
    }

    #[test]
    fn target_ids_must_be_uuids() {
        assert!(parse_uuid("00000000-0000-4000-8000-000000000001").is_ok());
        assert!(parse_uuid("../secrets/another").is_err());
    }

    #[test]
    fn option_ids_keep_the_wire_alphabet() {
        assert!(parse_option_id("lease:chat-1").is_ok());
        assert!(parse_option_id("../checkpoints").is_err());
        assert!(parse_option_id("").is_err());
    }

    #[test]
    fn an_unlock_payload_signs_only_for_its_checkpoint_and_option() {
        let unlock = Expectation {
            purpose: "unlock",
            subject: &[("checkpoint_id", "c1"), ("option_id", "o1")],
        };
        let good = payload(
            "unlock",
            "main",
            serde_json::json!({"checkpoint_id": "c1", "option_id": "o1"}),
        );
        assert!(check_payload(&good, "main", &unlock).is_ok());
        let other_option = payload(
            "unlock",
            "main",
            serde_json::json!({"checkpoint_id": "c1", "option_id": "o2"}),
        );
        assert!(check_payload(&other_option, "main", &unlock).is_err());
        assert!(check_payload(&good, "other-window", &unlock).is_err());
    }

    #[test]
    fn a_reveal_payload_never_signs_as_an_unlock() {
        let unlock = Expectation {
            purpose: "unlock",
            subject: &[("checkpoint_id", "c1")],
        };
        let reveal = payload("reveal", "main", serde_json::json!({"checkpoint_id": "c1"}));
        assert!(check_payload(&reveal, "main", &unlock).is_err());
        assert!(check_payload("not-base64!", "main", &unlock).is_err());
    }

    #[test]
    fn host_notice_actions_reach_the_frontend() {
        let host: HostError = serde_json::from_str(
            r#"{"code":"no_model","message":"No model","actions":["open_ai_providers","prompt_retry"]}"#,
        )
        .unwrap();
        let wire = serde_json::to_value(PresenceCommandError::from(host)).unwrap();
        assert_eq!(
            wire["actions"],
            serde_json::json!(["open_ai_providers", "prompt_retry"])
        );
        assert!(wire.get("action").is_none());
    }

    #[test]
    fn a_host_error_without_actions_carries_none() {
        let host: HostError =
            serde_json::from_str(r#"{"code":"presence_denied","message":"No"}"#).unwrap();
        let wire = serde_json::to_value(PresenceCommandError::from(host)).unwrap();
        assert!(wire.get("actions").is_none());
    }
}
