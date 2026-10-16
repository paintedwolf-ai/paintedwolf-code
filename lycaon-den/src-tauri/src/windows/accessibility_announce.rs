//! Screen reader announcements the web view's live regions cannot deliver.
//!
//! WebKitGTK emits no text-change events for live-region mutations, and Orca
//! presents live regions only from those events, so on Linux the host speaks
//! the announcement as an ATK notification on the window.

use serde::Deserialize;
use tauri::WebviewWindow;

/// ARIA politeness of the region the announcement came from.
#[derive(Clone, Copy, Debug, Default, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum Politeness {
    #[default]
    Polite,
    Assertive,
}

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
pub fn den_announce(
    window: WebviewWindow,
    text: String,
    politeness: Option<Politeness>,
) -> Result<(), String> {
    let Some(text) = announcement_text(&text) else {
        return Ok(());
    };
    let politeness = politeness.unwrap_or_default();
    #[cfg(target_os = "linux")]
    {
        linux::announce(window, text, politeness)
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = (window, text, politeness);
        Ok(())
    }
}

#[cfg(target_os = "linux")]
mod linux {
    use gtk::glib::prelude::*;
    use gtk::glib::subclass::signal::SignalId;
    use gtk::prelude::*;
    use tauri::WebviewWindow;

    use super::Politeness;

    /// `AtkLive` value for the politeness.
    fn atk_live(politeness: Politeness) -> i32 {
        match politeness {
            Politeness::Polite => 1,
            Politeness::Assertive => 2,
        }
    }

    pub(super) fn announce(
        window: WebviewWindow,
        text: String,
        politeness: Politeness,
    ) -> Result<(), String> {
        let live = atk_live(politeness);
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
                accessible.emit_by_name::<()>("notification", &[&text, &live]);
            })
            .map_err(|err| err.to_string())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn politeness_reads_the_aria_values_and_defaults_to_polite() {
        let polite: Politeness = serde_json::from_str("\"polite\"").expect("polite");
        let assertive: Politeness = serde_json::from_str("\"assertive\"").expect("assertive");
        assert_eq!(polite, Politeness::Polite);
        assert_eq!(assertive, Politeness::Assertive);
        assert_eq!(Politeness::default(), Politeness::Polite);
        assert!(serde_json::from_str::<Politeness>("\"off\"").is_err());
    }

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
