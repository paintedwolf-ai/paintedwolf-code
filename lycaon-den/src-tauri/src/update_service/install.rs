use super::check::fetch_candidate;
use super::persistence::{detect_install_source, write_journal};
use super::{
    emit_update_state, install_transition_allowed, Failure, InstallSource, NativeUpdateState,
    UpdateError, UpdateJournal, UpdateJournalPhase, UpdatePhase, UpdateService, JOURNAL_FILE,
};
use std::fs;
use tauri::AppHandle;
use tauri_plugin_updater::Update;

pub(super) fn same_candidate(displayed: &Update, fresh: &Update) -> bool {
    displayed.version == fresh.version
        && displayed.target == fresh.target
        && displayed.download_url == fresh.download_url
        && displayed.signature == fresh.signature
}

#[tauri::command]
pub async fn install_update(
    app: AppHandle,
    service: tauri::State<'_, UpdateService>,
    expected_version: String,
) -> Result<NativeUpdateState, UpdateError> {
    let _operation = service.operation.lock().await;
    let dir = crate::den_state_dir().ok_or_else(|| UpdateError::from(Failure::StateUnavailable))?;
    let (displayed, ticket) = {
        let mut inner = service.inner.lock().await;
        if inner.state.phase == UpdatePhase::RestartRequired
            && inner.state.available_version.as_deref() == Some(expected_version.as_str())
        {
            let state = inner.state.clone();
            drop(inner);
            crate::app_exit::prepare_install(&app)
                .await
                .map_err(|error| UpdateError::new(Failure::StateUnavailable, error))?
                .restart()
                .map_err(|error| UpdateError::new(Failure::StateUnavailable, error))?;
            return Ok(state);
        }
        if !install_transition_allowed(inner.state.phase) {
            return Err(UpdateError::from(Failure::InvalidTransition));
        }
        let (source, _, source_error) = detect_install_source();
        if let Some(error) = source_error {
            return Err(error);
        }
        if source == InstallSource::HomebrewCask {
            return Err(UpdateError::from(Failure::PackageManaged));
        }
        let candidate = inner
            .candidate
            .clone()
            .ok_or_else(|| UpdateError::from(Failure::CandidateMissing))?;
        if candidate.version != expected_version {
            return Err(UpdateError::from(Failure::CandidateChanged));
        }
        let ticket = inner
            .begin_check(false)?
            .ok_or_else(|| UpdateError::from(Failure::InvalidTransition))?;
        emit_update_state(&app, &mut inner.state);
        (candidate, ticket)
    };
    let checked = fetch_candidate(
        &app,
        ticket.previous.current_version.clone(),
        ticket.previous.channel,
    )
    .await;
    let candidate = {
        let mut inner = service.inner.lock().await;
        let (source, _, source_error) = detect_install_source();
        if !inner.finish_install_check(ticket, checked, source, source_error, |fresh| {
            same_candidate(&displayed, fresh)
        }) {
            emit_update_state(&app, &mut inner.state);
            return Ok(inner.state.clone());
        }
        let candidate = inner
            .candidate
            .clone()
            .ok_or_else(|| UpdateError::from(Failure::CandidateMissing))?;
        write_journal(
            &dir,
            &UpdateJournal {
                to_version: candidate.version.clone(),
                phase: UpdateJournalPhase::Installing,
            },
        )?;
        inner.state.phase = UpdatePhase::Installing;
        inner.state.error = None;
        emit_update_state(&app, &mut inner.state);
        candidate
    };
    let mut downloaded = 0u64;
    let downloaded = candidate
        .download(
            |chunk, total| {
                downloaded = downloaded.saturating_add(chunk as u64);
                if let Ok(mut inner) = service.inner.try_lock() {
                    inner.state.downloaded_bytes = downloaded;
                    inner.state.total_bytes = total;
                    emit_update_state(&app, &mut inner.state);
                }
            },
            || {},
        )
        .await;
    let installed = async {
        let bytes = downloaded.map_err(UpdateError::download)?;
        let prepared = crate::app_exit::prepare_install(&app)
            .await
            .map_err(|error| UpdateError::new(Failure::StateUnavailable, error))?;
        candidate
            .install(bytes)
            .map_err(|error| UpdateError::new(Failure::InstallFailed, error))?;
        Ok::<_, UpdateError>(prepared)
    }
    .await;
    let prepared = match installed {
        Ok(prepared) => prepared,
        Err(err) => {
            let mut inner = service.inner.lock().await;
            let journal_error = fs::remove_file(dir.join(JOURNAL_FILE)).err();
            inner.state.phase = UpdatePhase::Available;
            inner.state.error = Some(match journal_error {
                Some(clear_err) => err.with_context(format!("clear update journal: {clear_err}")),
                None => err,
            });
            emit_update_state(&app, &mut inner.state);
            let state = inner.state.clone();
            drop(inner);
            return Ok(state);
        }
    };

    let mut inner = service.inner.lock().await;
    if let Err(err) = write_journal(
        &dir,
        &UpdateJournal {
            to_version: candidate.version.clone(),
            phase: UpdateJournalPhase::RestartRequired,
        },
    ) {
        inner.state.phase = UpdatePhase::RestartRequired;
        inner.state.available_version = Some(candidate.version);
        inner.state.error = Some(err);
        inner.candidate = None;
        emit_update_state(&app, &mut inner.state);
        let state = inner.state.clone();
        drop(inner);
        return Ok(state);
    }
    inner.state.phase = UpdatePhase::RestartRequired;
    inner.state.available_version = Some(candidate.version);
    inner.state.error = None;
    inner.candidate = None;
    emit_update_state(&app, &mut inner.state);
    let state = inner.state.clone();
    drop(inner);
    prepared
        .restart()
        .map_err(|error| UpdateError::new(Failure::StateUnavailable, error))?;
    Ok(state)
}
