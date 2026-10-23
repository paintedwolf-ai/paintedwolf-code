//! Protected Linux startup consumes the sealed generation selection (4), claims
//! the outside-namespace broker (6) and retains the generation lifetime lock (7).
//! Only the engine receives the broker; startup authority never reaches children.

use std::sync::OnceLock;

mod generation;
pub(crate) use generation::{Generation, GenerationError};

static GENERATION: OnceLock<Result<Option<Generation>, GenerationError>> = OnceLock::new();

pub(crate) fn generation() -> Result<Option<Generation>, GenerationError> {
    GENERATION.get().cloned().unwrap_or(Ok(None))
}

/// Descriptor numbers the protected entry leaves open for the desktop.
pub(crate) const BROKER_FD: i32 = 6;
const LIFETIME_FD: i32 = 7;

/// The engine reads the broker from this descriptor number.
pub(crate) const LAUNCH_BROKER_FD_ENV: &str = "LYCAON_LAUNCH_BROKER_FD";

static BROKER: OnceLock<Option<i32>> = OnceLock::new();

/// Whether descriptor facts name the bootstrap's broker: a sequenced-packet
/// socket whose pair was created by uid 0, which nothing inside the protected
/// namespace can be.
pub(crate) fn is_launch_broker(socket_type: i32, peer_uid: u32) -> bool {
    socket_type == libc::SOCK_SEQPACKET && peer_uid == 0
}

/// Claims the launch descriptors once, at startup, before any thread or child
/// exists to inherit them.
pub(crate) fn claim() -> Result<(), GenerationError> {
    BROKER.get_or_init(|| {
        #[cfg(target_os = "linux")]
        let launch = claim_linux();
        #[cfg(not(target_os = "linux"))]
        let launch: Option<(i32, Result<Generation, GenerationError>)> = None;
        let (broker, selected) = match launch {
            Some((broker, selected)) => (Some(broker), selected.map(Some)),
            None => (None, Ok(None)),
        };
        #[cfg(target_os = "linux")]
        let marker = std::env::var_os("PW_PROTECTED_LAUNCH");
        #[cfg(not(target_os = "linux"))]
        let marker: Option<std::ffi::OsString> = None;
        let _ = GENERATION.set(selected_launch(
            marker.as_deref(),
            broker.is_some(),
            selected,
        ));
        broker
    });
    generation().map(|_| ())
}

// This marker can only tighten admission. A caller can forge it in an ordinary
// launch, which refuses that launch; it never supplies broker or generation facts.
fn selected_launch(
    marker: Option<&std::ffi::OsStr>,
    broker: bool,
    selected: Result<Option<Generation>, GenerationError>,
) -> Result<Option<Generation>, GenerationError> {
    match marker {
        Some(value) if value != "1" => Err(GenerationError::InvalidFrame),
        Some(_) if !broker => Err(GenerationError::BrokerMismatch),
        _ => selected,
    }
}

/// The claimed broker, when this desktop runs under the protected launch.
pub(crate) fn broker() -> Option<i32> {
    BROKER.get().copied().flatten()
}

#[cfg(target_os = "linux")]
fn claim_linux() -> Option<(i32, Result<Generation, GenerationError>)> {
    let mut descriptor_error = false;
    // SAFETY: fcntl and getsockopt only read descriptor state into locals.
    unsafe {
        let mut kind: libc::c_int = 0;
        let mut length = std::mem::size_of::<libc::c_int>() as libc::socklen_t;
        if libc::getsockopt(
            BROKER_FD,
            libc::SOL_SOCKET,
            libc::SO_TYPE,
            (&mut kind as *mut libc::c_int).cast(),
            &mut length,
        ) != 0
        {
            return None;
        }
        let mut peer: libc::ucred = std::mem::zeroed();
        let mut length = std::mem::size_of::<libc::ucred>() as libc::socklen_t;
        if libc::getsockopt(
            BROKER_FD,
            libc::SOL_SOCKET,
            libc::SO_PEERCRED,
            (&mut peer as *mut libc::ucred).cast(),
            &mut length,
        ) != 0
        {
            return None;
        }
        if !is_launch_broker(kind, peer.uid) {
            return None;
        }
        for fd in [BROKER_FD, LIFETIME_FD] {
            let flags = libc::fcntl(fd, libc::F_GETFD);
            if flags < 0 || libc::fcntl(fd, libc::F_SETFD, flags | libc::FD_CLOEXEC) < 0 {
                descriptor_error = true;
            }
        }
    }
    // A root broker establishes protected launch even when its frame is invalid:
    // retain the refusal so layout discovery cannot fall through to dev paths.
    let selected = generation::read(4, BROKER_FD);
    Some((
        BROKER_FD,
        if descriptor_error {
            Err(GenerationError::FrameUnavailable)
        } else {
            selected
        },
    ))
}

