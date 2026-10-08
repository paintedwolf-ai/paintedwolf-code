//! Device records: preferences, feed freshness, and the per-installation namespace.
use super::{Failure, InstallSource, UpdateChannel, UpdateError};
use serde::{de::DeserializeOwned, Deserialize, Serialize};
use std::{
    collections::BTreeMap,
    fs,
    io::Write,
    path::{Path, PathBuf},
};
const INSTALL_SOURCE_FILE: &str = "install-source.json";
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct InstallSourceReceipt {
    install_source: InstallSourceReceiptKind,
    release_channel: UpdateChannel,
}
#[derive(Deserialize)]
#[serde(rename_all = "snake_case")]
enum InstallSourceReceiptKind {
    HomebrewCask,
}
pub(super) fn read_install_source(
    dir: &Path,
) -> Result<(InstallSource, Option<UpdateChannel>), UpdateError> {
    let raw = match fs::read(dir.join(INSTALL_SOURCE_FILE)) {
        Ok(raw) => raw,
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => {
            return Ok((InstallSource::DirectDownload, None));
        }
        Err(err) => return Err(UpdateError::new(Failure::InstallSourceUnavailable, err)),
    };
    let receipt: InstallSourceReceipt = serde_json::from_slice(&raw)
        .map_err(|err| UpdateError::new(Failure::InstallSourceUnavailable, err))?;
    match receipt.install_source {
        InstallSourceReceiptKind::HomebrewCask => {
            Ok((InstallSource::HomebrewCask, Some(receipt.release_channel)))
        }
    }
}

pub(super) fn detect_install_source() -> (InstallSource, Option<UpdateChannel>, Option<UpdateError>)
{
    let Some(dir) = crate::den_state_dir() else {
        return (
            InstallSource::Unknown,
            None,
            Some(UpdateError::from(Failure::StateUnavailable)),
        );
    };
    match read_install_source(&dir) {
        Ok((source, channel)) => (source, channel, None),
        Err(err) => (InstallSource::Unknown, None, Some(err)),
    }
}

pub(super) fn write_json_atomic<T: Serialize>(
    dir: &Path,
    file_name: &str,
    temp_name: &str,
    value: &T,
    failure: Failure,
) -> Result<(), UpdateError> {
    crate::config_dir::ensure_private_dir(dir).map_err(|err| UpdateError::new(failure, err))?;
    let path = dir.join(file_name);
    let tmp = dir.join(format!("{temp_name}-{}", uuid::Uuid::new_v4()));
    let result = (|| {
        let raw = serde_json::to_vec_pretty(value).map_err(|err| UpdateError::new(failure, err))?;
        let mut file = fs::OpenOptions::new()
            .create_new(true)
            .write(true)
            .open(&tmp)
            .map_err(|err| UpdateError::new(failure, err))?;
        file.write_all(&raw)
            .map_err(|err| UpdateError::new(failure, err))?;
        file.sync_all()
            .map_err(|err| UpdateError::new(failure, err))?;
        drop(file);
        crate::atomic_file::replace(&tmp, &path, true).map_err(|err| UpdateError::new(failure, err))
    })();
    if result.is_err() {
        let _ = fs::remove_file(tmp);
    }
    result
}

/// Reads a JSON record; a missing file is `None` and an unreadable or unknown shape is refused unchanged.
pub(super) fn read_json<T: DeserializeOwned>(
    path: &Path,
    failure: Failure,
    valid: impl FnOnce(&T) -> bool,
) -> Result<Option<T>, UpdateError> {
    let raw = match fs::read(path) {
        Ok(raw) => raw,
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(err) => return Err(UpdateError::new(failure, err)),
    };
    let value: T = serde_json::from_slice(&raw).map_err(|err| UpdateError::new(failure, err))?;
    if !valid(&value) {
        return Err(UpdateError::new(
            failure,
            format!("Record has an unsupported shape: {}", path.display()),
        ));
    }
    Ok(Some(value))
}

