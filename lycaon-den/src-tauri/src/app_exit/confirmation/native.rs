//! Native panels accept the captured initiating window on macOS and Windows.
use super::{Choice, KEEP_WORKING, QUIT, TITLE};

pub(super) async fn show(parent: &tauri::WebviewWindow, description: &str) -> Result<Choice, String> {
    let result = rfd::AsyncMessageDialog::new().set_parent(parent).set_title(TITLE)
        .set_description(description)
        .set_buttons(rfd::MessageButtons::OkCancelCustom(QUIT.into(), KEEP_WORKING.into()))
        .show().await;
    Ok(if result == rfd::MessageDialogResult::Custom(QUIT.into()) {
        Choice::Quit
    } else { Choice::KeepWorking })
}
