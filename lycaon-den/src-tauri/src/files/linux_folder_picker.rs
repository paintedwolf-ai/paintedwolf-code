//! Folder selection stays parented to the requesting desktop window.
use gtk::{gio, glib, prelude::*};
use std::path::PathBuf;
use std::time::Duration;

use super::linux_desktop_open::{request, ParentWindow, ServiceError};

mod fallback;

const SERVICE: &str = "org.freedesktop.portal.FileChooser";
const OBJECT: &str = "/org/freedesktop/portal/desktop";

pub(super) fn pick(window: &tauri::WebviewWindow, title: &str) -> Result<Option<PathBuf>, String> {
    let parent = ParentWindow::of(window);
    let context = glib::MainContext::new();
    let result = context
        .with_thread_default(|| {
            let bus =
                crate::linux_desktop::bus::connect_session(&context, Duration::from_millis(500))
                    .map_err(|_| ServiceError::Unavailable)?;
            portal(&bus, parent.identity(), title)
        })
        .map_err(|error| error.to_string())?;
    match result {
        Ok(path) => Ok(Some(path)),
        Err(ServiceError::Cancelled) => Ok(None),
        Err(ServiceError::Unavailable) => fallback::pick(window, title),
        Err(ServiceError::Failed(reason)) => {
            Err(format!("The desktop folder picker failed: {reason}"))
        }
    }
}

fn portal(bus: &gio::DBusConnection, parent: &str, title: &str) -> Result<PathBuf, ServiceError> {
    let results =
        request::run_with_timeout(bus, Duration::from_secs(30 * 60), |owner, options| {
            let options = glib::VariantDict::new(Some(&options));
            options.insert("directory", true);
            options.insert("multiple", false);
            options.insert("modal", true);
            bus.call_sync(
                Some(owner),
                OBJECT,
                SERVICE,
                "OpenFile",
                Some(&(parent, title, options.end()).to_variant()),
                Some(glib::VariantTy::new("(o)").expect("static portal handle type")),
                gio::DBusCallFlags::NONE,
                5000,
                gio::Cancellable::NONE,
            )
            .map_err(|error| ServiceError::from_call(&error))
        })?;
    selected_path(&results)
}

fn selected_path(results: &glib::Variant) -> Result<PathBuf, ServiceError> {
    let dictionary = glib::VariantDict::new(Some(results));
    let uris = dictionary
        .lookup::<Vec<String>>("uris")
        .map_err(|_| ServiceError::Failed("Invalid folder picker result".into()))?
        .ok_or_else(|| ServiceError::Failed("The folder picker returned no location".into()))?;
    if uris.len() != 1 || uris[0].len() > 64 * 1024 {
        return Err(ServiceError::Failed(
            "The folder picker returned an invalid location".into(),
        ));
    }
    let file = gio::File::for_uri(&uris[0]);
    let path = file
        .path()
        .filter(|path| path.is_absolute() && path.is_dir())
        .ok_or_else(|| ServiceError::Failed("Select an accessible local folder".into()))?;
    if file.uri_scheme().as_deref() != Some("file") {
        return Err(ServiceError::Failed(
            "Select an accessible local folder".into(),
        ));
    }
    Ok(path)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn selection(uris: Vec<String>) -> glib::Variant {
        let dictionary = glib::VariantDict::new(None);
        dictionary.insert("uris", uris);
        dictionary.end()
    }

    #[test]
    fn folder_results_preserve_unicode_and_refuse_remote_missing_or_multiple_locations() {
        let root = crate::test_support::TempDir::new("folder-picker-uri");
        let folder = root.join("a é folder");
        std::fs::create_dir(&folder).unwrap();
        let uri = gio::File::for_path(&folder).uri().to_string();
        assert_eq!(
            selected_path(&selection(vec![uri.clone()])).unwrap(),
            folder
        );
        for uris in [
            vec![],
            vec![uri.clone(), uri],
            vec!["smb://host/share".into()],
            vec!["file:///nonexistent-paintedwolf-picker".into()],
        ] {
            assert!(selected_path(&selection(uris)).is_err());
        }
        assert!(selected_path(&glib::VariantDict::new(None).end()).is_err());
    }
}
