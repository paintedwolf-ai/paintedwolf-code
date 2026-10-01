//! Native-authenticated managed-secret reveal.

use std::time::Duration;

use serde::{Deserialize, Serialize};
use tauri::Manager;

use crate::sidecar::SidecarState;

const HTTP_TIMEOUT: Duration = Duration::from_secs(10);

#[derive(Debug, Deserialize)]
struct RevealChallenge {
    challenge_id: String,
    proof_payload: String,
    prompt: String,
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
pub struct RevealCommandError {
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

impl RevealCommandError {
    fn unavailable(message: impl Into<String>) -> Self {
        Self {
            code: "managed_secret_reveal_unavailable".into(),
            message: message.into(),
            title: Some("Secret reveal is unavailable".into()),
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
            code: "managed_secret_reveal_denied".into(),
            message: message.into(),
            title: Some("Secret reveal was not authorized".into()),
            suggested_action: Some(
                "Choose Reveal value again and complete the operating-system prompt.".into(),
            ),
            actions: Vec::new(),
            scope: Some("project".into()),
            resolution: None,
        }
    }

    fn failed(message: impl Into<String>) -> Self {
        Self {
            code: "managed_secret_reveal_failed".into(),
            message: message.into(),
            title: Some("This secret could not be revealed".into()),
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

impl From<HostError> for RevealCommandError {
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
) -> Result<ManagedSecretReveal, RevealCommandError> {
    let app = window.app_handle().clone();
    tauri::async_runtime::spawn_blocking(move || {
        let state = app.state::<SidecarState>();
        reveal_managed_secret(&state, &window, &project_id, &secret_id)
    })
    .await
    .map_err(|error| RevealCommandError::failed(format!("reveal task join: {error}")))?
}

fn reveal_managed_secret(
    state: &SidecarState,
    window: &tauri::Window,
    project_id: &str,
    secret_id: &str,
) -> Result<ManagedSecretReveal, RevealCommandError> {
    let _gate = state
        .reveal_gate
        .lock()
        .map_err(|_| RevealCommandError::failed("reveal lock is unavailable"))?;
    require_active_window(window)?;
    let project_id = parse_uuid(project_id)?;
    let secret_id = parse_uuid(secret_id)?;
    let sidecar = state.cached_info().ok_or_else(|| {
        if crate::sidecar::attach_only_requested() {
            return RevealCommandError::unavailable(
                "Development attach mode cannot reveal managed secrets because this app did not start and bind the engine's native proof key.",
            )
            .with_suggested_action(
                "Run `./task den:app -- --debug` and launch that app with a scratch LYCAON_CONFIG_DIR; release provisioning is not required.",
            );
        }
        RevealCommandError::unavailable(
            "The app-started engine is not running, so it cannot reveal this secret.",
        )
    })?;
    let agent = ureq::AgentBuilder::new()
        .timeout_connect(HTTP_TIMEOUT)
        .timeout_read(HTTP_TIMEOUT)
        .timeout_write(HTTP_TIMEOUT)
        .build();
    let base = format!("http://127.0.0.1:{}", sidecar.port);
    let begin_url =
        format!("{base}/v1/projects/{project_id}/secrets/{secret_id}/reveal-challenges");
    let challenge: RevealChallenge = send_json(
        agent.post(&begin_url),
        &sidecar.api_token,
        serde_json::json!({"window_label": window.label()}),
    )?;

    let authenticator = verify_user_presence(window, &challenge.prompt)?;
    // Closing the native prompt can delay focus returning to this window.
    let deadline = std::time::Instant::now() + Duration::from_millis(500);
    while !window.is_focused().unwrap_or(false) && std::time::Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(20));
    }
    require_active_window(window)?;
    let signature = state.sign_reveal(&challenge.proof_payload, authenticator);
    let complete_url = format!("{begin_url}/{}/complete", challenge.challenge_id);
    let result = send_json(
        agent.post(&complete_url),
        &sidecar.api_token,
        serde_json::json!({"authenticator": authenticator, "signature": signature}),
    )?;
    require_active_window(window)?;
    Ok(result)
}

fn require_active_window(window: &tauri::Window) -> Result<(), RevealCommandError> {
    if window.is_visible().unwrap_or(false)
        && window.is_focused().unwrap_or(false)
        && !window.is_minimized().unwrap_or(true)
    {
        return Ok(());
    }
    Err(RevealCommandError::denied(
        "The originating window is no longer active. Return to it and reveal the value again.",
    ))
}

fn parse_uuid(raw: &str) -> Result<String, RevealCommandError> {
    uuid::Uuid::parse_str(raw.trim())
        .map(|value| value.to_string())
        .map_err(|_| RevealCommandError::failed("managed secret reveal target is invalid"))
}

fn send_json<T: for<'de> Deserialize<'de>>(
    request: ureq::Request,
    api_token: &str,
    body: serde_json::Value,
) -> Result<T, RevealCommandError> {
    let encoded = serde_json::to_string(&body)
        .map_err(|_| RevealCommandError::failed("could not encode the reveal request"))?;
    let response = request
        .set("Authorization", &format!("Bearer {api_token}"))
        .set("Content-Type", "application/json")
        .send_string(&encoded);
    match response {
        Ok(response) => response
            .into_string()
            .ok()
            .and_then(|raw| serde_json::from_str::<T>(&raw).ok())
            .ok_or_else(|| {
                RevealCommandError::failed("engine returned an invalid reveal response")
            }),
        Err(ureq::Error::Status(_, response)) => {
            let host = response
                .into_string()
                .ok()
                .and_then(|raw| serde_json::from_str::<HostError>(&raw).ok());
            Err(match host {
                Some(host) => host.into(),
                None => RevealCommandError::failed("engine refused the reveal request"),
            })
        }
        Err(_) => Err(RevealCommandError::unavailable(
            "The engine became unavailable while revealing this secret.",
        )),
    }
}

#[cfg(target_os = "macos")]
fn verify_user_presence(
    _window: &tauri::Window,
    prompt: &str,
) -> Result<&'static str, RevealCommandError> {
    use std::sync::mpsc;

