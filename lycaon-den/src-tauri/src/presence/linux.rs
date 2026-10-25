//! Linux user presence. polkit confirms the person through the desktop's
//! authentication agent for a packaged action; the shell is the subject, so
//! the agent's prompt belongs to this process and no webview. Without an
//! agent, and only while the vault is on the app-password path, a native
//! GTK dialog re-verifies the app password the shell unlocked the vault with.

use std::collections::HashMap;
use std::sync::mpsc;
use std::time::Duration;

use gtk::prelude::*;
use gtk::{gio, glib};
use tauri::Manager;

use super::PresenceCommandError;
use crate::linux_desktop::bus as linux_bus;
use crate::sidecar::SidecarState;

pub(super) const AUTHENTICATOR_POLKIT: &str = "linux_user_presence";
pub(super) const AUTHENTICATOR_APP_PASSWORD: &str = "linux_app_password";

/// The packaged polkit action for an application identifier; its defaults
/// are `auth_self`.
pub(super) fn action_id(identifier: &str) -> String {
    format!("{identifier}.confirm-presence")
}
const AUTHORITY_NAME: &str = "org.freedesktop.PolicyKit1";
const AUTHORITY_PATH: &str = "/org/freedesktop/PolicyKit1/Authority";
const AUTHORITY_INTERFACE: &str = "org.freedesktop.PolicyKit1.Authority";
const BUS_NAME: &str = "org.freedesktop.DBus";
const BUS_PATH: &str = "/org/freedesktop/DBus";
const NO_USER_INTERACTION: u32 = 0;
const ALLOW_USER_INTERACTION: u32 = 1;
const PROMPT_TIMEOUT: Duration = Duration::from_secs(120);
const BUS_CALL_TIMEOUT: Duration = Duration::from_secs(10);
const BUS_CONNECT_TIMEOUT: Duration = Duration::from_millis(1500);

/// Why polkit did not confirm the person.
#[derive(Debug, PartialEq, Eq)]
pub(super) enum PolkitRefusal {
    /// Nothing on this host can run the check: no system bus, no authority,
    /// no installed action, or no agent in this session.
    Unavailable(String),
    Denied(String),
    Failed(String),
}

/// Maps the authority's answer. With user interaction allowed, an unmet
/// challenge means no agent in this session could ask.
pub(super) fn outcome(is_authorized: bool, is_challenge: bool) -> Result<(), PolkitRefusal> {
    match (is_authorized, is_challenge) {
        (true, _) => Ok(()),
        (false, true) => Err(PolkitRefusal::Unavailable(
            "No authentication agent is running in this desktop session.".into(),
        )),
        (false, false) => Err(PolkitRefusal::Denied(
            "Authentication was canceled or did not succeed.".into(),
        )),
    }
}

/// Maps the authority's answer to a check that may not interact. Presence needs
/// a challenge the person answers: a host policy that grants the action without
/// one, or retains an earlier answer, cannot witness that someone is here now.
pub(super) fn preflight(is_authorized: bool, is_challenge: bool) -> Result<(), PolkitRefusal> {
    match (is_authorized, is_challenge) {
        (true, _) => Err(PolkitRefusal::Unavailable(
            "This computer's polkit policy grants the presence check without asking, so it cannot confirm you are here.".into(),
        )),
        (false, true) => Ok(()),
        (false, false) => Err(PolkitRefusal::Denied(
            "This computer's polkit policy refuses the presence check.".into(),
        )),
    }
}

/// The authority may be owned by root or by polkit's own service user.
pub(super) fn authority_owner_accepted(owner_uid: u32, polkitd_uid: Option<u32>) -> bool {
    owner_uid == 0 || Some(owner_uid) == polkitd_uid
}

/// Whether an `EnumerateActions` reply lists the action.
pub(super) fn lists_action(actions: &glib::Variant, action_id: &str) -> bool {
    (0..actions.n_children()).any(|index| {
        let action = actions.child_value(index);
        action.n_children() > 0 && action.child_value(0).str() == Some(action_id)
    })
}

