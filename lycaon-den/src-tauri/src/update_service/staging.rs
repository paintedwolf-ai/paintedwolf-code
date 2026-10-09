//! Verified download staging and the per-installation ready and rejection records.
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
    /// When the release origin last confirmed this offer.
    pub offer_confirmed_at: Option<u64>,
    pub prepared_bundle: PathBuf,
    pub executable_hash: String,
    pub bundle_hash: String,
}
const READY_FILE: &str = "ready.json";
const REJECTED_FILE: &str = "rejected.json";
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
    offer_confirmed_at: Option<u64>,
) -> Result<(), UpdateError> {
    let _lock = lock()?;
    write_ready(&Staged {
        format_version: 2,
        prepared_bundle: super::installer::prepared_path(&super::installer::bundle()?, candidate)?,
        candidate: candidate.clone(),
        verified_at: super::now(),
        offer_confirmed_at,
        executable_hash: identity.executable_hash,
        bundle_hash: identity.bundle_hash,
    })
}
fn write_ready(staged: &Staged) -> Result<(), UpdateError> {
    persistence::write_json_atomic(
        &persistence::update_dir()?,
        READY_FILE,
        "ready.tmp",
        staged,
        Failure::JournalUnavailable,
    )
}
/// Records a fresh confirmation of the staged offer so an offline quit can still install it.
pub fn confirm_offer(release_id: &str, at: u64) -> Result<(), UpdateError> {
    let _lock = lock()?;
    if let Some(mut staged) = read_ready_unlocked()? {
        if staged.candidate.release_id == release_id && staged.offer_confirmed_at != Some(at) {
            staged.offer_confirmed_at = Some(at);
            write_ready(&staged)?;
        }
    }
    Ok(())
}
fn lock() -> Result<fs::File, UpdateError> {
    super::record_lock::acquire(&persistence::update_dir()?, "records.lock")
}
pub fn invalidate_confirmation() -> Result<(), UpdateError> {
    let _lock = lock()?;
    if let Some(mut staged) = read_ready_unlocked()? {
        staged.offer_confirmed_at = None;
        write_ready(&staged)?;
    }
    Ok(())
}
pub fn read_ready() -> Result<Option<Staged>, UpdateError> {
    let _lock = lock()?;
    read_ready_unlocked()
}
#[derive(Clone, Deserialize)]
#[serde(deny_unknown_fields)]
struct LegacyStaged {
    format_version: u8,
    candidate: Candidate,
    verified_at: u64,
    offer_confirmed_at: u64,
    executable_hash: String,
    bundle_hash: String,
}
#[derive(Clone, Deserialize)]
#[serde(untagged)]
enum ReadyRecord {
    Current(Staged),
    Legacy(LegacyStaged),
}
impl ReadyRecord {
    fn convert(self, target: &std::path::Path) -> Result<Staged, UpdateError> {
        let staged = match self {
            Self::Current(staged) => staged,
            Self::Legacy(old) if old.format_version == 1 => Staged {
                format_version: 2,
                prepared_bundle: super::installer::legacy_prepared_path(target, &old.candidate)?,
                candidate: old.candidate,
                verified_at: old.verified_at,
                offer_confirmed_at: Some(old.offer_confirmed_at),
                executable_hash: old.executable_hash,
                bundle_hash: old.bundle_hash,
            },
            Self::Legacy(_) => return Err(Failure::JournalUnavailable.into()),
        };
        if staged.format_version != 2
            || !staged.candidate.valid_identity()
            || !persistence::hex_digest(&staged.executable_hash)
            || !persistence::hex_digest(&staged.bundle_hash)
            || !super::installer::valid_prepared_path(
                target,
                &staged.candidate,
                &staged.prepared_bundle,
            )
        {
            return Err(Failure::JournalUnavailable.into());
        }
        Ok(staged)
    }
}
pub(super) fn read_ready_unlocked() -> Result<Option<Staged>, UpdateError> {
    let path = persistence::update_dir()?.join(READY_FILE);
    if !path
        .try_exists()
        .map_err(|e| UpdateError::new(Failure::JournalUnavailable, e))?
    {
        return Ok(None);
    }
    let target = super::installer::bundle()?;
    let record: Option<ReadyRecord> = persistence::read_or_quarantine(
        &path,
        Failure::JournalUnavailable,
        |record: &ReadyRecord| record.clone().convert(&target).is_ok(),
    )?;
    let Some(record) = record else {
        return Ok(None);
    };
    let legacy = matches!(record, ReadyRecord::Legacy(_));
    let staged = record.convert(&target)?;
    if legacy {
        write_ready(&staged)?;
    }
    Ok(Some(staged))
}
pub fn forget_ready_for(release_id: &str) -> Result<(), UpdateError> {
    let _lock = lock()?;
    if read_ready_unlocked()?.is_some_and(|ready| ready.candidate.release_id == release_id) {
        persistence::remove_if_present(
            &persistence::update_dir()?.join(READY_FILE),
            Failure::JournalUnavailable,
        )?;
    }
    Ok(())
}
pub fn forget_ready() -> Result<(), UpdateError> {
    let _lock = lock()?;
    persistence::remove_if_present(
        &persistence::update_dir()?.join(READY_FILE),
        Failure::JournalUnavailable,
    )
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
    state.offer_confirmed_at = staged.offer_confirmed_at;
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
        if persistence::hex_digest(&name)
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
/// A release whose signed contents failed verification; automatic preparation skips it.
pub fn rejected() -> Result<Option<String>, UpdateError> {
    let _lock = lock()?;
    read_rejected()
}
fn read_rejected() -> Result<Option<String>, UpdateError> {
    Ok(persistence::read_or_quarantine(
        &persistence::update_dir()?.join(REJECTED_FILE),
        Failure::JournalUnavailable,
        |rejected: &Rejected| {
            rejected.format_version == 1 && persistence::hex_digest(&rejected.release_id)
        },
    )?
    .map(|rejected| rejected.release_id))
}
pub fn reject(release_id: String) -> Result<(), UpdateError> {
    let _lock = lock()?;
    persistence::write_json_atomic(
        &persistence::update_dir()?,
        REJECTED_FILE,
        "rejected.tmp",
        &Rejected {
            format_version: 1,
            release_id,
        },
        Failure::JournalUnavailable,
    )
}
pub fn clear_rejected(release_id: &str) -> Result<(), UpdateError> {
    let _lock = lock()?;
    if read_rejected()?.as_deref() == Some(release_id) {
        persistence::remove_if_present(
            &persistence::update_dir()?.join(REJECTED_FILE),
            Failure::JournalUnavailable,
        )?;
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

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn released_ready_record_retains_its_prepared_path_and_confirmation() {
        let root = crate::test_support::TempDir::new("update-ready-migration");
        let target = root.join("App.app");
        let record: ReadyRecord =
            serde_json::from_str(include_str!("fixtures/ready-v1.json")).unwrap();
        let mut staged = record.convert(&target).unwrap();
        assert_eq!(staged.format_version, 2);
        assert_eq!(staged.offer_confirmed_at, Some(20));
        assert_eq!(
            staged.prepared_bundle,
            super::super::installer::legacy_prepared_path(&target, &staged.candidate).unwrap()
        );
        staged.offer_confirmed_at = None;
        let reloaded: ReadyRecord =
            serde_json::from_value(serde_json::to_value(staged).unwrap()).unwrap();
        assert_eq!(reloaded.convert(&target).unwrap().offer_confirmed_at, None);
    }
}
