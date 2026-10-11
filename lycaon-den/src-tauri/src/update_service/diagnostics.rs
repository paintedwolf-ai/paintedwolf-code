use super::{now, NativeUpdateState};

pub(super) fn record(state: &mut NativeUpdateState) {
    record_with(state, |bytes| {
        if let Ok(dir) = crate::config_dir::host_config_dir() {
            let _ = crate::diagnostic_log::append(&dir.join("updates.log"), bytes);
        }
    });
}

fn record_with(state: &mut NativeUpdateState, write: impl FnOnce(&[u8])) {
    if state.last_error == state.diagnostic_error {
        return;
    }
    state.diagnostic_error = state.last_error.clone();
    let Some(error) = &state.last_error else {
        return;
    };
    let record = serde_json::json!({
        "timestamp": now(), "running_version": state.running_version,
        "channel": state.channel, "discovery": state.discovery,
        "installation": state.installation, "error": error,
    });
    write(format!("{record}\n").as_bytes());
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::update_service::{Failure, InstallSource, UpdateChannel, UpdateError};

    #[test]
    fn records_failure_transitions_and_preserves_the_original_cause() {
        let mut state = NativeUpdateState::new(
            "1.0.2".into(),
            UpdateChannel::Preview,
            InstallSource::DirectDownload,
        );
        record_with(&mut state, |_| panic!("no failure"));
        state.last_error = Some(UpdateError::new(
            Failure::FeedRejected,
            "latest.json.sig returned HTTP 404",
        ));
        record_with(&mut state, |bytes| {
            let record: serde_json::Value = serde_json::from_slice(bytes).unwrap();
            assert_eq!(record["channel"], "preview");
            assert_eq!(record["running_version"], "1.0.2");
            assert_eq!(record["error"]["code"], "feed_rejected");
            assert_eq!(
                record["error"]["detail"],
                "latest.json.sig returned HTTP 404"
            );
        });
        record_with(&mut state, |_| panic!("duplicate failure"));
        state.last_error = None;
        record_with(&mut state, |_| panic!("recovery is not a failure"));
        state.last_error = Some(Failure::FeedRejected.into());
        record_with(&mut state, |_| {});
    }
}
