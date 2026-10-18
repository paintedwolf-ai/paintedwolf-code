use crate::linux_desktop::session::{GraphicalSession, SessionUnavailable};
use serde::Serialize;

#[derive(Debug, Clone, Default, Serialize)]
pub(crate) struct LockSources {
    pub session: Option<GraphicalSession>,
    pub session_error: Option<SessionUnavailable>,
    pub screensaver_locked: Option<bool>,
    pub system_connected: bool,
    pub session_bus_connected: bool,
    pub sleeping: bool,
}

impl LockSources {
    pub(super) fn confirmation_allowed(&self) -> bool {
        !self.sleeping
            && self.session_error != Some(SessionUnavailable::WrongUser)
            && self
                .session
                .as_ref()
                .is_none_or(|session| session.active && !session.locked)
            && self.screensaver_locked != Some(true)
            && (self
                .session
                .as_ref()
                .is_some_and(|session| session.can_lock)
                || self.screensaver_locked.is_some())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn unknown_locked_inactive_and_sleeping_sessions_cannot_confirm() {
        let mut state = LockSources::default();
        assert!(!state.confirmation_allowed());
        state.screensaver_locked = Some(false);
        assert!(state.confirmation_allowed());
        state.session_error = Some(SessionUnavailable::WrongUser);
        assert!(!state.confirmation_allowed());
        state.session_error = None;
        state.sleeping = true;
        assert!(!state.confirmation_allowed());
        state.sleeping = false;
        state.session = Some(GraphicalSession {
            id: "one".into(),
            path: "/session/one".into(),
            uid: 1000,
            session_type: "wayland".into(),
            active: false,
            can_lock: true,
            locked: false,
        });
        assert!(!state.confirmation_allowed());
        state.session.as_mut().unwrap().active = true;
        assert!(state.confirmation_allowed());
        state.session.as_mut().unwrap().locked = true;
        assert!(!state.confirmation_allowed());
    }
}
