//! Consumes the launcher's sealed selection, never a path supplied by the caller.
use serde::Deserialize;
use std::path::{Component, Path, PathBuf};

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Generation {
    pub(crate) logical_root: PathBuf,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum GenerationError {
    FrameUnavailable,
    InvalidFrame,
    IdentityMismatch,
    BrokerMismatch,
    LayoutUnavailable,
}

impl std::fmt::Display for GenerationError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let code = match self {
            Self::FrameUnavailable => "frame_unavailable",
            Self::InvalidFrame => "invalid_frame",
            Self::IdentityMismatch => "identity_mismatch",
            Self::BrokerMismatch => "broker_mismatch",
            Self::LayoutUnavailable => "layout_unavailable",
        };
        write!(f, "Protected launch generation refused ({code})")
    }
}
impl std::error::Error for GenerationError {}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Entry {
    version: u32,
    supervisor_pid: i32,
    uid: u32,
    gid: u32,
    groups: Option<Vec<u32>>,
    generation: Policy,
    image: Image,
    #[serde(rename = "arguments")]
    _arguments: Option<Vec<String>>,
    #[serde(rename = "environment")]
    _environment: Option<Vec<String>>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Policy {
    version: u32,
    generation_root: String,
    logical_install_root: String,
    manifest_sha256: String,
    #[serde(default)]
    system_executables: Vec<String>,
    #[serde(default, deserialize_with = "present_roots")]
    library_roots: Option<Vec<String>>,
}

fn present_roots<'de, D: serde::Deserializer<'de>>(
    deserializer: D,
) -> Result<Option<Vec<String>>, D::Error> {
    Vec::<String>::deserialize(deserializer).map(Some)
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Image {
    path: String,
    sha256: String,
    size: u64,
}

fn absolute_root(raw: &str) -> bool {
    let path = Path::new(raw);
    path.is_absolute()
        && raw != "/"
        && !raw.as_bytes().contains(&0)
        && path
            .components()
            .all(|part| matches!(part, Component::RootDir | Component::Normal(_)))
        && path.components().collect::<PathBuf>().as_os_str() == raw
}

fn digest(raw: &str) -> bool {
    raw.len() == 64
        && raw
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

fn decode(
    raw: &[u8],
    peer_pid: i32,
    uid: u32,
    gid: u32,
    groups: &[u32],
) -> Result<Generation, GenerationError> {
    let entry: Entry = serde_json::from_slice(raw).map_err(|_| GenerationError::InvalidFrame)?;
    let policy = entry.generation;
    if entry.version != 1
        || policy.version != 1
        || entry.supervisor_pid <= 1
        || !absolute_root(&policy.generation_root)
        || !absolute_root(&policy.logical_install_root)
        || !policy
            .system_executables
            .iter()
            .all(|root| absolute_root(root))
        || policy
            .library_roots
            .as_ref()
            .is_some_and(|roots| roots.is_empty() || !roots.iter().all(|root| absolute_root(root)))
        || !digest(&policy.manifest_sha256)
        || !digest(&entry.image.sha256)
        || entry.image.size == 0
        || Path::new(&entry.image.path)
            != Path::new(&policy.generation_root).join("bin/painted-wolf-code")
    {
        return Err(GenerationError::InvalidFrame);
    }
    if entry.supervisor_pid != peer_pid {
        return Err(GenerationError::BrokerMismatch);
    }
    if uid == 0
        || entry.uid != uid
        || entry.gid != gid
        || entry.groups.as_deref().unwrap_or_default() != groups
    {
        return Err(GenerationError::IdentityMismatch);
    }
    Ok(Generation {
        logical_root: policy.logical_install_root.into(),
    })
}

#[cfg(target_os = "linux")]
pub(super) fn read(frame_fd: i32, broker_fd: i32) -> Result<Generation, GenerationError> {
    use std::fs::File;
    use std::os::fd::FromRawFd;
    use std::os::unix::fs::{FileExt, MetadataExt};
    // SAFETY: startup claims this fixed descriptor before any threads exist.
    // File ownership closes it on every return; it cannot reach an engine child.
    let owned = unsafe { libc::fcntl(frame_fd, libc::F_DUPFD_CLOEXEC, 8) };
    if owned < 0 {
        return Err(GenerationError::FrameUnavailable);
    }
    // SAFETY: duplicate succeeded, and the original is a claimed launch descriptor.
    let file = unsafe {
        libc::close(frame_fd);
        File::from_raw_fd(owned)
    };
    let metadata = file
        .metadata()
        .map_err(|_| GenerationError::FrameUnavailable)?;
    const LIMIT: u64 = 128 << 10;
    const SEALS: i32 =
        libc::F_SEAL_WRITE | libc::F_SEAL_GROW | libc::F_SEAL_SHRINK | libc::F_SEAL_SEAL;
    // SAFETY: these calls inspect only the owned descriptor or fill local values.
    unsafe {
        let seals = libc::fcntl(owned, libc::F_GET_SEALS);
        if seals < 0
            || !metadata.is_file()
            || metadata.uid() != 0
            || metadata.mode() & 0o222 != 0
            || metadata.len() == 0
            || metadata.len() > LIMIT
            || seals & SEALS != SEALS
        {
            return Err(GenerationError::InvalidFrame);
        }
        let mut peer: libc::ucred = std::mem::zeroed();
        let mut len = std::mem::size_of_val(&peer) as libc::socklen_t;
        if libc::getsockopt(
            broker_fd,
            libc::SOL_SOCKET,
            libc::SO_PEERCRED,
            (&mut peer as *mut libc::ucred).cast(),
            &mut len,
        ) != 0
            || peer.uid != 0
        {
            return Err(GenerationError::BrokerMismatch);
        }
        let (mut real_uid, mut effective_uid, mut saved_uid) = (0, 0, 0);
        let (mut real_gid, mut effective_gid, mut saved_gid) = (0, 0, 0);
        if libc::getresuid(&mut real_uid, &mut effective_uid, &mut saved_uid) != 0
            || libc::getresgid(&mut real_gid, &mut effective_gid, &mut saved_gid) != 0
            || real_uid != effective_uid
            || real_uid != saved_uid
            || real_gid != effective_gid
            || real_gid != saved_gid
        {
            return Err(GenerationError::IdentityMismatch);
        }
        let count = libc::getgroups(0, std::ptr::null_mut());
        if count < 0 {
            return Err(GenerationError::IdentityMismatch);
        }
        let mut groups = vec![0; count as usize];
        if libc::getgroups(count, groups.as_mut_ptr()) != count {
            return Err(GenerationError::IdentityMismatch);
        }
        let mut raw = vec![0; metadata.len() as usize];
        file.read_exact_at(&mut raw, 0)
            .map_err(|_| GenerationError::InvalidFrame)?;
        decode(&raw, peer.pid, real_uid, real_gid, &groups)
    }
}

#[cfg(test)]
mod tests;
