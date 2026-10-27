//! Ownership of a file tab carried by a native drag.
//!
//! The shell mints a single-use token bound to the initiating window when a
//! native drag begins. The tab stays with its initiator until a recipient
//! window commits the token, or the shell opens a torn-off window for it. The
//! initiator marks the transfer ready once its latest draft is published, so a
//! recipient never opens stale contents; drops and tear-offs that arrive
//! earlier wait for it.

use std::collections::HashMap;

use super::ItemWindowView;

/// What the initiator's tab does when the transfer settles.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(super) enum Settled {
    /// Another window owns the file now; the initiator closes its tab.
    Committed,
    /// The tab stays where it was.
    Cancelled,
}

/// Work the shell does once a transfer is ready.
#[derive(Debug, Clone, PartialEq)]
pub(super) enum Delivery {
    /// Tell the recipient to open the file at this client position.
    Drop {
        recipient: String,
        x: f64,
        y: f64,
        view: ItemWindowView,
    },
    /// Open a new window for the file where the compositor places it.
    TearOff { view: ItemWindowView },
}

/// A settled transfer's notice to its initiator.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(super) struct Notice {
    pub initiator: String,
    pub token: String,
    pub settled: Settled,
}

#[derive(Debug, Clone, PartialEq)]
enum Stage {
    Dragging,
    Dropped {
        recipient: String,
        x: f64,
        y: f64,
        delivered: bool,
    },
    TearingOff,
}

#[derive(Debug, Clone)]
struct Transfer {
    initiator: String,
    view: ItemWindowView,
    ready: bool,
    stage: Stage,
}

/// Why a token was refused.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(super) enum Refusal {
    /// Unknown, already settled, or replayed.
    UnknownToken,
    /// The caller is not the window the token is bound to.
    WrongWindow,
    /// The transfer is not in a state that accepts this step.
    OutOfOrder,
    /// The recipient shows a different project.
    ForeignProject,
}

#[derive(Default)]
pub(super) struct Transfers {
    by_token: HashMap<String, Transfer>,
}

impl Transfers {
    /// Registers a transfer for `initiator`; one window drags one tab at a time.
    pub fn begin(&mut self, token: String, initiator: &str, view: ItemWindowView) -> Result<(), Refusal> {
        if self.by_token.values().any(|transfer| transfer.initiator == initiator && transfer.stage == Stage::Dragging) {
            return Err(Refusal::OutOfOrder);
        }
        self.by_token.insert(
            token,
            Transfer {
                initiator: initiator.into(),
                view,
                ready: false,
                stage: Stage::Dragging,
            },
        );
        Ok(())
    }

    pub fn view_of(&self, token: &str) -> Option<ItemWindowView> {
        self.by_token.get(token).map(|transfer| transfer.view.clone())
    }

    /// The initiator published its draft; returns the work that was waiting.
    pub fn ready(&mut self, token: &str, caller: &str) -> Result<Option<Delivery>, Refusal> {
        let transfer = self.owned_by_initiator(token, caller)?;
        if transfer.ready {
            return Ok(None);
        }
        transfer.ready = true;
        Ok(match &mut transfer.stage {
            Stage::Dropped { recipient, x, y, delivered } => {
                *delivered = true;
                Some(Delivery::Drop {
                    recipient: recipient.clone(),
                    x: *x,
                    y: *y,
                    view: transfer.view.clone(),
                })
            }
            Stage::TearingOff => Some(Delivery::TearOff { view: transfer.view.clone() }),
            Stage::Dragging => None,
        })
    }

    /// A recipient window accepted the native drop at a client position.
    pub fn drop_at(&mut self, token: &str, recipient: &str, project_id: &str, x: f64, y: f64) -> Result<Option<Delivery>, Refusal> {
        let transfer = self.by_token.get_mut(token).ok_or(Refusal::UnknownToken)?;
        if transfer.stage != Stage::Dragging {
            return Err(Refusal::OutOfOrder);
        }
        if transfer.initiator == recipient {
            return Err(Refusal::WrongWindow);
        }
        if transfer.view.project_id != project_id {
            return Err(Refusal::ForeignProject);
        }
        transfer.stage = Stage::Dropped {
            recipient: recipient.into(),
            x,
            y,
            delivered: transfer.ready,
        };
        Ok(transfer.ready.then(|| Delivery::Drop {
            recipient: recipient.into(),
            x,
            y,
            view: transfer.view.clone(),
        }))
    }

