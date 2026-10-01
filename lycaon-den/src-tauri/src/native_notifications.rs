//! Native macOS notifications.
//!
//! Packaged builds support foreground banners and click routing. Development
//! binaries are not app bundles, so notification setup reports `unavailable`.

#![cfg(target_os = "macos")]

use std::ptr::NonNull;
use std::sync::mpsc;
use std::sync::Mutex;
use std::time::{SystemTime, UNIX_EPOCH};

use block2::RcBlock;
use objc2::rc::Retained;
use objc2::runtime::{Bool, ProtocolObject};
use objc2::{define_class, msg_send, AnyThread};
use objc2_foundation::{
    NSArray, NSBundle, NSDictionary, NSError, NSObject, NSObjectProtocol, NSString,
};
use objc2_user_notifications::{
    UNAuthorizationOptions, UNAuthorizationStatus, UNMutableNotificationContent, UNNotification,
    UNNotificationPresentationOptions, UNNotificationRequest, UNNotificationResponse,
    UNNotificationSettings, UNUserNotificationCenter, UNUserNotificationCenterDelegate,
};
use serde::Serialize;
use tauri::{AppHandle, Emitter};

pub const ACTIVATED_EVENT: &str = "notifications://activated";
const SESSION_KEY: &str = "sessionId";

#[derive(Debug, Clone, Copy, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum AuthState {
    NotDetermined,
    Denied,
    Authorized,
    Provisional,
    Ephemeral,
    /// Process has no application bundle, so notification APIs stay disabled.
    Unavailable,
}

impl AuthState {
    pub fn as_str(self) -> &'static str {
        match self {
            AuthState::NotDetermined => "not_determined",
            AuthState::Denied => "denied",
            AuthState::Authorized => "authorized",
            AuthState::Provisional => "provisional",
            AuthState::Ephemeral => "ephemeral",
            AuthState::Unavailable => "unavailable",
        }
    }
}

