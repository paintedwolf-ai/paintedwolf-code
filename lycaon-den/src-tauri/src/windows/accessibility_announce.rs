//! Screen reader announcements the web view's live regions cannot deliver.
//!
//! WebKitGTK emits no text-change events for live-region mutations, and Orca
//! presents live regions only from those events, so on Linux the host speaks
//! the announcement as an ATK notification on the window.

use tauri::WebviewWindow;

/// Longest announcement forwarded; a reply longer than this is cut at a char boundary.
const MAX_ANNOUNCEMENT_BYTES: usize = 16 << 10;

/// The text to speak, or `None` when there is nothing to say.
fn announcement_text(text: &str) -> Option<String> {
    let text = text.trim();
    if text.is_empty() {
        return None;
    }
    let mut end = text.len().min(MAX_ANNOUNCEMENT_BYTES);
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    Some(text[..end].to_string())
}

#[tauri::command]
pub fn den_announce(window: WebviewWindow, text: String) -> Result<(), String> {
    let Some(text) = announcement_text(&text) else {
        return Ok(());
    };
    #[cfg(target_os = "linux")]
    {
        linux::announce(window, text)
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = (window, text);
        Ok(())
    }
}

#[cfg(target_os = "linux")]
mod linux {
    use gtk::glib::prelude::*;
    use gtk::glib::subclass::signal::SignalId;
    use gtk::prelude::*;
    use tauri::WebviewWindow;

    /// `ATK_LIVE_POLITE`.
    const LIVE_POLITE: i32 = 1;

    pub(super) fn announce(window: WebviewWindow, text: String) -> Result<(), String> {
        let target = window.clone();
        window
            .run_on_main_thread(move || {
                let Some(accessible) = target.gtk_window().ok().and_then(|w| w.accessible()) else {
                    return;
                };
                // ATK before 2.50 has no notification signal.
                if SignalId::lookup("notification", accessible.type_()).is_none() {
                    return;
                }
                accessible.emit_by_name::<()>("notification", &[&text, &LIVE_POLITE]);
            })
            .map_err(|err| err.to_string())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn blank_text_is_not_announced() {
        assert_eq!(announcement_text(""), None);
        assert_eq!(announcement_text(" \n\t"), None);
    }

    #[test]
    fn text_is_trimmed() {
        assert_eq!(
            announcement_text("  Assistant: done \n").as_deref(),
            Some("Assistant: done")
        );
    }

    #[test]
    fn long_text_is_cut_at_a_char_boundary() {
        let long = "é".repeat(MAX_ANNOUNCEMENT_BYTES);
        let cut = announcement_text(&long).expect("text");
        assert!(cut.len() <= MAX_ANNOUNCEMENT_BYTES);
        assert!(cut.chars().all(|c| c == 'é'));
    }
}