    /// The drag ended over no window that accepted it.
    pub fn tear_off(&mut self, token: &str) -> Result<Option<Delivery>, Refusal> {
        let transfer = self.by_token.get_mut(token).ok_or(Refusal::UnknownToken)?;
        if transfer.stage != Stage::Dragging {
            return Err(Refusal::OutOfOrder);
        }
        transfer.stage = Stage::TearingOff;
        Ok(transfer.ready.then(|| Delivery::TearOff { view: transfer.view.clone() }))
    }

    /// The drag was cancelled, or ended over the initiator itself.
    pub fn cancel(&mut self, token: &str) -> Option<Notice> {
        let transfer = self.by_token.get(token)?;
        if transfer.stage != Stage::Dragging {
            return None;
        }
        self.settle(token, Settled::Cancelled)
    }

    /// The recipient opened the file; consumes the token.
    pub fn commit(&mut self, token: &str, caller: &str) -> Result<Notice, Refusal> {
        self.recipient_step(token, caller)?;
        self.settle(token, Settled::Committed).ok_or(Refusal::UnknownToken)
    }

    /// The recipient could not open the file; ownership returns to the initiator.
    pub fn give_back(&mut self, token: &str, caller: &str) -> Result<Notice, Refusal> {
        self.recipient_step(token, caller)?;
        self.settle(token, Settled::Cancelled).ok_or(Refusal::UnknownToken)
    }

    /// The torn-off window opened, or could not.
    pub fn torn_off(&mut self, token: &str, opened: bool) -> Option<Notice> {
        if self.by_token.get(token)?.stage != Stage::TearingOff {
            return None;
        }
        self.settle(token, if opened { Settled::Committed } else { Settled::Cancelled })
    }

    /// The initiator could not publish its draft; nothing leaves it.
    pub fn abandon(&mut self, token: &str, caller: &str) -> Result<Notice, Refusal> {
        self.owned_by_initiator(token, caller)?;
        self.settle(token, Settled::Cancelled).ok_or(Refusal::UnknownToken)
    }

    /// Settles every transfer a destroyed window took part in.
    pub fn window_destroyed(&mut self, label: &str) -> Vec<Notice> {
        let tokens: Vec<String> = self
            .by_token
            .iter()
            .filter(|(_, transfer)| {
                transfer.initiator == label || matches!(&transfer.stage, Stage::Dropped { recipient, .. } if recipient == label)
            })
            .map(|(token, _)| token.clone())
            .collect();
        tokens
            .into_iter()
            .filter_map(|token| self.settle(&token, Settled::Cancelled))
            .filter(|notice| notice.initiator != label)
            .collect()
    }

    fn owned_by_initiator(&mut self, token: &str, caller: &str) -> Result<&mut Transfer, Refusal> {
        let transfer = self.by_token.get_mut(token).ok_or(Refusal::UnknownToken)?;
        if transfer.initiator != caller {
            return Err(Refusal::WrongWindow);
        }
        Ok(transfer)
    }

    fn recipient_step(&self, token: &str, caller: &str) -> Result<(), Refusal> {
        match &self.by_token.get(token).ok_or(Refusal::UnknownToken)?.stage {
            Stage::Dropped { recipient, delivered: true, .. } if recipient == caller => Ok(()),
            Stage::Dropped { recipient, .. } if recipient != caller => Err(Refusal::WrongWindow),
            _ => Err(Refusal::OutOfOrder),
        }
    }

