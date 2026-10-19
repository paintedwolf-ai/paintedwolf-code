//! Exact-document grants for an explicitly selected external Flatpak editor.

use std::collections::HashMap;
use std::os::unix::{ffi::OsStringExt, fs::OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::time::Duration;

use glib::variant::Handle;
use gtk::{gio, glib, prelude::*};

const SERVICE: &str = "org.freedesktop.portal.Documents";
const OBJECT: &str = "/org/freedesktop/portal/documents";
const TIMEOUT_MS: i32 = 5_000;

/// Unique, nonpersistent exports live through the editor's desktop session.
/// A launch failure removes the unused grant; a successful launch retains it.
pub(crate) struct Export {
    pub(crate) path: PathBuf,
    bus: gio::DBusConnection,
    owner: String,
    id: String,
    retained: bool,
}

impl Export {
    pub(crate) fn retain(mut self) {
        self.retained = true;
    }
}

impl Drop for Export {
    fn drop(&mut self) {
        if !self.retained {
            let _ = self.bus.call_sync(
                Some(&self.owner),
                OBJECT,
                SERVICE,
                "Delete",
                Some(&(self.id.as_str(),).to_variant()),
                None,
                gio::DBusCallFlags::NONE,
                500,
                gio::Cancellable::NONE,
            );
        }
    }
}

pub(crate) fn export(path: &Path, app: &str) -> Result<Export, String> {
    let context = glib::MainContext::new();
    let bus = context
        .with_thread_default(|| {
            crate::linux_desktop::bus::connect_session(&context, Duration::from_millis(500))
        })
        .map_err(|error| error.to_string())??;
    export_on(&bus, path, app)
}

fn export_on(bus: &gio::DBusConnection, path: &Path, app: &str) -> Result<Export, String> {
    let file = std::fs::OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_PATH | libc::O_CLOEXEC)
        .open(path)
        .map_err(|error| format!("The editor file is unavailable: {error}"))?;
    let flags = if file.metadata().map_err(|error| error.to_string())?.is_dir() {
        8u32
    } else {
        0u32
    };
    let files = gio::UnixFDList::new();
    let index = files.append(file).map_err(|error| error.to_string())?;
    let message =
        gio::DBusMessage::new_method_call(Some(SERVICE), OBJECT, Some(SERVICE), "AddFull");
    message.set_body(&(vec![Handle(index)], flags, app, vec!["read", "write"]).to_variant());
    message.set_unix_fd_list(Some(&files));
    let (reply, _) = bus
        .send_message_with_reply_sync(
            &message,
            gio::DBusSendMessageFlags::NONE,
            TIMEOUT_MS,
            gio::Cancellable::NONE,
        )
        .map_err(|error| format!("The document portal is unavailable: {error}"))?;
    if reply.message_type() == gio::DBusMessageType::Error {
        return Err(format!(
            "The document portal refused editor access: {}",
            reply.error_name().as_deref().unwrap_or("unknown error")
        ));
    }
    let owner = reply
        .sender()
        .ok_or("The document portal reply has no owner")?
        .to_string();
    let (ids, extra) = reply
        .body()
        .and_then(|body| body.get::<(Vec<String>, HashMap<String, glib::Variant>)>())
        .ok_or("Invalid document portal export response")?;
    let id = export_id(&ids)?;
    let mut export = Export {
        path: PathBuf::new(),
        bus: bus.clone(),
        owner,
        id,
        retained: false,
    };
    let mount = if let Some(mount) = extra
        .get("mountpoint")
        .and_then(|value| value.get::<Vec<u8>>())
    {
        mount
    } else {
        let reply = bus
            .call_sync(
                Some(&export.owner),
                OBJECT,
                SERVICE,
                "GetMountPoint",
                None,
                None,
                gio::DBusCallFlags::NONE,
                TIMEOUT_MS,
                gio::Cancellable::NONE,
            )
            .map_err(|error| error.to_string())?;
        reply
            .get::<(Vec<u8>,)>()
            .ok_or("Invalid document portal mount point")?
            .0
    };
    export.path = exported_path(mount, &export.id, path)?;
    Ok(export)
}

fn export_id(ids: &[String]) -> Result<String, String> {
    let [id] = ids else {
        return Err("Invalid document portal export count".into());
    };
    if id.is_empty() || id.len() > 128 || !id.bytes().all(|byte| byte.is_ascii_alphanumeric()) {
        return Err("Invalid document portal identifier".into());
    }
    Ok(id.clone())
}

fn exported_path(mut mount: Vec<u8>, id: &str, original: &Path) -> Result<PathBuf, String> {
    if mount.last() == Some(&0) {
        mount.pop();
    }
    if mount.is_empty() || mount.len() > 4096 || mount.contains(&0) {
        return Err("Invalid document portal mount point".into());
    }
    let mount = PathBuf::from(std::ffi::OsString::from_vec(mount));
    if !mount.is_absolute() {
        return Err("The document portal mount point is not absolute".into());
    }
    let name = original.file_name().ok_or("The editor file has no name")?;
    Ok(mount.join(id).join(name))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn exported_paths_preserve_unicode_and_spaces_without_navigation_suffixes() {
        assert_eq!(
            exported_path(
                b"/run/user/1000/doc\0".to_vec(),
                "abc123",
                Path::new("/outside/a é.txt")
            )
            .unwrap(),
            Path::new("/run/user/1000/doc/abc123/a é.txt")
        );
        for mount in [b"relative".to_vec(), b"/a\0b".to_vec(), vec![]] {
            assert!(exported_path(mount, "abc123", Path::new("/a")).is_err());
        }
        assert!(exported_path(b"/doc".to_vec(), "abc123", Path::new("/")).is_err());
        for ids in [
            vec![],
            vec!["../escape".into()],
            vec!["one".into(), "two".into()],
            vec!["".into()],
        ] {
            assert!(export_id(&ids).is_err());
        }
    }
}

#[cfg(test)]
mod transport_tests;