/// Reads an ephemeral record, moving an unreadable one aside so the service rebuilds it.
pub(super) fn read_or_quarantine<T: DeserializeOwned>(
    path: &Path,
    failure: Failure,
    valid: impl FnOnce(&T) -> bool,
) -> Result<Option<T>, UpdateError> {
    match read_json(path, failure, valid) {
        Ok(value) => Ok(value),
        Err(error) => {
            let stem = path
                .file_stem()
                .and_then(|stem| stem.to_str())
                .unwrap_or("record");
            let quarantine =
                path.with_file_name(format!("{stem}-quarantined-{}.json", uuid::Uuid::new_v4()));
            crate::atomic_file::replace(path, &quarantine, true).map_err(|moved| {
                error.with_context(format!(
                    "Record: {}. Could not preserve the incompatible record: {moved}",
                    path.display()
                ))
            })?;
            Ok(None)
        }
    }
}

pub(super) fn remove_if_present(path: &Path, failure: Failure) -> Result<(), UpdateError> {
    match fs::remove_file(path) {
        Ok(()) => Ok(()),
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(err) => Err(UpdateError::new(failure, err)),
    }
}

pub(super) fn hex_digest(value: &str) -> bool {
    value.len() == 64 && value.bytes().all(|byte| byte.is_ascii_hexdigit())
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Preferences {
    pub format_version: u8,
    pub automatic_updates_enabled: bool,
    pub channel: UpdateChannel,
    pub rollout_bucket: u8,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct LegacyPreferences {
    checks_enabled: bool,
    channel: UpdateChannel,
    rollout_bucket: u8,
}
/// Device-wide update records shared by every installation.
pub fn preferences_dir() -> Result<PathBuf, UpdateError> {
    crate::den_state_dir()
        .map(|p| p.join("updates"))
        .ok_or_else(|| Failure::StateUnavailable.into())
}
static HELPER_INSTALLATION: std::sync::OnceLock<String> = std::sync::OnceLock::new();
pub fn installation_id(target: &Path) -> String {
    use sha2::{Digest, Sha256};
    hex::encode(Sha256::digest(target.as_os_str().as_encoded_bytes()))
}
pub fn helper_installation(id: &str) -> Result<(), UpdateError> {
    if !hex_digest(id) {
        return Err(Failure::InvalidTransition.into());
    }
    HELPER_INSTALLATION
        .set(id.into())
        .map_err(|_| Failure::InvalidTransition.into())
}
/// Records that belong to one installed application path.
pub fn update_dir() -> Result<PathBuf, UpdateError> {
    let id = match HELPER_INSTALLATION.get() {
        Some(id) => id.clone(),
        None => installation_id(&super::installer::bundle().or_else(|_| {
            std::env::current_exe().map_err(|e| UpdateError::new(Failure::StateUnavailable, e))
        })?),
    };
    Ok(preferences_dir()?.join("installations").join(id))
}

pub fn write_preferences(dir: &Path, preferences: &Preferences) -> Result<(), UpdateError> {
    write_json_atomic(
        dir,
        "preferences.json",
        "preferences.tmp",
        preferences,
        Failure::PreferencesUnavailable,
    )
}
pub fn load_preferences(
    dir: &Path,
    channel: UpdateChannel,
    bucket: u8,
) -> Result<Preferences, UpdateError> {
    let path = dir.join("preferences.json");
    let preferences = match fs::read(&path) {
        Ok(raw) => serde_json::from_slice::<Preferences>(&raw)
            .map_err(|e| UpdateError::new(Failure::PreferencesUnavailable, e))?,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            let legacy = dir
                .parent()
                .ok_or(Failure::StateUnavailable)?
                .join("update-preferences.json");
            let value = match fs::read(&legacy) {
                Ok(raw) => {
                    let old: LegacyPreferences = serde_json::from_slice(&raw)
                        .map_err(|e| UpdateError::new(Failure::PreferencesUnavailable, e))?;
                    Preferences {
                        format_version: 2,
                        automatic_updates_enabled: old.checks_enabled,
                        channel: old.channel,
                        rollout_bucket: old.rollout_bucket,
                    }
                }
                Err(e) if e.kind() == std::io::ErrorKind::NotFound => Preferences {
                    format_version: 2,
                    automatic_updates_enabled: true,
                    channel,
                    rollout_bucket: bucket,
                },
                Err(e) => return Err(UpdateError::new(Failure::PreferencesUnavailable, e)),
            };
            validate_preferences(&value)?;
            write_preferences(dir, &value)?;
            if legacy.exists() {
                fs::remove_file(legacy)
                    .map_err(|e| UpdateError::new(Failure::PreferencesUnavailable, e))?;
            }
            value
        }
        Err(e) => return Err(UpdateError::new(Failure::PreferencesUnavailable, e)),
    };
    validate_preferences(&preferences)?;
    Ok(preferences)
}
fn validate_preferences(p: &Preferences) -> Result<(), UpdateError> {
    if p.format_version != 2 || p.rollout_bucket >= 100 {
        return Err(Failure::PreferencesUnavailable.into());
    }
    Ok(())
}

/// The newest signed pointer timestamp accepted per feed; an older pointer is a replay.
#[derive(Debug, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
struct FeedState {
    format_version: u8,
    feeds: BTreeMap<String, u64>,
}
const FEED_STATE_FILE: &str = "feed-state.json";
fn read_feed_state(dir: &Path) -> Result<FeedState, UpdateError> {
    Ok(read_or_quarantine(
        &dir.join(FEED_STATE_FILE),
        Failure::JournalUnavailable,
        |state: &FeedState| state.format_version == 1,
    )?
    .unwrap_or(FeedState {
        format_version: 1,
        feeds: BTreeMap::new(),
    }))
}
/// Accepts a pointer timestamp for a feed unless a newer one was already accepted.
pub(super) fn accept_feed_timestamp(
    dir: &Path,
    feed: &str,
    timestamp: u64,
) -> Result<(), UpdateError> {
    let mut state = read_feed_state(dir)?;
    match state.feeds.get(feed) {
        Some(&newest) if newest > timestamp => Err(UpdateError::new(
            Failure::InvalidRelease,
            "The update feed is older than one this device already accepted",
        )),
        Some(&newest) if newest == timestamp => Ok(()),
        _ => {
            state.feeds.insert(feed.into(), timestamp);
            write_json_atomic(
                dir,
                FEED_STATE_FILE,
                "feed-state.tmp",
                &state,
                Failure::JournalUnavailable,
            )
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_support::TempDir;
    #[test]
    fn installation_namespaces_are_path_bound_and_stable() {
        let a = Path::new("/Applications/Painted Wolf Code.app");
        let b = Path::new("/Users/person/Applications/Painted Wolf Code.app");
        assert_eq!(installation_id(a), installation_id(a));
        assert_ne!(installation_id(a), installation_id(b));
        assert_eq!(installation_id(a).len(), 64);
    }
    #[test]
    fn released_preferences_migrate_without_changing_choices() {
        for enabled in [false, true] {
            let root = TempDir::new("update-preferences-migration");
            fs::write(
                root.join("update-preferences.json"),
                format!(
                    r#"{{"checks_enabled":{enabled},"channel":"preview","rollout_bucket":79}}"#
                ),
            )
            .unwrap();
            let dir = root.join("updates");
            let p = load_preferences(&dir, UpdateChannel::Stable, 1).unwrap();
            assert_eq!(p.automatic_updates_enabled, enabled);
            assert_eq!(p.channel, UpdateChannel::Preview);
            assert_eq!(p.rollout_bucket, 79);
            assert!(!root.join("update-preferences.json").exists());
            assert_eq!(
                load_preferences(&dir, UpdateChannel::Stable, 2)
                    .unwrap()
                    .rollout_bucket,
                79
            );
        }
    }
    #[test]
    fn unknown_preferences_are_not_rewritten() {
        let root = TempDir::new("update-preferences-unknown");
        let bytes = br#"{"format_version":99,"automatic_updates_enabled":true,"channel":"stable","rollout_bucket":0}"#;
        fs::write(root.join("preferences.json"), bytes).unwrap();
        assert!(load_preferences(&root, UpdateChannel::Stable, 0).is_err());
        assert_eq!(fs::read(root.join("preferences.json")).unwrap(), bytes);
    }
    #[test]
    fn feed_timestamps_only_advance_and_a_replayed_pointer_is_refused() {
        let root = TempDir::new("update-feed-state");
        accept_feed_timestamp(&root, "latest-stable-key-1.json", 100).unwrap();
        accept_feed_timestamp(&root, "latest-stable-key-1.json", 100).unwrap();
        accept_feed_timestamp(&root, "latest-stable-key-1.json", 150).unwrap();
        let replay = accept_feed_timestamp(&root, "latest-stable-key-1.json", 120).unwrap_err();
        assert_eq!(replay.code, Failure::InvalidRelease);
        accept_feed_timestamp(&root, "latest-preview-key-1.json", 5).unwrap();
        assert_eq!(
            read_feed_state(&root).unwrap().feeds["latest-stable-key-1.json"],
            150
        );
    }
    #[test]
    fn unreadable_ephemeral_records_are_quarantined_and_rebuilt() {
        let root = TempDir::new("update-record-quarantine");
        let path = root.join(FEED_STATE_FILE);
        for bytes in [
            b"not json".as_slice(),
            br#"{"format_version":99,"feeds":{}}"#,
        ] {
            fs::write(&path, bytes).unwrap();
            assert!(read_json(&path, Failure::JournalUnavailable, |state: &FeedState| {
                state.format_version == 1
            })
            .is_err());
            assert!(read_feed_state(&root).unwrap().feeds.is_empty());
            assert!(!path.exists());
            assert!(fs::read_dir(&*root)
                .unwrap()
                .filter_map(Result::ok)
                .any(|entry| fs::read(entry.path()).is_ok_and(|saved| saved == bytes)));
        }
    }
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct LegacyJournal {
    to_version: String,
    phase: LegacyPhase,
}
#[derive(Deserialize)]
#[serde(rename_all = "snake_case")]
enum LegacyPhase {
    Installing,
    RestartRequired,
}
pub fn reconcile_legacy(dir: &Path, running: &str) -> Result<(), UpdateError> {
    let path = dir
        .parent()
        .ok_or(Failure::StateUnavailable)?
        .join("update-state.json");
    let raw = match fs::read(&path) {
        Ok(raw) => raw,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(e) => return Err(UpdateError::new(Failure::JournalUnavailable, e)),
    };
    let journal: LegacyJournal = serde_json::from_slice(&raw)
        .map_err(|e| UpdateError::new(Failure::JournalUnavailable, e))?;
    let target = semver::Version::parse(&journal.to_version)
        .map_err(|e| UpdateError::new(Failure::JournalUnavailable, e))?;
    let running = semver::Version::parse(running)
        .map_err(|e| UpdateError::new(Failure::InvalidVersion, e))?;
    if running < target {
        return Err(match journal.phase {
            LegacyPhase::Installing | LegacyPhase::RestartRequired => Failure::Interrupted.into(),
        });
    }
    fs::remove_file(path).map_err(|e| UpdateError::new(Failure::JournalUnavailable, e))
}
#[cfg(test)]
mod legacy_tests {
    use super::*;
    #[test]
    fn released_journal_only_completes_when_target_runs() {
        let root = crate::test_support::TempDir::new("legacy-update");
        let dir = root.join("updates");
        for phase in ["installing", "restart_required"] {
            let raw = format!(r#"{{"to_version":"1.1.0","phase":"{phase}"}}"#);
            fs::write(root.join("update-state.json"), &raw).unwrap();
            assert!(reconcile_legacy(&dir, "1.0.1").is_err());
            assert_eq!(
                fs::read_to_string(root.join("update-state.json")).unwrap(),
                raw
            );
            reconcile_legacy(&dir, "1.1.1").unwrap();
            assert!(!root.join("update-state.json").exists());
        }
    }
    #[test]
    fn unrecognized_legacy_journal_survives_refusal() {
        let root = crate::test_support::TempDir::new("legacy-update-unknown");
        let path = root.join("update-state.json");
        fs::write(&path, r#"{"to_version":"1.0.0","phase":"unknown"}"#).unwrap();
        assert!(reconcile_legacy(&root.join("updates"), "2.0.0").is_err());
        assert!(path.exists());
    }
}
