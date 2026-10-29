use super::idle::IdleFacts;
use crate::linux_desktop::session::{GraphicalSession, SessionUnavailable};
use serde::Serialize;

/// How the monitor sees the person step away, as macOS does through the
/// screen lock where it can.
#[derive(Debug, Clone, Copy, Default, Serialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub(crate) enum StepAwayMode {
    /// The session's locker is observable: logind's session together with a
    /// screen saver service that answers, or a `LockedHint` seen true in this
    /// session. Lock, sleep and user switch end unlocks; input idle does not.
    LockObserved,
    /// No lock observation exists, as with lockers that bypass logind on
    /// wlroots compositors, Hyprland or i3. Display-server input idle for the
    /// unlock idle period steps the person away.
    IdleFallback,
    /// Neither: presence cannot be confirmed.
    #[default]
    Unavailable,
}

/// What the monitor currently observes. `can_lock` on the session is
/// reported only: logind derives it from the session class, it does not say
/// that any locker reports to logind, and systemd 255 and 256 lack it.
#[derive(Debug, Clone, Default, Serialize)]
pub(crate) struct LockSources {
    pub session: Option<GraphicalSession>,
    pub session_error: Option<SessionUnavailable>,
    pub screensaver_locked: Option<bool>,
    /// `LockedHint` was true at least once in this logind session, so
    /// something maintains it.
    pub locked_hint_seen: bool,
    pub system_connected: bool,
    pub session_bus_connected: bool,
    pub sleeping: bool,
    pub idle: IdleFacts,
    /// The mode these observations select; recomputed on every change.
    pub mode: StepAwayMode,
}

impl LockSources {
    /// The mode decided from structured observations only.
    pub(super) fn step_away_mode(&self) -> StepAwayMode {
        let locker_observed =
            self.session.is_some() && (self.screensaver_locked.is_some() || self.locked_hint_seen);
        if locker_observed {
            StepAwayMode::LockObserved
        } else if self.idle.source.is_some() {
            StepAwayMode::IdleFallback
        } else {
            StepAwayMode::Unavailable
        }
    }

    /// A source reports that the person stepped away. Input idle counts
    /// only where no lock is observable.
    pub(super) fn observed_away(&self) -> bool {
        self.sleeping
            || (self.step_away_mode() == StepAwayMode::IdleFallback && self.idle.idle)
            || self
                .session
                .as_ref()
                .is_some_and(|session| !session.active || session.locked)
            || self.screensaver_locked == Some(true)
    }

    /// A logind session that became unreadable, changed identity or belongs
    /// to another user is stale, not absent, and observes nothing.
    fn session_readable(&self) -> bool {
        self.session.is_some()
            || matches!(
                self.session_error,
                None | Some(SessionUnavailable::NoGraphicalSession)
            )
    }

    /// The person-facing reason confirmation is refused, or none.
    pub(crate) fn confirmation_refusal(&self) -> Option<&'static str> {
        if self.confirmation_allowed() {
            return None;
        }
        if self.step_away_mode() == StepAwayMode::Unavailable {
            return Some("This desktop reports neither its screen lock nor when input goes idle, so stepping away cannot be detected.");
        }
        Some("The graphical session is locked, inactive, idle, or its lock state is unavailable.")
    }

    pub(super) fn confirmation_allowed(&self) -> bool {
        self.step_away_mode() != StepAwayMode::Unavailable
            && self.session_readable()
            && !self.observed_away()
    }
}

#[cfg(test)]
mod tests {
    use super::super::idle::{IdleSource, IdleUnavailable};
    use super::*;

    fn observer(idle: bool) -> IdleFacts {
        IdleFacts {
            source: Some(IdleSource::WaylandInputIdle),
            error: None,
            idle,
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

    fn lock_observed() -> LockSources {
        LockSources {
            session: Some(session(true, false, false)),
            screensaver_locked: Some(false),
            ..LockSources::default()
        }
    }

    #[test]
    fn a_session_with_a_screen_saver_is_lock_observed_and_idle_never_ends_it() {
        let mut state = lock_observed();
        assert_eq!(state.step_away_mode(), StepAwayMode::LockObserved);
        assert!(state.confirmation_allowed());
        state.idle = observer(true);
        assert!(!state.observed_away() && state.confirmation_allowed());
        state.session.as_mut().unwrap().locked = true;
        assert!(state.observed_away() && !state.confirmation_allowed());
        let mut saver = lock_observed();
        saver.screensaver_locked = Some(true);
        assert!(!saver.confirmation_allowed());
        let mut asleep = lock_observed();
        asleep.sleeping = true;
        assert!(!asleep.confirmation_allowed());
        let mut switched = lock_observed();
        switched.session.as_mut().unwrap().active = false;
        assert!(!switched.confirmation_allowed());
    }

    #[test]
    fn a_locked_hint_once_true_proves_a_maintained_hint() {
        let mut state = LockSources {
            session: Some(session(true, false, false)),
            idle: observer(false),
            ..LockSources::default()
        };
        assert_eq!(state.step_away_mode(), StepAwayMode::IdleFallback);
        state.locked_hint_seen = true;
        assert_eq!(state.step_away_mode(), StepAwayMode::LockObserved);
        state.idle.idle = true;
        assert!(state.confirmation_allowed());
    }

    #[test]
    fn without_lock_observation_input_idle_steps_away() {
        let mut state = LockSources {
            session: Some(session(true, false, false)),
            idle: observer(false),
            ..LockSources::default()
        };
        assert_eq!(state.step_away_mode(), StepAwayMode::IdleFallback);
        assert!(state.confirmation_allowed());
        state.idle.idle = true;
        assert!(state.observed_away() && !state.confirmation_allowed());
        assert!(state
            .confirmation_refusal()
            .is_some_and(|reason| reason.contains("idle")));
    }

    #[test]
    fn neither_lock_observation_nor_an_idle_observer_is_unavailable() {
        for error in [
            IdleUnavailable::NoIdleNotifier,
            IdleUnavailable::MutterNotCompositor,
            IdleUnavailable::X11NoScreenSaver,
            IdleUnavailable::Lost,
        ] {
            let state = LockSources {
                session: Some(session(true, false, false)),
                idle: IdleFacts {
                    source: None,
                    error: Some(error),
                    idle: false,
                },
                ..LockSources::default()
            };
            assert_eq!(state.step_away_mode(), StepAwayMode::Unavailable);
            assert!(!state.confirmation_allowed());
            assert!(state
                .confirmation_refusal()
                .is_some_and(|reason| reason.contains("neither")));
        }
        assert!(!LockSources::default().confirmation_allowed());
    }

    #[test]
    fn can_lock_is_not_evidence_of_lock_observation() {
        let state = LockSources {
            session: Some(session(true, false, true)),
            ..LockSources::default()
        };
        assert_eq!(state.step_away_mode(), StepAwayMode::Unavailable);
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
                idle: observer(false),
                ..LockSources::default()
            };
            assert!(!state.confirmation_allowed());
        }
    }
}
