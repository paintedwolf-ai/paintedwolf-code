//! rfd's GTK backend ignores parent handles; GTK owns this transient relationship.
use gtk::prelude::*;
use super::{Choice, ConfirmationPrompt, DONT_ASK_AGAIN};

const RESPONSE_OPEN_RUNNING: u16 = 1;

pub(super) async fn show(
    parent: &tauri::WebviewWindow,
    prompt: &ConfirmationPrompt,
) -> Result<Choice, String> {
    let (send, receive) = tokio::sync::oneshot::channel();
    let owner = parent.clone();
    let prompt = prompt.clone();
    parent.run_on_main_thread(move || {
        let result = owner.gtk_window().map_err(|error| error.to_string()).and_then(|parent| {
            let dialog = gtk::MessageDialog::new(Some(&parent),
                gtk::DialogFlags::MODAL | gtk::DialogFlags::DESTROY_WITH_PARENT,
                gtk::MessageType::Info, gtk::ButtonsType::None, prompt.title);
            dialog.set_title(prompt.title);
            dialog.set_secondary_text(Some(&prompt.description));
            dialog.add_button(prompt.confirm_label, gtk::ResponseType::Ok);
            if let Some(open_label) = prompt.open_running_label {
                dialog.add_button(open_label, gtk::ResponseType::Other(RESPONSE_OPEN_RUNNING));
            }
            let keep_working = dialog.add_button(prompt.cancel_label, gtk::ResponseType::Cancel);
            dialog.set_default_response(gtk::ResponseType::Cancel);
            keep_working.grab_focus();
            let dont_ask_check = prompt.allow_dont_ask_again.then(|| {
                let check = gtk::CheckButton::with_label(DONT_ASK_AGAIN);
                check.show();
                dialog.content_area().pack_end(&check, false, false, 4);
                check
            });
            let (Some(accessible), Some(parent_accessible)) = (dialog.accessible(), parent.accessible()) else {
                dialog.close();
                return Err("Exit confirmation could not bind its accessible modal parent.".to_owned());
            };
            accessible.set_role(gtk::atk::Role::Dialog);
            accessible.set_name(prompt.title);
            if !accessible.add_relationship(gtk::atk::RelationType::SubwindowOf, &parent_accessible) {
                dialog.close();
                return Err("Exit confirmation could not record its accessible parent relationship.".to_owned());
            }
            let response = dialog.run();
            let dont_ask_again = dont_ask_check.as_ref().is_some_and(|c| c.is_active());
            dialog.close();
            Ok(choice_with_options(response, dont_ask_again, prompt.allow_dont_ask_again))
        });
        let _ = send.send(result);
    }).map_err(|error| error.to_string())?;
    receive.await.map_err(|_| "Exit confirmation closed before returning its choice.".to_owned())?
}

fn choice(response: gtk::ResponseType) -> Choice {
    choice_with_options(response, false, false)
}

fn choice_with_options(
    response: gtk::ResponseType,
    dont_ask_again: bool,
    allow_dont_ask_again: bool,
) -> Choice {
    if response == gtk::ResponseType::Ok {
        Choice::Quit {
            dont_ask_again: allow_dont_ask_again && dont_ask_again,
        }
    } else if response == gtk::ResponseType::Other(RESPONSE_OPEN_RUNNING) {
        Choice::OpenRunning
    } else {
        Choice::KeepWorking
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_the_explicit_quit_response_approves() {
        assert_eq!(
            choice(gtk::ResponseType::Ok),
            Choice::Quit { dont_ask_again: false }
        );
        assert_eq!(
            choice_with_options(gtk::ResponseType::Ok, true, true),
            Choice::Quit { dont_ask_again: true }
        );
        assert_eq!(
            choice_with_options(gtk::ResponseType::Ok, true, false),
            Choice::Quit { dont_ask_again: false }
        );
        assert_eq!(
            choice_with_options(gtk::ResponseType::Other(RESPONSE_OPEN_RUNNING), false, true),
            Choice::OpenRunning
        );
        for response in [gtk::ResponseType::None, gtk::ResponseType::DeleteEvent,
                         gtk::ResponseType::Cancel, gtk::ResponseType::Close,
                         gtk::ResponseType::Accept, gtk::ResponseType::Yes] {
            assert_eq!(choice(response), Choice::KeepWorking);
        }
    }
}