/// Reads one `(bba{ss})` authorization result.
pub(super) fn authorization_flags(reply: &glib::Variant) -> Option<(bool, bool)> {
    let result = reply.child_value(0);
    if result.n_children() < 2 {
        return None;
    }
    Some((
        result.child_value(0).get::<bool>()?,
        result.child_value(1).get::<bool>()?,
    ))
}

pub(super) fn confirm_with_polkit(identifier: &str) -> Result<(), PolkitRefusal> {
    let action = action_id(identifier);
    let context = glib::MainContext::new();
    context
        .with_thread_default(|| confirm_on(&context, &action))
        .map_err(|_| PolkitRefusal::Failed("the bus context is in use".into()))?
}

fn confirm_on(context: &glib::MainContext, action: &str) -> Result<(), PolkitRefusal> {
    let bus = linux_bus::connect(context, linux_bus::SYSTEM_BUS_ADDRESS, BUS_CONNECT_TIMEOUT)
        .map_err(|error| {
            PolkitRefusal::Unavailable(format!("The system bus is unavailable: {error}"))
        })?;
    let owner = authority_owner(context, &bus, action)?;
    let subject = own_subject()?;
    let probe_id = uuid::Uuid::new_v4().to_string();
    let probe = linux_bus::call(
        context,
        &bus,
        Some(&owner),
        AUTHORITY_PATH,
        AUTHORITY_INTERFACE,
        "CheckAuthorization",
        Some(&check_parameters(
            subject.clone(),
            action,
            NO_USER_INTERACTION,
            &probe_id,
        )),
        BUS_CALL_TIMEOUT,
    )
    .map_err(|error| PolkitRefusal::Failed(format!("The authority refused the check: {error}")))?;
    let (authorized, challenge) = authorization_flags(&probe).ok_or_else(|| {
        PolkitRefusal::Failed("the authority answered in an unknown shape".into())
    })?;
    preflight(authorized, challenge)?;
    let cancellation_id = uuid::Uuid::new_v4().to_string();
    let parameters = check_parameters(subject, action, ALLOW_USER_INTERACTION, &cancellation_id);
    let reply = linux_bus::call(
        context,
        &bus,
        Some(&owner),
        AUTHORITY_PATH,
        AUTHORITY_INTERFACE,
        "CheckAuthorization",
        Some(&parameters),
        PROMPT_TIMEOUT + Duration::from_secs(5),
    );
    match reply {
        Ok(reply) => {
            let (authorized, challenge) = authorization_flags(&reply).ok_or_else(|| {
                PolkitRefusal::Failed("the authority answered in an unknown shape".into())
            })?;
            outcome(authorized, challenge)
        }
        Err(error) if error.matches(gio::IOErrorEnum::TimedOut) => {
            let _ = linux_bus::call(
                context,
                &bus,
                Some(&owner),
                AUTHORITY_PATH,
                AUTHORITY_INTERFACE,
                "CancelCheckAuthorization",
                Some(&(cancellation_id,).to_variant()),
                BUS_CALL_TIMEOUT,
            );
            Err(PolkitRefusal::Denied(
                "Authentication did not finish in time.".into(),
            ))
        }
        Err(error) => Err(PolkitRefusal::Failed(format!(
            "The authority refused the check: {error}"
        ))),
    }
}

/// `CheckAuthorization` arguments. polkit refuses details from a caller that
/// is neither root nor the action's owner, so the agent shows the packaged
/// action's own message.
fn check_parameters(
    subject: HashMap<String, glib::Variant>,
    action: &str,
    flags: u32,
    cancellation_id: &str,
) -> glib::Variant {
    (
        ("unix-process".to_string(), subject),
        action.to_string(),
        HashMap::<String, String>::new(),
        flags,
        cancellation_id.to_string(),
    )
        .to_variant()
}

