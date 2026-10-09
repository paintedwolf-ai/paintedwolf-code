//! UI state is derived from durable transaction progress.
use super::{
    invalidate_preparation,
    journal::{self, read, retire, Phase},
};
use crate::update_service::{
    emit, installer, Discovery, Failure, Installation, NativeUpdateState, UpdateError, UpdateService,
};
use tauri::{AppHandle, Manager};

pub fn clear_failed(release: &str) -> Result<(), UpdateError> {
    if let Some(t) = read()? {
        if t.phase == Phase::Failed && t.candidate.release_id == release {
            retire(&t)?;
        }
    }
    Ok(())
}
pub async fn resume(app: &AppHandle, error: Option<UpdateError>) {
    let service = app.state::<UpdateService>();
    let mut inner = service.inner.lock().await;
    let committed = read()
        .ok()
        .flatten()
        .is_some_and(|t| t.phase == Phase::Committed);
    resume_state(&mut inner.state, error.clone(), committed);
    if let (Some(error), Some(candidate)) = (error, inner.state.candidate.clone()) {
        if error.code == Failure::ActivationFailed {
            invalidate_preparation(&mut inner, &candidate, error);
        }
    }
    emit(app, &mut inner.state);
}
pub(super) fn resume_state(
    state: &mut NativeUpdateState,
    error: Option<UpdateError>,
    committed: bool,
) {
    match error.as_ref().map(|error| error.code) {
        Some(Failure::RecoveryRequired) => {
            state.staged_release_id = None;
            state.installation = Installation::RecoveryRequired;
        }
        Some(Failure::ActivationFailed) => {
            state.staged_release_id = None;
            state.installation = Installation::Failed;
        }
        _ if committed => state.installation = Installation::Committed,
        _ if state.staged_release_id.is_some() => state.installation = Installation::Staged,
        _ => {}
    }
    state.last_error = error;
}
pub(in crate::update_service) fn retained_release() -> Result<Option<String>, UpdateError> {
    Ok(read()?
        .filter(|t| {
            matches!(
                t.phase,
                Phase::Prepared | Phase::Committed | Phase::Activated
            )
        })
        .map(|t| t.candidate.release_id))
}
/// Projects the journal into startup state and names a release blocked by a recorded failure.
pub(in crate::update_service) fn restore(
    state: &mut NativeUpdateState,
) -> Result<Option<String>, UpdateError> {
    let Some(t) = read()? else {
        return Ok(None);
    };
    if !installer::bundle().is_ok_and(|bundle| bundle == t.target) {
        return Ok(None);
    }
    match t.phase {
        Phase::Committed => {
            state.staged_release_id = Some(t.candidate.release_id.clone());
            state.candidate = Some(t.candidate);
            state.discovery = Discovery::Available;
            state.installation = Installation::Committed;
            Ok(None)
        }
        Phase::Activated if state.installation != Installation::Staged => {
            state.installation = Installation::AwaitingStartup;
            state.staged_release_id = None;
            state.candidate = None;
            Ok(None)
        }
        Phase::Failed => {
            state.candidate = Some(t.candidate.clone());
            state.staged_release_id = None;
            state.installation = Installation::Failed;
            state.last_error = Some(UpdateError::new(
                Failure::ActivationFailed,
                t.error.unwrap_or_else(|| "Interrupted activation".into()),
            ));
            Ok(Some(t.candidate.release_id))
        }
        _ => Ok(None),
    }
}

pub fn recovery_guidance(error: &UpdateError) -> String {
    if error.code == Failure::StateUnavailable {
        return "Painted Wolf Code could not coordinate access to this installation. Close other copies and reopen the app. Your saved work is unchanged.".into();
    }
    if error.code == Failure::CandidateChanged {
        return "Painted Wolf Code was updated while this copy was opening. Close this copy and open the installed application again. Your saved work is unchanged.".into();
    }
    let journal = journal::active_path()
        .map(|path| path.display().to_string())
        .unwrap_or_else(|_| {
            "the transaction.json file in the application's updates directory".into()
        });
    format!("Painted Wolf Code could not safely recover an application update. The update record has been preserved.\n\nRecord: {journal}\n\nQuit every instance of Painted Wolf Code. Keep a copy of this record for support. If the record is unreadable or from an incompatible beta, move it out of the updates directory, then reinstall the same or a newer signed release at the same application location before launching. Reinstalling alone does not replace the update record. Do not remove your saved-work or database files.\n\n{error}")
}