/// Gives the engine the broker at its fixed number, inheritable across exec,
/// and names it in the engine's environment.
#[cfg(unix)]
pub(crate) fn hand_broker_to(cmd: &mut std::process::Command) {
    use std::os::unix::process::CommandExt;
    let Some(fd) = broker() else {
        return;
    };
    cmd.env(LAUNCH_BROKER_FD_ENV, BROKER_FD.to_string());
    // SAFETY: the closure only calls async-signal-safe dup2 and fcntl.
    unsafe {
        cmd.pre_exec(move || {
            if fd != BROKER_FD && libc::dup2(fd, BROKER_FD) < 0 {
                return Err(std::io::Error::last_os_error());
            }
            let flags = libc::fcntl(BROKER_FD, libc::F_GETFD);
            if flags < 0 || libc::fcntl(BROKER_FD, libc::F_SETFD, flags & !libc::FD_CLOEXEC) < 0 {
                return Err(std::io::Error::last_os_error());
            }
            Ok(())
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn entry_marker_only_tightens_admission_for_missing_launch_facts() {
        use std::ffi::OsStr;
        assert_eq!(selected_launch(None, false, Ok(None)), Ok(None));
        assert_eq!(
            selected_launch(Some(OsStr::new("1")), false, Ok(None)),
            Err(GenerationError::BrokerMismatch)
        );
        assert_eq!(
            selected_launch(
                Some(OsStr::new("1")),
                true,
                Err(GenerationError::FrameUnavailable)
            ),
            Err(GenerationError::FrameUnavailable)
        );
        assert_eq!(
            selected_launch(Some(OsStr::new("forged")), false, Ok(None)),
            Err(GenerationError::InvalidFrame)
        );
        let selected = Some(Generation {
            logical_root: "/opt/painted-wolf-code".into(),
        });
        assert_eq!(
            selected_launch(Some(OsStr::new("1")), true, Ok(selected.clone())),
            Ok(selected)
        );
    }

    #[test]
    fn only_a_root_created_packet_pair_is_the_launch_broker() {
        assert!(is_launch_broker(libc::SOCK_SEQPACKET, 0));
        assert!(!is_launch_broker(libc::SOCK_SEQPACKET, 1000));
        assert!(!is_launch_broker(libc::SOCK_STREAM, 0));
        assert!(!is_launch_broker(libc::SOCK_DGRAM, 0));
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn a_user_created_pair_is_not_claimed() {
        let mut pair = [0; 2];
        // SAFETY: socketpair writes two descriptors into the local array.
        assert_eq!(
            unsafe { libc::socketpair(libc::AF_UNIX, libc::SOCK_SEQPACKET, 0, pair.as_mut_ptr()) },
            0
        );
        let mut kind: libc::c_int = 0;
        let mut length = std::mem::size_of::<libc::c_int>() as libc::socklen_t;
        // SAFETY: reads the socket type into a local.
        unsafe {
            libc::getsockopt(
                pair[0],
                libc::SOL_SOCKET,
                libc::SO_TYPE,
                (&mut kind as *mut libc::c_int).cast(),
                &mut length,
            )
        };
        // SAFETY: getuid has no preconditions.
        let uid = unsafe { libc::getuid() };
        assert_eq!(is_launch_broker(kind, uid), uid == 0);
        // SAFETY: closes descriptors this test opened.
        unsafe {
            libc::close(pair[0]);
            libc::close(pair[1]);
        }
    }

    #[cfg(unix)]
    #[test]
    fn the_engine_learns_the_broker_only_when_one_was_claimed() {
        let mut cmd = std::process::Command::new("/bin/true");
        hand_broker_to(&mut cmd);
        let named = cmd.get_envs().any(|(key, _)| key == LAUNCH_BROKER_FD_ENV);
        assert_eq!(named, broker().is_some());
    }
}