/// Resolves the authority's unique connection name, confirms the system owns
/// it, and confirms the packaged action is registered. Later calls address
/// the unique name, so a name that changes hands mid-flight is not followed.
fn authority_owner(
    context: &glib::MainContext,
    bus: &gio::DBusConnection,
    action: &str,
) -> Result<String, PolkitRefusal> {
    let owner = linux_bus::call(
        context,
        bus,
        Some(BUS_NAME),
        BUS_PATH,
        BUS_NAME,
        "GetNameOwner",
        Some(&(AUTHORITY_NAME.to_string(),).to_variant()),
        BUS_CALL_TIMEOUT,
    )
    .map_err(|_| PolkitRefusal::Unavailable("The polkit authority is not running.".into()))?
    .child_value(0)
    .str()
    .map(str::to_owned)
    .ok_or_else(|| PolkitRefusal::Failed("the bus named no authority owner".into()))?;
    let owner_uid = linux_bus::call(
        context,
        bus,
        Some(BUS_NAME),
        BUS_PATH,
        BUS_NAME,
        "GetConnectionUnixUser",
        Some(&(owner.clone(),).to_variant()),
        BUS_CALL_TIMEOUT,
    )
    .ok()
    .and_then(|reply| reply.child_value(0).get::<u32>())
    .ok_or_else(|| PolkitRefusal::Failed("the authority owner's user is unknown".into()))?;
    if !authority_owner_accepted(owner_uid, user_id("polkitd")) {
        return Err(PolkitRefusal::Unavailable(
            "The polkit authority is not owned by the system.".into(),
        ));
    }
    let actions = linux_bus::call(
        context,
        bus,
        Some(&owner),
        AUTHORITY_PATH,
        AUTHORITY_INTERFACE,
        "EnumerateActions",
        Some(&(String::new(),).to_variant()),
        BUS_CALL_TIMEOUT,
    )
    .map_err(|error| {
        PolkitRefusal::Failed(format!("The authority could not list its actions: {error}"))
    })?;
    if !lists_action(&actions.child_value(0), action) {
        return Err(PolkitRefusal::Unavailable(
            "The Painted Wolf Code presence action is not installed.".into(),
        ));
    }
    Ok(owner)
}

/// This process as a polkit `unix-process` subject.
fn own_subject() -> Result<HashMap<String, glib::Variant>, PolkitRefusal> {
    let start = linux_bus::own_start_time().map_err(PolkitRefusal::Failed)?;
    let mut subject = HashMap::new();
    subject.insert("pid".to_string(), std::process::id().to_variant());
    subject.insert("start-time".to_string(), start.to_variant());
    // SAFETY: getuid has no preconditions and cannot fail.
    subject.insert(
        "uid".to_string(),
        (unsafe { libc::getuid() } as i32).to_variant(),
    );
    Ok(subject)
}

/// The uid of a named account, from the system user database.
fn user_id(name: &str) -> Option<u32> {
    let name = std::ffi::CString::new(name).ok()?;
    let mut entry: libc::passwd = unsafe { std::mem::zeroed() };
    let mut buffer = vec![0u8; 4096];
    let mut result: *mut libc::passwd = std::ptr::null_mut();
    // SAFETY: every pointer names a live buffer of the stated size; the
    // reentrant lookup writes only within them.
    let status = unsafe {
        libc::getpwnam_r(
            name.as_ptr(),
            &mut entry,
            buffer.as_mut_ptr().cast(),
            buffer.len(),
            &mut result,
        )
    };
    if status != 0 || result.is_null() {
        return None;
    }
    Some(entry.pw_uid)
}

