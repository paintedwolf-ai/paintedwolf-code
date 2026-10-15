//! Whether a screen reader is running.
//!
//! WebKitGTK aborts its web process answering a screen reader's text-attribute
//! queries over text that cannot be selected, so Den makes text selectable
//! while one runs. On Linux the desktop settings watch follows AT-SPI's
//! `org.a11y.Status` `ScreenReaderEnabled`; other platforms report none.

use serde::Serialize;

pub const SCREEN_READER_CHANGED_EVENT: &str = "accessibility://screen-reader-changed";

/// Screen reader fact; `revision` orders the boot read against later changes.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ScreenReaderPayload {
    pub active: bool,
    pub revision: u64,
}

#[tauri::command]
pub fn accessibility_screen_reader() -> ScreenReaderPayload {
    #[cfg(target_os = "linux")]
    {
        super::linux_desktop_settings::portal::current_screen_reader_payload()
    }
    #[cfg(not(target_os = "linux"))]
    {
        ScreenReaderPayload::default()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_payload_serializes_in_camel_case() {
        let json = serde_json::to_string(&ScreenReaderPayload { active: true, revision: 4 }).unwrap();
        assert_eq!(json, r#"{"active":true,"revision":4}"#);
    }
}
