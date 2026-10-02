//! Locks every chat's stored values when the person steps away.
//!
//! The engine ends idle unlocks itself; only the desktop sees the screen
//! lock, sleep, a user switch, or quit. Locking only removes authority, so
//! a lost request costs at most the rest of an unlock. Windows lock events
//! are not observed yet.

use std::time::Duration;

use tauri::{AppHandle, Manager};

use crate::sidecar::SidecarState;

const LOCK_TIMEOUT: Duration = Duration::from_secs(2);

/// Why the shell asked the engine to lock. The wire names match the
/// engine's `LockVaultRequest.reason`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LockReason {
    ScreenLocked,
    Sleep,
    AppQuit,
}

impl LockReason {
    fn wire(self) -> &'static str {
        match self {
            LockReason::ScreenLocked => "screen_locked",
            LockReason::Sleep => "sleep",
            LockReason::AppQuit => "app_quit",
        }
    }
}

/// Asks the app-started engine to lock every chat. Best effort: an engine
/// that is not running holds no unlocks.
pub fn lock_now(state: &SidecarState, reason: LockReason) {
    let Some(sidecar) = state.cached_info() else {
        return;
    };
    let agent = ureq::AgentBuilder::new()
        .timeout_connect(LOCK_TIMEOUT)
        .timeout_read(LOCK_TIMEOUT)
        .timeout_write(LOCK_TIMEOUT)
        .build();
    let body = format!(r#"{{"reason":"{}"}}"#, reason.wire());
    let result = agent
        .post(&format!("http://127.0.0.1:{}/v1/vault/lock", sidecar.port))
        .set("Authorization", &format!("Bearer {}", sidecar.api_token))
        .set("Content-Type", "application/json")
        .send_string(&body);
    if let Err(error) = result {
        eprintln!("Could not lock stored values ({}): {error}", reason.wire());
    }
}

/// Locks off the calling thread, so a notification handler never waits on
/// the engine.
fn lock_in_background(app: &AppHandle, reason: LockReason) {
    let app = app.clone();
    std::thread::spawn(move || {
        if let Some(state) = app.try_state::<SidecarState>() {
            lock_now(&state, reason);
        }
    });
}

/// Registers the step-away observers.
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

#[cfg(target_os = "macos")]
mod macos {
    use super::*;
    use std::sync::Mutex;

    use objc2::rc::Retained;
    use objc2::{define_class, msg_send, sel, AnyThread};
    use objc2_app_kit::{
        NSWorkspace, NSWorkspaceScreensDidSleepNotification,
        NSWorkspaceSessionDidResignActiveNotification, NSWorkspaceWillSleepNotification,
    };
    use objc2_foundation::{
        NSDistributedNotificationCenter, NSNotification, NSObject, NSObjectProtocol, NSString,
    };

    static APP_HANDLE: Mutex<Option<AppHandle>> = Mutex::new(None);
    // Keep the observer alive.
    static OBSERVER: Mutex<Option<Retained<DenVaultLockObserver>>> = Mutex::new(None);

    fn lock(reason: LockReason) {
        let guard = APP_HANDLE.lock().unwrap_or_else(|e| e.into_inner());
        if let Some(app) = guard.as_ref() {
            lock_in_background(app, reason);
        }
    }

    define_class!(
        #[unsafe(super(NSObject))]
        #[name = "DenVaultLockObserver"]
        struct DenVaultLockObserver;

        impl DenVaultLockObserver {
            #[unsafe(method(onWillSleep:))]
            fn on_will_sleep(&self, _notification: &NSNotification) {
                lock(LockReason::Sleep);
            }

            #[unsafe(method(onScreenLocked:))]
            fn on_screen_locked(&self, _notification: &NSNotification) {
                lock(LockReason::ScreenLocked);
            }
        }

        unsafe impl NSObjectProtocol for DenVaultLockObserver {}
    );

    impl DenVaultLockObserver {
        fn new() -> Retained<Self> {
            unsafe { msg_send![Self::alloc(), init] }
        }
    }

    pub(super) fn setup_macos(app: AppHandle) -> Result<(), String> {
        {
            let mut guard = APP_HANDLE.lock().unwrap_or_else(|e| e.into_inner());
            *guard = Some(app);
        }
        let observer = DenVaultLockObserver::new();
        unsafe {
            let workspace = NSWorkspace::sharedWorkspace().notificationCenter();
            workspace.addObserver_selector_name_object(
                &observer,
                sel!(onWillSleep:),
                Some(NSWorkspaceWillSleepNotification),
                None,
            );
            // A display that sleeps usually locks, and a user switch hands
            // the device to someone else.
            for name in [
                NSWorkspaceScreensDidSleepNotification,
                NSWorkspaceSessionDidResignActiveNotification,
            ] {
                workspace.addObserver_selector_name_object(
                    &observer,
                    sel!(onScreenLocked:),
                    Some(name),
                    None,
                );
            }
            NSDistributedNotificationCenter::defaultCenter().addObserver_selector_name_object(
                &observer,
                sel!(onScreenLocked:),
                Some(&NSString::from_str("com.apple.screenIsLocked")),
                None,
            );
        }
        let mut guard = OBSERVER.lock().unwrap_or_else(|e| e.into_inner());
        *guard = Some(observer);
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn lock_reasons_use_the_engine_wire_names() {
        assert_eq!(LockReason::ScreenLocked.wire(), "screen_locked");
        assert_eq!(LockReason::Sleep.wire(), "sleep");
        assert_eq!(LockReason::AppQuit.wire(), "app_quit");
    }
}