/// Re-verifies the app password in a native dialog. Only a shell that
/// unlocked the vault with that password can verify it, so the dialog is
/// unavailable on the identity-service path.
pub(super) fn confirm_app_password(
    window: &tauri::Window,
    prompt: &str,
    polkit_reason: &str,
) -> Result<(), PresenceCommandError> {
    let state = window.app_handle().state::<SidecarState>();
    if !state.holds_vault_password() {
        return Err(PresenceCommandError::unavailable(format!(
            "{polkit_reason} The vault is not on the app-password path, so there is no password to re-enter."
        ))
        .with_suggested_action(
            "Install a polkit authentication agent for this desktop, then try again.",
        ));
    }
    let (tx, rx) = mpsc::channel();
    let owner = window.clone();
    let prompt = prompt.to_string();
    window
        .run_on_main_thread(move || {
            // GTK objects stay on the main thread; the window handle crosses.
            let parent = owner.gtk_window().ok();
            let _ = tx.send(ask_password(parent.as_ref(), &prompt));
        })
        .map_err(|error| {
            PresenceCommandError::failed(format!("the password dialog could not open: {error}"))
        })?;
    let entered = rx
        .recv_timeout(PROMPT_TIMEOUT)
        .map_err(|_| PresenceCommandError::denied("Authentication did not finish in time."))?;
    let Some(mut entered) = entered else {
        return Err(PresenceCommandError::denied(
            "The password dialog was canceled.",
        ));
    };
    let verified = state.verify_vault_password(&entered);
    entered.fill(0);
    match verified {
        Some(true) => Ok(()),
        Some(false) => Err(PresenceCommandError::denied(
            "The app password was incorrect.",
        )),
        None => Err(PresenceCommandError::unavailable(
            "The vault is no longer on the app-password path.",
        )),
    }
}

