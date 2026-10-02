//! Watches the engine this shell launched. An exit nobody asked for is
//! reaped, recorded, and answered with a bounded run of restarts; every
//! change is published so each window can follow the engine it talks to.

use super::process::{kill_child, wait_for_exit};
use super::{SidecarInfo, SidecarStartError, SidecarState};
use serde::Serialize;
use std::process::ExitStatus;
use std::sync::atomic::Ordering;
use std::sync::mpsc::Receiver;
use std::time::{Duration, Instant};

/// Event name for [`EngineState`] changes.
pub(crate) const ENGINE_STATE_EVENT: &str = "engine-state";

/// How the shell answers unexpected exits.
pub(crate) struct RestartPolicy {
    /// Unexpected exits inside this window count toward one crash loop.
    pub(crate) crash_window: Duration,
    /// Delay before each automatic restart within a crash loop; one more exit
    /// inside the window stops retrying.
    pub(crate) delays: &'static [Duration],
}

pub(crate) const RESTART_POLICY: RestartPolicy = RestartPolicy {
    crash_window: Duration::from_secs(120),
    delays: &[
        Duration::from_millis(250),
        Duration::from_secs(2),
        Duration::from_secs(5),
    ],
};
/// An engine that closed its output gets this long to finish exiting.
const REAP_GRACE: Duration = Duration::from_secs(2);

/// What the shell knows about the engine it supervises.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(tag = "state", rename_all = "snake_case")]
pub enum EngineState {
    /// This shell has no engine of its own: before the first launch, after an
    /// intentional stop, or when attaching to a development engine.
    Idle,
    /// Serving. A new generation is a new process with its own port and token.
    Running { generation: u64 },
    /// Exited unexpectedly; the shell is launching a replacement.
    Restarting { exit: EngineExit, attempt: u32 },
    /// Exited unexpectedly and the shell stopped retrying: the engine kept
    /// exiting, or its replacement could not start.
    Stopped {
        exit: EngineExit,
        #[serde(skip_serializing_if = "Option::is_none")]
        failure: Option<String>,
    },
}

/// How an engine process ended.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct EngineExit {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub code: Option<i32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub signal: Option<i32>,
    /// What happened to the engine, as a predicate: "was killed by signal 9
    /// (SIGKILL)", "exited with status 3".
    pub description: String,
}

/// One launched engine's end of output, which closes when the process exits.
pub(crate) struct ExitWatch {
    pub(super) generation: u64,
    pub(super) closed: Receiver<()>,
}

/// Runs for the life of the shell, answering each launched engine's exit.
/// `relaunch` starts a replacement under the lifecycle lock.
pub(crate) fn supervise<R>(
    state: &SidecarState,
    watches: Receiver<ExitWatch>,
    policy: &RestartPolicy,
    relaunch: R,
) where
    R: Fn(&SidecarState) -> Result<SidecarInfo, SidecarStartError>,
{
    for watch in watches {
        // Disconnects when the engine's output reaches end of file.
        let _ = watch.closed.recv();
        recover(state, watch.generation, policy, &relaunch);
    }
}

fn recover<R>(state: &SidecarState, generation: u64, policy: &RestartPolicy, relaunch: &R)
where
    R: Fn(&SidecarState) -> Result<SidecarInfo, SidecarStartError>,
{
    let lifecycle = state.lifecycle.lock().unwrap();
    // A stop, restart, or newer launch already accounted for this process.
    if state.generation.load(Ordering::Acquire) != generation {
        return;
    }
    let exit = reap(state);
    state.retire();
    let attempt = state.record_crash(Instant::now(), policy.crash_window);
    let Some(delay) = policy.delays.get(attempt as usize - 1).copied() else {
        state.publish(EngineState::Stopped { exit, failure: None });
        return;
    };
    state.publish(EngineState::Restarting {
        exit: exit.clone(),
        attempt,
    });
    let retired = state.generation.load(Ordering::Acquire);
    drop(lifecycle);
    std::thread::sleep(delay);

    let _lifecycle = state.lifecycle.lock().unwrap();
    // Someone started or stopped an engine during the delay; theirs stands.
    if state.generation.load(Ordering::Acquire) != retired {
        return;
    }
    if let Err(err) = relaunch(state) {
        state.publish(EngineState::Stopped {
            exit,
            failure: Some(err.message().to_string()),
        });
    }
}

/// Takes the exited child and reports how it ended.
fn reap(state: &SidecarState) -> EngineExit {
    let mut slot = state.process.lock().unwrap();
    let Some(child) = slot.as_mut() else {
        return describe_exit(None);
    };
    if wait_for_exit(child, REAP_GRACE) {
        let status = child.try_wait().ok().flatten();
        slot.take();
        return describe_exit(status);
    }
    // Output closed but the process lives on; it can no longer be trusted.
    kill_child(&mut slot);
    EngineExit {
        code: None,
        signal: None,
        description: "closed its output and was stopped".into(),
    }
}

/// The engine log's account of a state change, when it records one.
pub(super) fn log_line(state: &EngineState) -> Option<String> {
    match state {
        EngineState::Idle | EngineState::Running { .. } => None,
        EngineState::Restarting { exit, attempt } => Some(format!(
            "--- engine {}; restarting (attempt {attempt}) ---\n",
            exit.description
        )),
        EngineState::Stopped { exit, failure: None } => Some(format!(
            "--- engine {}; not restarting after repeated exits ---\n",
            exit.description
        )),
        EngineState::Stopped { failure: Some(failure), .. } => Some(format!(
            "--- replacement engine could not start: {failure} ---\n"
        )),
    }
}

pub(super) fn describe_exit(status: Option<ExitStatus>) -> EngineExit {
    let Some(status) = status else {
        return EngineExit {
            code: None,
            signal: None,
            description: "exited without a status".into(),
        };
    };
    #[cfg(unix)]
    {
        use std::os::unix::process::ExitStatusExt;
        if let Some(signal) = status.signal() {
            let description = match signal_name(signal) {
                Some(name) => format!("was killed by signal {signal} ({name})"),
                None => format!("was killed by signal {signal}"),
            };
            return EngineExit {
                code: None,
                signal: Some(signal),
                description,
            };
        }
    }
    let code = status.code();
    EngineExit {
        code,
        signal: None,
        description: match code {
            Some(code) => format!("exited with status {code}"),
            None => "exited".into(),
        },
    }
}

#[cfg(unix)]
fn signal_name(signal: i32) -> Option<&'static str> {
    Some(match signal {
        libc::SIGHUP => "SIGHUP",
        libc::SIGINT => "SIGINT",
        libc::SIGQUIT => "SIGQUIT",
        libc::SIGILL => "SIGILL",
        libc::SIGTRAP => "SIGTRAP",
        libc::SIGABRT => "SIGABRT",
        libc::SIGBUS => "SIGBUS",
        libc::SIGFPE => "SIGFPE",
        libc::SIGKILL => "SIGKILL",
        libc::SIGSEGV => "SIGSEGV",
        libc::SIGPIPE => "SIGPIPE",
        libc::SIGTERM => "SIGTERM",
        _ => return None,
    })
}
