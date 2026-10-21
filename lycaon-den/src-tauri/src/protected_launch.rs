//! The protected Linux launch hands the desktop two descriptors: the broker
//! that starts programs outside the protected namespace (6) and the selected
//! generation's lifetime lock (7). The desktop claims them before anything can
//! inherit them, keeps the lock for its lifetime, and passes the broker only
//! to the engine it starts.

use std::sync::OnceLock;

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
pub(crate) fn claim() {
    BROKER.get_or_init(|| {
        #[cfg(target_os = "linux")]
        {
            claim_linux()
        }
        #[cfg(not(target_os = "linux"))]
        {
            None
        }
    });
}

/// The claimed broker, when this desktop runs under the protected launch.
pub(crate) fn broker() -> Option<i32> {
    BROKER.get().copied().flatten()
}

#[cfg(target_os = "linux")]
fn claim_linux() -> Option<i32> {
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
            if flags >= 0 {
                libc::fcntl(fd, libc::F_SETFD, flags | libc::FD_CLOEXEC);
            }
        }
    }
    Some(BROKER_FD)
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
