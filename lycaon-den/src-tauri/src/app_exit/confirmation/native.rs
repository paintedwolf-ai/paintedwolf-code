//! Native panels accept the captured initiating window on macOS and Windows.
use super::{Choice, ConfirmationPrompt, DONT_ASK_AGAIN};

pub(super) async fn show(
    parent: &tauri::WebviewWindow,
    prompt: &ConfirmationPrompt,
) -> Result<Choice, String> {
    let buttons = if prompt.allow_dont_ask_again {
        rfd::MessageButtons::YesNoCancelCustom(
            prompt.confirm_label.into(),
            DONT_ASK_AGAIN.into(),
            prompt.cancel_label.into(),
        )
    } else {
        rfd::MessageButtons::OkCancelCustom(
            prompt.confirm_label.into(),
            prompt.cancel_label.into(),
        )
    };
    let result = rfd::AsyncMessageDialog::new()
        .set_parent(parent)
        .set_title(prompt.title)
        .set_description(&prompt.description)
        .set_buttons(buttons)
        .show()
        .await;
    Ok(native_choice(result, prompt))
}

fn native_choice(result: rfd::MessageDialogResult, prompt: &ConfirmationPrompt) -> Choice {
    match result {
        rfd::MessageDialogResult::Custom(label) if label == prompt.confirm_label => {
            Choice::Quit { dont_ask_again: false }
        }
        rfd::MessageDialogResult::Custom(label)
            if prompt.allow_dont_ask_again && label == DONT_ASK_AGAIN =>
        {
            Choice::Quit { dont_ask_again: true }
        }
        rfd::MessageDialogResult::Custom(label)
            if prompt.open_running_label == Some(label.as_str()) =>
        {
            Choice::OpenRunning
        }
        _ => Choice::KeepWorking,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::app_exit::confirmation::{CANCEL, KEEP_WORKING, OPEN_RUNNING, QUIT, TITLE};

    #[test]
    fn native_choice_maps_quit_dont_ask_again_open_running_and_cancel() {
        let prompt = ConfirmationPrompt {
            title: TITLE,
            description: "1 background command is running".into(),
            confirm_label: QUIT,
            cancel_label: CANCEL,
            has_background_commands: true,
            allow_dont_ask_again: true,
            open_running_label: Some(OPEN_RUNNING),
            running_project_id: Some("proj-1".into()),
        };
        assert_eq!(
            native_choice(rfd::MessageDialogResult::Custom(QUIT.into()), &prompt),
            Choice::Quit { dont_ask_again: false }
        );
        assert_eq!(
            native_choice(
                rfd::MessageDialogResult::Custom(DONT_ASK_AGAIN.into()),
                &prompt
            ),
            Choice::Quit { dont_ask_again: true }
        );
        assert_eq!(
            native_choice(
                rfd::MessageDialogResult::Custom(OPEN_RUNNING.into()),
                &prompt
            ),
            Choice::OpenRunning
        );
        assert_eq!(
            native_choice(rfd::MessageDialogResult::Custom(CANCEL.into()), &prompt),
            Choice::KeepWorking
        );
        assert_eq!(
            native_choice(rfd::MessageDialogResult::Cancel, &prompt),
            Choice::KeepWorking
        );

        let foreground_only = ConfirmationPrompt {
            title: TITLE,
            description: "Work is still active.".into(),
            confirm_label: QUIT,
            cancel_label: KEEP_WORKING,
            has_background_commands: false,
            allow_dont_ask_again: false,
            open_running_label: None,
            running_project_id: None,
        };
        assert_eq!(
            native_choice(
                rfd::MessageDialogResult::Custom(DONT_ASK_AGAIN.into()),
                &foreground_only
            ),
            Choice::KeepWorking
        );
    }
}