/// Shows the modal password dialog on the main thread and returns the
/// entered password, or None when dismissed.
fn ask_password(parent: Option<&gtk::ApplicationWindow>, prompt: &str) -> Option<Vec<u8>> {
    let dialog = gtk::Dialog::with_buttons(
        Some("Confirm it's you"),
        parent,
        gtk::DialogFlags::MODAL | gtk::DialogFlags::DESTROY_WITH_PARENT,
        &[
            ("Cancel", gtk::ResponseType::Cancel),
            ("Confirm", gtk::ResponseType::Ok),
        ],
    );
    dialog.set_default_response(gtk::ResponseType::Ok);
    let content = dialog.content_area();
    content.set_margin_top(16);
    content.set_margin_bottom(8);
    content.set_margin_start(16);
    content.set_margin_end(16);
    content.set_spacing(8);
    let reason = gtk::Label::new(Some(prompt));
    reason.set_line_wrap(true);
    reason.set_max_width_chars(60);
    reason.set_xalign(0.0);
    let instruction = gtk::Label::new(Some(
        "Enter your Painted Wolf Code app password to continue.",
    ));
    instruction.set_xalign(0.0);
    let entry = gtk::Entry::new();
    entry.set_visibility(false);
    entry.set_input_purpose(gtk::InputPurpose::Password);
    entry.set_activates_default(true);
    content.pack_start(&reason, false, false, 0);
    content.pack_start(&instruction, false, false, 0);
    content.pack_start(&entry, false, false, 0);
    dialog.show_all();
    let response = dialog.run();
    let text = entry.text();
    let value =
        (response == gtk::ResponseType::Ok && !text.is_empty()).then(|| text.as_bytes().to_vec());
    entry.set_text("");
    dialog.close();
    value
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn polkit_outcomes_map_to_presence_results() {
        assert_eq!(outcome(true, false), Ok(()));
        assert_eq!(outcome(true, true), Ok(()));
        assert!(matches!(
            outcome(false, true),
            Err(PolkitRefusal::Unavailable(_))
        ));
        assert!(matches!(
            outcome(false, false),
            Err(PolkitRefusal::Denied(_))
        ));
    }

    #[test]
    fn only_a_challenged_policy_can_witness_presence() {
        assert!(matches!(
            preflight(true, false),
            Err(PolkitRefusal::Unavailable(_))
        ));
        assert!(matches!(
            preflight(true, true),
            Err(PolkitRefusal::Unavailable(_))
        ));
        assert_eq!(preflight(false, true), Ok(()));
        assert!(matches!(
            preflight(false, false),
            Err(PolkitRefusal::Denied(_))
        ));
    }

    #[test]
    fn the_authority_must_be_owned_by_the_system() {
        assert!(authority_owner_accepted(0, None));
        assert!(authority_owner_accepted(998, Some(998)));
        assert!(!authority_owner_accepted(1000, Some(998)));
        assert!(!authority_owner_accepted(1000, None));
    }

    #[test]
    fn the_action_listing_is_searched_by_id() {
        let action = |id: &str| {
            let empty: HashMap<String, String> = HashMap::new();
            (
                id.to_string(),
                String::new(),
                String::new(),
                String::new(),
                String::new(),
                String::new(),
                0u32,
                0u32,
                0u32,
                empty,
            )
                .to_variant()
        };
        let listing = glib::Variant::array_from_iter_with_type(
            glib::VariantTy::new("(ssssssuuua{ss})").unwrap(),
            [
                action("org.example.other"),
                action("dev.paintedwolf.code.confirm-presence"),
            ],
        );
        assert!(lists_action(
            &listing,
            "dev.paintedwolf.code.confirm-presence"
        ));
        assert!(!lists_action(&listing, "org.example.absent"));
        let empty = glib::Variant::array_from_iter_with_type(
            glib::VariantTy::new("(ssssssuuua{ss})").unwrap(),
            std::iter::empty::<glib::Variant>(),
        );
        assert!(!lists_action(
            &empty,
            "dev.paintedwolf.code.confirm-presence"
        ));
    }

    #[test]
    fn the_packaged_action_is_named_for_the_app_identifier() {
        let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR"));
        let conf: serde_json::Value =
            serde_json::from_str(&std::fs::read_to_string(root.join("tauri.conf.json")).unwrap())
                .unwrap();
        let identifier = conf["identifier"].as_str().unwrap();
        let policy_path = root.join(format!("../../packaging/linux/polkit/{identifier}.policy"));
        let policy = std::fs::read_to_string(&policy_path).unwrap();
        assert!(policy.contains(&format!(r#"<action id="{}">"#, action_id(identifier))));
        // The shell passes no details, so nothing in the message can be substituted.
        assert!(!policy.contains("$("));
    }

    #[test]
    fn the_check_carries_no_details() {
        let mut subject = HashMap::new();
        subject.insert("pid".to_string(), 42u32.to_variant());
        let parameters = check_parameters(
            subject.clone(),
            "dev.paintedwolf.code.confirm-presence",
            ALLOW_USER_INTERACTION,
            "c1",
        );
        assert!(parameters.is_type(glib::VariantTy::new("((sa{sv})sa{ss}us)").unwrap()));
        assert_eq!(parameters.child_value(2).n_children(), 0);
        assert_eq!(
            parameters.child_value(3).get::<u32>(),
            Some(ALLOW_USER_INTERACTION)
        );
        assert_eq!(parameters.child_value(4).str(), Some("c1"));
        let probe = check_parameters(
            subject,
            "dev.paintedwolf.code.confirm-presence",
            NO_USER_INTERACTION,
            "c2",
        );
        assert_eq!(probe.child_value(3).get::<u32>(), Some(NO_USER_INTERACTION));
    }

    #[test]
    fn authorization_results_are_read_from_the_reply_tuple() {
        let details: HashMap<String, String> = HashMap::new();
        let reply = ((true, false, details.clone()),).to_variant();
        assert_eq!(authorization_flags(&reply), Some((true, false)));
        let reply = ((false, true, details),).to_variant();
        assert_eq!(authorization_flags(&reply), Some((false, true)));
        assert_eq!(authorization_flags(&(("x",),).to_variant()), None);
    }

    #[test]
    fn root_is_a_known_user_and_nonsense_is_not() {
        assert_eq!(user_id("root"), Some(0));
        assert_eq!(user_id("no-such-user-painted-wolf"), None);
    }
}