    fn settle(&mut self, token: &str, settled: Settled) -> Option<Notice> {
        let transfer = self.by_token.remove(token)?;
        Some(Notice {
            initiator: transfer.initiator,
            token: token.into(),
            settled,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn view(project: &str) -> ItemWindowView {
        ItemWindowView {
            label: String::new(),
            title: "notes.md".into(),
            view_number: 0,
            kind: "file".into(),
            project_id: project.into(),
            session_id: None,
            root_id: Some("root".into()),
            path: Some("notes.md".into()),
            stage_id: None,
        }
    }

    fn dragging() -> Transfers {
        let mut transfers = Transfers::default();
        transfers.begin("t1".into(), "main", view("p1")).unwrap();
        transfers
    }

    #[test]
    fn a_drop_waits_for_the_published_draft_and_commits_once() {
        let mut transfers = dragging();
        assert_eq!(transfers.drop_at("t1", "file:a:2", "p1", 10.0, 4.0), Ok(None));
        let delivery = transfers.ready("t1", "main").unwrap();
        assert!(matches!(delivery, Some(Delivery::Drop { ref recipient, x, .. }) if recipient == "file:a:2" && x == 10.0));
        assert_eq!(transfers.commit("t1", "main"), Err(Refusal::WrongWindow));
        let notice = transfers.commit("t1", "file:a:2").unwrap();
        assert_eq!(notice.settled, Settled::Committed);
        assert_eq!(notice.initiator, "main");
        assert_eq!(transfers.commit("t1", "file:a:2"), Err(Refusal::UnknownToken));
    }

    #[test]
    fn a_ready_transfer_delivers_its_drop_at_once() {
        let mut transfers = dragging();
        assert_eq!(transfers.ready("t1", "main"), Ok(None));
        assert!(matches!(transfers.drop_at("t1", "file:a:2", "p1", 1.0, 2.0), Ok(Some(Delivery::Drop { .. }))));
        assert!(transfers.give_back("t1", "file:a:2").is_ok_and(|notice| notice.settled == Settled::Cancelled));
    }

    #[test]
    fn stale_foreign_and_replayed_tokens_are_refused() {
        let mut transfers = dragging();
        assert_eq!(transfers.drop_at("forged", "file:a:2", "p1", 0.0, 0.0), Err(Refusal::UnknownToken));
        assert_eq!(transfers.drop_at("t1", "file:a:2", "p2", 0.0, 0.0), Err(Refusal::ForeignProject));
        assert_eq!(transfers.drop_at("t1", "main", "p1", 0.0, 0.0), Err(Refusal::WrongWindow));
        assert_eq!(transfers.ready("t1", "file:a:2"), Err(Refusal::WrongWindow));
        assert!(transfers.drop_at("t1", "file:a:2", "p1", 0.0, 0.0).is_ok());
        assert_eq!(transfers.drop_at("t1", "file:b:3", "p1", 0.0, 0.0), Err(Refusal::OutOfOrder));
        assert_eq!(transfers.commit("t1", "file:a:2"), Err(Refusal::OutOfOrder), "not delivered before ready");
        assert_eq!(transfers.cancel("t1"), None, "a dropped transfer is no longer cancellable by the drag");
    }

    #[test]
    fn tear_off_waits_for_ready_and_settles_by_window_creation() {
        let mut transfers = dragging();
        assert_eq!(transfers.tear_off("t1"), Ok(None));
        assert!(matches!(transfers.ready("t1", "main"), Ok(Some(Delivery::TearOff { .. }))));
        assert_eq!(transfers.torn_off("t1", true).map(|notice| notice.settled), Some(Settled::Committed));
        let mut failed = dragging();
        failed.ready("t1", "main").unwrap();
        assert!(matches!(failed.tear_off("t1"), Ok(Some(Delivery::TearOff { .. }))));
        assert_eq!(failed.torn_off("t1", false).map(|notice| notice.settled), Some(Settled::Cancelled));
    }

    #[test]
    fn readiness_delivers_waiting_work_once() {
        let mut transfers = dragging();
        transfers.tear_off("t1").unwrap();
        assert!(matches!(transfers.ready("t1", "main"), Ok(Some(Delivery::TearOff { .. }))));
        assert_eq!(transfers.ready("t1", "main"), Ok(None));
    }

    #[test]
    fn cancellation_and_abandonment_keep_the_tab() {
        let mut transfers = dragging();
        assert_eq!(transfers.cancel("t1").map(|notice| notice.settled), Some(Settled::Cancelled));
        assert_eq!(transfers.cancel("t1"), None);
        let mut abandoned = dragging();
        assert_eq!(abandoned.abandon("t1", "file:a:2"), Err(Refusal::WrongWindow));
        assert!(abandoned.abandon("t1", "main").is_ok_and(|notice| notice.settled == Settled::Cancelled));
    }

    #[test]
    fn one_window_drags_one_tab_at_a_time() {
        let mut transfers = dragging();
        assert_eq!(transfers.begin("t2".into(), "main", view("p1")), Err(Refusal::OutOfOrder));
        assert!(transfers.begin("t3".into(), "file:a:2", view("p1")).is_ok());
    }

    #[test]
    fn a_closed_recipient_returns_ownership_and_a_closed_initiator_drops_its_transfers() {
        let mut transfers = dragging();
        transfers.drop_at("t1", "file:a:2", "p1", 0.0, 0.0).unwrap();
        let notices = transfers.window_destroyed("file:a:2");
        assert_eq!(notices.len(), 1);
        assert_eq!(notices[0].settled, Settled::Cancelled);
        let mut initiator_closed = dragging();
        assert!(initiator_closed.window_destroyed("main").is_empty());
        assert_eq!(initiator_closed.view_of("t1"), None);
    }
}
