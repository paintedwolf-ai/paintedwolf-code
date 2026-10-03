use super::{
    persistence, Candidate, Discovery, Failure, Installation, NativeUpdateState, UpdateError,
};
use serde::{Deserialize, Serialize};
use std::{fs, path::PathBuf};
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Staged {
    pub format_version: u8,
    pub candidate: Candidate,
    pub verified_at: u64,
    pub executable_hash: String,
    pub bundle_hash: String,
}
pub fn root(candidate: &Candidate) -> Result<PathBuf, UpdateError> {
    if !candidate.valid_identity() {
        return Err(Failure::InvalidRelease.into());
    }
    Ok(persistence::update_dir()?
        .join("staging")
        .join(&candidate.release_id))
}
pub fn publish(
    candidate: &Candidate,
    identity: super::installer::PreparedIdentity,
) -> Result<(), UpdateError> {
    read_ready()?;
    let staged = Staged {
        format_version: 1,
        candidate: candidate.clone(),
        verified_at: super::now(),
        executable_hash: identity.executable_hash,
        bundle_hash: identity.bundle_hash,
    };
    persistence::write_json_atomic(
        &persistence::update_dir()?,
        "ready.json",
        "ready.tmp",
        &staged,
        Failure::JournalUnavailable,
    )
}
pub fn read_ready() -> Result<Option<Staged>, UpdateError> {
    let path = persistence::update_dir()?.join("ready.json");
    let bytes = match fs::read(path) {
        Ok(b) => b,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(UpdateError::new(Failure::JournalUnavailable, e)),
    };
    let staged: Staged = serde_json::from_slice(&bytes)
        .map_err(|e| UpdateError::new(Failure::JournalUnavailable, e))?;
    if staged.bundle_hash.len() != 64
        || !staged.bundle_hash.bytes().all(|b| b.is_ascii_hexdigit())
        || staged.format_version != 1
        || !staged.candidate.valid_identity()
        || staged.executable_hash.len() != 64
        || !staged
            .executable_hash
            .bytes()
            .all(|b| b.is_ascii_hexdigit())
    {
        return Err(Failure::JournalUnavailable.into());
    }
    Ok(Some(staged))
}
pub fn forget_ready() -> Result<(), UpdateError> {
    read_ready()?;
    match fs::remove_file(persistence::update_dir()?.join("ready.json")) {
        Ok(()) => Ok(()),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(e) => Err(UpdateError::new(Failure::JournalUnavailable, e)),
    }
}
pub fn restore(state: &mut NativeUpdateState) -> Result<(), UpdateError> {
    let Some(staged) = read_ready()? else {
        return Ok(());
    };
    let version = semver::Version::parse(&staged.candidate.version)
        .map_err(|e| UpdateError::new(Failure::InvalidRelease, e))?;
    let running = semver::Version::parse(&state.running_version)
        .map_err(|e| UpdateError::new(Failure::InvalidVersion, e))?;
    if version <= running || staged.candidate.channel != state.channel {
        forget_ready()?;
        return Ok(());
    }
    // Activation validates the prepared executable against this authenticated receipt.
    state.staged_release_id = Some(staged.candidate.release_id.clone());
    state.candidate = Some(staged.candidate);
    state.discovery = Discovery::Available;
    state.installation = Installation::Staged;
    Ok(())
}
pub fn cleanup(keep: &[String]) -> Result<(), UpdateError> {
    let dir = persistence::update_dir()?.join("staging");
    let entries = match fs::read_dir(dir) {
        Ok(e) => e,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(e) => return Err(UpdateError::new(Failure::StateUnavailable, e)),
    };
    for entry in entries {
        let entry = entry.map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        let name = entry.file_name().to_string_lossy().into_owned();
        if name.len() == 64
            && name.bytes().all(|b| b.is_ascii_hexdigit())
            && !keep.contains(&name)
            && entry.file_type().is_ok_and(|t| t.is_dir())
        {
            fs::remove_dir_all(entry.path())
                .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        }
    }
    Ok(())
}

#[derive(Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct Rejected {
    format_version: u8,
    release_id: String,
}
pub fn rejected() -> Result<Option<String>, UpdateError> {
    let bytes = match fs::read(persistence::update_dir()?.join("rejected.json")) {
        Ok(bytes) => bytes,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(UpdateError::new(Failure::JournalUnavailable, e)),
    };
    let rejected: Rejected = serde_json::from_slice(&bytes)
        .map_err(|e| UpdateError::new(Failure::JournalUnavailable, e))?;
    if rejected.format_version != 1
        || rejected.release_id.len() != 64
        || !rejected.release_id.bytes().all(|b| b.is_ascii_hexdigit())
    {
        return Err(Failure::JournalUnavailable.into());
    }
    Ok(Some(rejected.release_id))
}
pub fn reject(release_id: String) -> Result<(), UpdateError> {
    rejected()?;
    persistence::write_json_atomic(
        &persistence::update_dir()?,
        "rejected.json",
        "rejected.tmp",
        &Rejected {
            format_version: 1,
            release_id,
        },
        Failure::JournalUnavailable,
    )
}
pub fn clear_rejected(release_id: &str) -> Result<(), UpdateError> {
    if rejected()?.as_deref() == Some(release_id) {
        fs::remove_file(persistence::update_dir()?.join("rejected.json"))
            .map_err(|e| UpdateError::new(Failure::JournalUnavailable, e))?;
    }
    Ok(())
}
pub fn clean_partials(candidate: &Candidate) -> Result<(), UpdateError> {
    let entries = fs::read_dir(root(candidate)?)
        .map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
    for entry in entries {
        let entry = entry.map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        let path = entry.path();
        if path.extension().and_then(|s| s.to_str()) == Some("partial")
            && path
                .file_stem()
                .and_then(|s| s.to_str())
                .is_some_and(|s| uuid::Uuid::parse_str(s).is_ok())
        {
            fs::remove_file(path).map_err(|e| UpdateError::new(Failure::StateUnavailable, e))?;
        }
    }
    Ok(())
}
