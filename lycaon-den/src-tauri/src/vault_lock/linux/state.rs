use super::idle::IdleFacts;
use crate::linux_desktop::session::{GraphicalSession, SessionUnavailable};
use serde::Serialize;

/// What the monitor currently observes. `can_lock` on the session is
/// reported only: logind derives it from the session class, it does not say
/// that any locker reports to logind, and systemd 255 and 256 lack it.
///
/// Lock sources make a lock take effect at once; the display server's input
/// idle is the coverage, because a locker may tell neither logind nor a
/// screen saver service.
#[derive(Debug, Clone, Default, Serialize)]
pub(crate) struct LockSources {
    pub session: Option<GraphicalSession>,
    pub session_error: Option<SessionUnavailable>,
    pub screensaver_locked: Option<bool>,
    pub system_connected: bool,
    pub session_bus_connected: bool,
    pub sleeping: bool,
    pub idle: IdleFacts,
}

impl LockSources {
    /// A source reports that the person stepped away.
    pub(super) fn observed_away(&self) -> bool {
        self.sleeping
            || self.idle.idle
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
        !self.observed_away() && self.lock_observed() && self.idle.source.is_some()
    }
}

#[cfg(test)]
mod tests {
    use super::super::idle::{IdleSource, IdleUnavailable};
    use super::*;

    fn observed() -> IdleFacts {
        IdleFacts {
            source: Some(IdleSource::WaylandInputIdle),
            error: None,
            idle: false,
        }
    }

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
        let mut state = LockSources {
            idle: observed(),
            ..LockSources::default()
        };
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
            idle: observed(),
            ..LockSources::default()
        };
        assert!(state.confirmation_allowed());
        let locked = LockSources {
            session: Some(session(true, true, true)),
            idle: observed(),
            ..LockSources::default()
        };
        assert!(!locked.confirmation_allowed());
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
                idle: observed(),
                ..LockSources::default()
            };
            assert!(!state.confirmation_allowed());
        }
        let no_session = LockSources {
            session_error: Some(SessionUnavailable::NoGraphicalSession),
            screensaver_locked: Some(false),
            idle: observed(),
            ..LockSources::default()
        };
        assert!(no_session.confirmation_allowed());
    }

    #[test]
    fn input_idle_is_the_coverage_and_steps_away() {
        let mut state = LockSources {
            session: Some(session(true, false, false)),
            idle: observed(),
            ..LockSources::default()
        };
        assert!(state.confirmation_allowed());
        state.idle.idle = true;
        assert!(state.observed_away() && !state.confirmation_allowed());
        // Lock sources alone never keep confirmation allowed: a locker may
        // report to neither of them.
        for error in [
            IdleUnavailable::NoIdleNotifier,
            IdleUnavailable::MutterNotCompositor,
            IdleUnavailable::X11NoScreenSaver,
            IdleUnavailable::Lost,
        ] {
            state.idle = IdleFacts {
                source: None,
                error: Some(error),
                idle: false,
            };
            assert!(!state.confirmation_allowed());
        }
    }
}