    use block2::RcBlock;
    use objc2::runtime::Bool;
    use objc2_foundation::{NSError, NSString};
    use objc2_local_authentication::{LAContext, LAPolicy};

    let context = unsafe { LAContext::new() };
    unsafe { context.canEvaluatePolicy_error(LAPolicy::DeviceOwnerAuthentication) }.map_err(
        |_| {
            RevealCommandError::unavailable(
                "Set up Touch ID or a login password to reveal managed secrets.",
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
        Ok(false) => Err(RevealCommandError::denied(
            "Authentication was canceled or did not succeed.",
        )),
        Err(_) => {
            unsafe { context.invalidate() };
            Err(RevealCommandError::denied(
                "Authentication did not finish in time.",
            ))
        }
    }
}

#[cfg(target_os = "windows")]
fn verify_user_presence(
    window: &tauri::Window,
    prompt: &str,
) -> Result<&'static str, RevealCommandError> {
    use windows::core::{factory, HSTRING};
    use windows::Foundation::IAsyncOperation;
    use windows::Security::Credentials::UI::{UserConsentVerificationResult, UserConsentVerifier};
    use windows::Win32::System::WinRT::IUserConsentVerifierInterop;

    let hello_action =
        || "Set up Windows Hello in Windows Settings, restart the app, and try again.";
    let _apartment = windows::core::initialize_mta().map_err(|_| {
        RevealCommandError::unavailable("Windows Hello could not be initialized.")
            .with_suggested_action(hello_action())
    })?;
    let interop = factory::<UserConsentVerifier, IUserConsentVerifierInterop>().map_err(|_| {
        RevealCommandError::unavailable("Windows Hello is not available on this device.")
            .with_suggested_action(hello_action())
    })?;
    let hwnd = window.hwnd().map_err(|_| {
        RevealCommandError::unavailable(
            "The current app window cannot host a Windows Hello prompt.",
        )
        .with_suggested_action("Close other app windows, reopen this one, and try again.")
    })?;
    let operation: IAsyncOperation<UserConsentVerificationResult> =
        unsafe { interop.RequestVerificationForWindowAsync(hwnd, &HSTRING::from(prompt)) }
            .map_err(|_| {
                RevealCommandError::unavailable("Windows Hello could not start.")
                    .with_suggested_action(hello_action())
            })?;
    match operation.get() {
        Ok(result) if result == UserConsentVerificationResult::Verified => {
            Ok("windows_user_presence")
        }
        Ok(_) => Err(RevealCommandError::denied(
            "Windows Hello was canceled or did not succeed.",
        )),
        Err(_) => Err(RevealCommandError::denied(
            "Windows Hello could not verify this reveal.",
        )),
    }
}

#[cfg(not(any(target_os = "macos", target_os = "windows")))]
fn verify_user_presence(
    _window: &tauri::Window,
    _prompt: &str,
) -> Result<&'static str, RevealCommandError> {
    Err(RevealCommandError::unavailable(
        "Authenticated reveal is not available on this operating system.",
    )
    .with_suggested_action(
        "Use the installed desktop app on macOS or Windows to reveal managed secrets.",
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn target_ids_must_be_uuids() {
        assert!(parse_uuid("00000000-0000-4000-8000-000000000001").is_ok());
        assert!(parse_uuid("../secrets/another").is_err());
    }

    #[test]
    fn host_notice_actions_reach_the_frontend() {
        let host: HostError = serde_json::from_str(
            r#"{"code":"no_model","message":"No model","actions":["open_ai_providers","prompt_retry"]}"#,
        )
        .unwrap();
        let wire = serde_json::to_value(RevealCommandError::from(host)).unwrap();
        assert_eq!(
            wire["actions"],
            serde_json::json!(["open_ai_providers", "prompt_retry"])
        );
        assert!(wire.get("action").is_none());
    }

    #[test]
    fn a_host_error_without_actions_carries_none() {
        let host: HostError =
            serde_json::from_str(r#"{"code":"managed_secret_reveal_denied","message":"No"}"#)
                .unwrap();
        let wire = serde_json::to_value(RevealCommandError::from(host)).unwrap();
        assert!(wire.get("actions").is_none());
    }
}