impl From<UNAuthorizationStatus> for AuthState {
    fn from(s: UNAuthorizationStatus) -> Self {
        match s {
            UNAuthorizationStatus::NotDetermined => AuthState::NotDetermined,
            UNAuthorizationStatus::Denied => AuthState::Denied,
            UNAuthorizationStatus::Authorized => AuthState::Authorized,
            UNAuthorizationStatus::Provisional => AuthState::Provisional,
            UNAuthorizationStatus::Ephemeral => AuthState::Ephemeral,
            _ => AuthState::NotDetermined,
        }
    }
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ActivatedPayload {
    pub session_id: Option<String>,
}

static APP_HANDLE: Mutex<Option<AppHandle>> = Mutex::new(None);
static DELEGATE: Mutex<Option<Retained<DenNotificationDelegate>>> = Mutex::new(None);

define_class!(
    #[unsafe(super(NSObject))]
    #[name = "DenNotificationDelegate"]
    struct DenNotificationDelegate;

    unsafe impl UNUserNotificationCenterDelegate for DenNotificationDelegate {
        #[unsafe(method(userNotificationCenter:willPresentNotification:withCompletionHandler:))]
        fn user_notification_center_will_present_notification_with_completion_handler(
            &self,
            _center: &UNUserNotificationCenter,
            _notification: &UNNotification,
            completion_handler: &block2::DynBlock<dyn Fn(UNNotificationPresentationOptions)>,
        ) {
            // Show banner + list while the app is foregrounded (test button,
            // and any emit that races focus). Sound stays off.
            completion_handler
                .call((UNNotificationPresentationOptions::Banner
                    | UNNotificationPresentationOptions::List,));
        }

        #[unsafe(method(userNotificationCenter:didReceiveNotificationResponse:withCompletionHandler:))]
        fn user_notification_center_did_receive_notification_response_with_completion_handler(
            &self,
            _center: &UNUserNotificationCenter,
            response: &UNNotificationResponse,
            completion_handler: &block2::DynBlock<dyn Fn()>,
        ) {
            let session_id = session_id_from_response(response);
            emit_activated(session_id);
            completion_handler.call(());
        }
    }

    unsafe impl NSObjectProtocol for DenNotificationDelegate {}
);

impl DenNotificationDelegate {
    fn new() -> Retained<Self> {
        unsafe { msg_send![Self::alloc(), init] }
    }
}

fn session_id_from_response(response: &UNNotificationResponse) -> Option<String> {
    let user_info = response.notification().request().content().userInfo();
    let key = NSString::from_str(SESSION_KEY);
    let value = user_info.objectForKey(&key)?;
    let as_string: Option<Retained<NSString>> = value.downcast().ok();
    as_string.map(|s| s.to_string()).filter(|s| !s.is_empty())
}

fn emit_activated(session_id: Option<String>) {
    let guard = APP_HANDLE.lock().unwrap_or_else(|e| e.into_inner());
    if let Some(app) = guard.as_ref() {
        let _ = app.emit(ACTIVATED_EVENT, ActivatedPayload { session_id });
    }
}

fn ns_error_message(error: *mut NSError) -> String {
    if error.is_null() {
        return "unknown UN error".to_string();
    }
    // SAFETY: non-null pointer from UN completion handler.
    let err = unsafe { &*error };
    err.localizedDescription().to_string()
}

/// Reports whether the process has an application bundle for notifications.
pub fn process_supports_un() -> bool {
    let bundle = NSBundle::mainBundle();
    let path = bundle.bundlePath().to_string();
    if !path.ends_with(".app") {
        return false;
    }
    bundle
        .bundleIdentifier()
        .map(|id| !id.to_string().is_empty())
        .unwrap_or(false)
}

/// Retains the app handle for clicks even when native notifications are unavailable.
pub fn setup(app: AppHandle) -> Result<(), String> {
    {
        let mut guard = APP_HANDLE.lock().unwrap_or_else(|e| e.into_inner());
        *guard = Some(app);
    }

    if !process_supports_un() {
        return Ok(());
    }

    let delegate = DenNotificationDelegate::new();
    let center = UNUserNotificationCenter::currentNotificationCenter();
    center.setDelegate(Some(ProtocolObject::from_ref(&*delegate)));

    let mut guard = DELEGATE.lock().unwrap_or_else(|e| e.into_inner());
    *guard = Some(delegate);
    Ok(())
}

/// Prompt once (macOS caches the decision). Returns the resulting state.
pub fn request_authorization() -> Result<AuthState, String> {
    if !process_supports_un() {
        return Ok(AuthState::Unavailable);
    }

    let center = UNUserNotificationCenter::currentNotificationCenter();
    let options = UNAuthorizationOptions::Alert
        | UNAuthorizationOptions::Sound
        | UNAuthorizationOptions::Badge;

    let (tx, rx) = mpsc::channel::<Result<(), String>>();
    let tx_clone = tx.clone();
    let handler = RcBlock::new(move |_granted: Bool, error: *mut NSError| {
        let result = if error.is_null() {
            Ok(())
        } else {
            Err(ns_error_message(error))
        };
        let _ = tx_clone.send(result);
    });

    center.requestAuthorizationWithOptions_completionHandler(options, &handler);
    drop(tx);

    rx.recv()
        .map_err(|e| format!("UN auth completion never fired: {e}"))??;

    authorization_state()
}

/// Query state without prompting.
pub fn authorization_state() -> Result<AuthState, String> {
    if !process_supports_un() {
        return Ok(AuthState::Unavailable);
    }

    let center = UNUserNotificationCenter::currentNotificationCenter();

    let (tx, rx) = mpsc::channel::<AuthState>();
    let tx_clone = tx.clone();
    let handler = RcBlock::new(move |settings: NonNull<UNNotificationSettings>| {
        // SAFETY: UN guarantees a non-null settings object.
        let state = unsafe { settings.as_ref().authorizationStatus() }.into();
        let _ = tx_clone.send(state);
    });

    center.getNotificationSettingsWithCompletionHandler(&handler);
    drop(tx);

    rx.recv()
        .map_err(|e| format!("UN settings completion never fired: {e}"))
}

/// Deliver a notification immediately. Optional sessionId is carried in
/// userInfo for click routing.
pub fn send(title: &str, body: &str, session_id: Option<&str>) -> Result<(), String> {
    if !process_supports_un() {
        return Err("UN unavailable outside .app bundle".to_string());
    }

    let center = UNUserNotificationCenter::currentNotificationCenter();

    let content = UNMutableNotificationContent::new();
    content.setTitle(&NSString::from_str(title));
    content.setBody(&NSString::from_str(body));

    if let Some(sid) = session_id.filter(|s| !s.is_empty()) {
        let key = NSString::from_str(SESSION_KEY);
        let val = NSString::from_str(sid);
        let info = NSDictionary::from_slices(&[&*key], &[&*val]);
        // SAFETY: plain string userInfo; erase typed NSDictionary to the
        // unparameterized form UNMutableNotificationContent expects.
        unsafe {
            let erased: &NSDictionary =
                &*(&*info as *const NSDictionary<NSString, NSString> as *const NSDictionary);
            content.setUserInfo(erased);
        }
    }

    let id_str = notification_identifier(session_id);
    let id_ns = NSString::from_str(&id_str);
    let request =
        UNNotificationRequest::requestWithIdentifier_content_trigger(&id_ns, &content, None);

    let (tx, rx) = mpsc::channel::<Result<(), String>>();
    let tx_clone = tx.clone();
    let handler = RcBlock::new(move |error: *mut NSError| {
        let result = if error.is_null() {
            Ok(())
        } else {
            Err(ns_error_message(error))
        };
        let _ = tx_clone.send(result);
    });

    center.addNotificationRequest_withCompletionHandler(&request, Some(&handler));
    drop(tx);

    rx.recv()
        .map_err(|e| format!("UN add completion never fired: {e}"))?
}

fn notification_identifier(session_id: Option<&str>) -> String {
    match session_id.filter(|s| !s.is_empty()) {
        Some(sid) => format!("den-session-{sid}"),
        None => format!("den-{}", monotonic_nanos()),
    }
}

/// Cancel delivered and pending banners for these sessions.
pub fn cancel_sessions(session_ids: &[String]) -> Result<(), String> {
    if !process_supports_un() {
        return Ok(());
    }
    let idents: Vec<Retained<NSString>> = session_ids
        .iter()
        .map(|s| s.trim())
        .filter(|s| !s.is_empty())
        .map(|s| NSString::from_str(&format!("den-session-{s}")))
        .collect();
    if idents.is_empty() {
        return Ok(());
    }
    let refs: Vec<&NSString> = idents.iter().map(|s| &**s).collect();
    let array = NSArray::from_slice(&refs);
    let center = UNUserNotificationCenter::currentNotificationCenter();
    center.removeDeliveredNotificationsWithIdentifiers(&array);
    center.removePendingNotificationRequestsWithIdentifiers(&array);
    Ok(())
}

fn monotonic_nanos() -> u128 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or(0)
}
