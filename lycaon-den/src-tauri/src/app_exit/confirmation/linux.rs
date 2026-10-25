//! rfd's GTK backend ignores parent handles; GTK owns this transient relationship.
use gtk::prelude::*;
use super::{Choice, KEEP_WORKING, QUIT, TITLE};

pub(super) async fn show(parent: &tauri::WebviewWindow, description: &str) -> Result<Choice, String> {
    let (send, receive) = tokio::sync::oneshot::channel();
    let owner = parent.clone();
    let description = description.to_owned();
    parent.run_on_main_thread(move || {
        let result = owner.gtk_window().map_err(|error| error.to_string()).and_then(|parent| {
            let dialog = gtk::MessageDialog::new(Some(&parent),
                gtk::DialogFlags::MODAL | gtk::DialogFlags::DESTROY_WITH_PARENT,
                gtk::MessageType::Info, gtk::ButtonsType::None, TITLE);
            dialog.set_title(TITLE);
            dialog.set_secondary_text(Some(&description));
            dialog.add_button(QUIT, gtk::ResponseType::Ok);
            let keep_working = dialog.add_button(KEEP_WORKING, gtk::ResponseType::Cancel);
            dialog.set_default_response(gtk::ResponseType::Cancel);
            keep_working.grab_focus();
            let (Some(accessible), Some(parent_accessible)) = (dialog.accessible(), parent.accessible()) else {
                dialog.close();
                return Err("Exit confirmation could not bind its accessible modal parent.".to_owned());
            };
            accessible.set_role(gtk::atk::Role::Dialog);
            accessible.set_name(TITLE);
            if !accessible.add_relationship(gtk::atk::RelationType::SubwindowOf, &parent_accessible) {
                dialog.close();
                return Err("Exit confirmation could not record its accessible parent relationship.".to_owned());
            }
            let response = dialog.run();
            dialog.close();
            Ok(choice(response))
        });
        let _ = send.send(result);
    }).map_err(|error| error.to_string())?;
    receive.await.map_err(|_| "Exit confirmation closed before returning its choice.".to_owned())?
}

fn choice(response: gtk::ResponseType) -> Choice {
    if response == gtk::ResponseType::Ok { Choice::Quit } else { Choice::KeepWorking }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_the_explicit_quit_response_approves() {
        assert_eq!(choice(gtk::ResponseType::Ok), Choice::Quit);
        for response in [gtk::ResponseType::None, gtk::ResponseType::DeleteEvent,
                         gtk::ResponseType::Cancel, gtk::ResponseType::Close,
                         gtk::ResponseType::Accept, gtk::ResponseType::Yes] {
            assert_eq!(choice(response), Choice::KeepWorking);
        }
    }
}
