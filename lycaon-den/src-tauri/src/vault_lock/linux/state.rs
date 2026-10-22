use crate::linux_desktop::session::{GraphicalSession, SessionUnavailable};
use serde::Serialize;

/// What the monitor currently observes. `can_lock` on the session is
/// reported only: logind derives it from the session class, it does not say
/// that any locker reports to logind, and systemd 255 and 256 lack it.
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
    /// A source reports that the person stepped away.
    pub(super) fn observed_away(&self) -> bool {
        self.sleeping
            || self
                .session
                .as_ref()
                .is_some_and(|session| !session.active || session.locked)
            || self.screensaver_locked == Some(true)
    }

    /// Whether a lock source observes this session now. A logind session
    /// that became unreadable, changed identity or belongs to another user
    /// is stale, not absent, and observes nothing.
    fn lock_observed(&self) -> bool {
        match (&self.session, &self.session_error) {
            (Some(_), _) => true,
            (None, None | Some(SessionUnavailable::NoGraphicalSession)) => {
                self.screensaver_locked.is_some()
            }
            (None, Some(_)) => false,
        }
    }

    pub(super) fn confirmation_allowed(&self) -> bool {
        !self.observed_away() && self.lock_observed()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn session(active: bool, locked: bool, can_lock: bool) -> GraphicalSession {
        GraphicalSession {
            id: "one".into(),
            path: "/session/one".into(),
            uid: 1000,
            session_type: "wayland".into(),
            active,
            can_lock,
            locked,
        }
    }

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
        state.session = Some(session(false, false, true));
        assert!(!state.confirmation_allowed());
        state.session.as_mut().unwrap().active = true;
        assert!(state.confirmation_allowed());
        state.session.as_mut().unwrap().locked = true;
        assert!(!state.confirmation_allowed());
    }

    #[test]
    fn can_lock_is_not_evidence_of_lock_observation() {
        // systemd 255 and 256 omit CanLock; the session still reports its lock.
        let state = LockSources {
            session: Some(session(true, false, false)),
            ..LockSources::default()
        };
        assert!(state.confirmation_allowed());
        let observed = LockSources {
            session: Some(session(true, true, true)),
            ..LockSources::default()
        };
        assert!(!observed.confirmation_allowed());
    }

    #[test]
    fn a_stale_session_source_never_keeps_confirmation_allowed() {
        for error in [
            SessionUnavailable::Unreadable,
            SessionUnavailable::InvalidProperties,
            SessionUnavailable::WrongUser,
        ] {
            let state = LockSources {
                session_error: Some(error),
                screensaver_locked: Some(false),
                ..LockSources::default()
            };
            assert!(!state.confirmation_allowed());
        }
        let no_session = LockSources {
            session_error: Some(SessionUnavailable::NoGraphicalSession),
            screensaver_locked: Some(false),
            ..LockSources::default()
        };
        assert!(no_session.confirmation_allowed());
    }
}
