use super::check::ROLLOUT_BUCKETS;
use super::{
    Failure, InstallSource, InstallSourceReceipt, InstallSourceReceiptKind, NativeUpdateState,
    RolloutEligibility, UpdateChannel, UpdateError, UpdateJournal, UpdateJournalPhase, UpdatePhase,
    UpdatePreferences, INSTALL_SOURCE_FILE, JOURNAL_FILE, JOURNAL_TMP, PREFERENCES_FILE,
    PREFERENCES_TMP,
};
use serde::Serialize;
use std::fs;
use std::io::Write;
use std::path::Path;

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
    let tmp = dir.join(temp_name);
    let result = (|| {
        let raw = serde_json::to_vec_pretty(value).map_err(|err| UpdateError::new(failure, err))?;
        let mut file = fs::File::create(&tmp).map_err(|err| UpdateError::new(failure, err))?;
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

pub(super) fn write_journal(dir: &Path, journal: &UpdateJournal) -> Result<(), UpdateError> {
    write_json_atomic(
        dir,
        JOURNAL_FILE,
        JOURNAL_TMP,
        journal,
        Failure::JournalUnavailable,
    )
}

pub(super) fn load_preferences(
    dir: &Path,
    default_channel: UpdateChannel,
    default_rollout_bucket: u8,
) -> Result<UpdatePreferences, UpdateError> {
    let raw = match fs::read(dir.join(PREFERENCES_FILE)) {
        Ok(raw) => raw,
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => {
            let preferences = UpdatePreferences::new(default_channel, default_rollout_bucket);
            write_preferences(dir, &preferences)?;
            return Ok(preferences);
        }
        Err(err) => return Err(UpdateError::new(Failure::PreferencesUnavailable, err)),
    };
    let preferences: UpdatePreferences = serde_json::from_slice(&raw)
        .map_err(|err| UpdateError::new(Failure::PreferencesUnavailable, err))?;
    if preferences.rollout_bucket >= ROLLOUT_BUCKETS {
        return Err(UpdateError::from(Failure::PreferencesUnavailable));
    }
    Ok(preferences)
}

pub(super) fn write_preferences(
    dir: &Path,
    preferences: &UpdatePreferences,
) -> Result<(), UpdateError> {
    write_json_atomic(
        dir,
        PREFERENCES_FILE,
        PREFERENCES_TMP,
        preferences,
        Failure::PreferencesUnavailable,
    )
}

pub(super) fn target_version_is_running(target: &str, current: &str) -> Result<bool, UpdateError> {
    let target = semver::Version::parse(target)
        .map_err(|err| UpdateError::new(Failure::JournalUnavailable, err))?;
    let current = semver::Version::parse(current)
        .map_err(|err| UpdateError::new(Failure::InvalidVersion, err))?;
    Ok(current >= target)
}

pub(super) fn reconcile_journal(
    dir: &Path,
    current_version: &str,
    state: &mut NativeUpdateState,
) -> Result<(), UpdateError> {
    let path = dir.join(JOURNAL_FILE);
    let raw = match fs::read(&path) {
        Ok(raw) => raw,
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(err) => return Err(UpdateError::new(Failure::JournalUnavailable, err)),
    };
    let journal: UpdateJournal = serde_json::from_slice(&raw)
        .map_err(|err| UpdateError::new(Failure::JournalUnavailable, err))?;
    if target_version_is_running(&journal.to_version, current_version)? {
        fs::remove_file(path).map_err(|err| UpdateError::new(Failure::JournalUnavailable, err))?;
        let checks_enabled = state.checks_enabled;
        let channel = state.channel;
        *state =
            NativeUpdateState::idle(current_version.to_string(), state.install_source, channel);
        state.checks_enabled = checks_enabled;
        return Ok(());
    }
    match journal.phase {
        UpdateJournalPhase::Installing => {
            fs::remove_file(path)
                .map_err(|err| UpdateError::new(Failure::JournalUnavailable, err))?;
            let checks_enabled = state.checks_enabled;
            let channel = state.channel;
            *state =
                NativeUpdateState::idle(current_version.to_string(), state.install_source, channel);
            state.checks_enabled = checks_enabled;
            state.error = Some(UpdateError::from(Failure::Interrupted));
            return Ok(());
        }
        UpdateJournalPhase::RestartRequired => {}
    }
    state.phase = UpdatePhase::RestartRequired;
    state.current_version = current_version.to_string();
    state.available_version = Some(journal.to_version);
    state.notes = None;
    state.error = None;
    state.rollout_eligibility = RolloutEligibility::Eligible;
    Ok(())
}
