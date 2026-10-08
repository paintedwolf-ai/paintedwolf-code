//! Per-profile replay watermarks, serialized across every installation.
use super::{persistence, record_lock, Failure, UpdateError};
use serde::{Deserialize, Serialize};
use std::{collections::BTreeMap, path::Path};

pub async fn accept(dir: &Path, feed: &str, timestamp: u64) -> Result<(), UpdateError> {
    let _lock = record_lock::acquire_async(dir, "feed-state.lock").await?;
    accept_locked(dir, feed, timestamp)
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
    Ok(persistence::read_or_quarantine(
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
fn accept_locked(dir: &Path, feed: &str, timestamp: u64) -> Result<(), UpdateError> {
    let mut state = read_feed_state(dir)?;
    match state.feeds.get(feed) {
        Some(&newest) if newest > timestamp => Err(UpdateError::new(
            Failure::InvalidRelease,
            "The update feed is older than one this device already accepted",
        )),
        Some(&newest) if newest == timestamp => Ok(()),
        _ => {
            state.feeds.insert(feed.into(), timestamp);
            persistence::write_json_atomic(
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
    use std::fs;
    #[tokio::test]
    async fn process_writer() {
        let Some(dir) = std::env::var_os("PW_TEST_FEED_STATE_DIR") else {
            return;
        };
        let number: u64 = std::env::var("PW_TEST_FEED_NUMBER")
            .unwrap()
            .parse()
            .unwrap();
        let dir = std::path::PathBuf::from(dir);
        accept(
            &dir,
            &format!("latest-stable-key-{number}.json"),
            number * 100,
        )
        .await
        .unwrap();
        let _ = accept(&dir, "latest-preview-key-1.json", number * 100).await;
    }
    #[test]
    fn processes_preserve_every_feed_and_the_largest_watermark() {
        let root = TempDir::new("update-feed-processes");
        let lock = record_lock::acquire(&root, "feed-state.lock").unwrap();
        let children: Vec<_> = (1..=8)
            .map(|number| {
                std::process::Command::new(std::env::current_exe().unwrap())
                    .args([
                        "--exact",
                        "update_service::feed_state::tests::process_writer",
                    ])
                    .env("PW_TEST_FEED_STATE_DIR", root.as_os_str())
                    .env("PW_TEST_FEED_NUMBER", number.to_string())
                    .spawn()
                    .unwrap()
            })
            .collect();
        drop(lock);
        for mut child in children {
            assert!(child.wait().unwrap().success());
        }
        let state = read_feed_state(&root).unwrap();
        for number in 1..=8 {
            assert_eq!(
                state.feeds[&format!("latest-stable-key-{number}.json")],
                number * 100
            );
        }
        assert_eq!(state.feeds["latest-preview-key-1.json"], 800);
    }
    #[tokio::test]
    async fn cancellation_does_not_leave_a_lock_or_accept_a_timestamp() {
        let root = TempDir::new("update-feed-cancel");
        let lock = record_lock::acquire(&root, "feed-state.lock").unwrap();
        assert!(tokio::time::timeout(
            std::time::Duration::from_millis(30),
            accept(&root, "latest-stable-key-1.json", 100)
        )
        .await
        .is_err());
        assert!(!root.join(FEED_STATE_FILE).exists());
        drop(lock);
        accept(&root, "latest-stable-key-1.json", 50).await.unwrap();
    }
    #[test]
    fn feed_timestamps_only_advance_and_a_replayed_pointer_is_refused() {
        let root = TempDir::new("update-feed-state");
        accept_locked(&root, "latest-stable-key-1.json", 100).unwrap();
        accept_locked(&root, "latest-stable-key-1.json", 100).unwrap();
        accept_locked(&root, "latest-stable-key-1.json", 150).unwrap();
        let replay = accept_locked(&root, "latest-stable-key-1.json", 120).unwrap_err();
        assert_eq!(replay.code, Failure::InvalidRelease);
        accept_locked(&root, "latest-preview-key-1.json", 5).unwrap();
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
            assert!(persistence::read_json(
                &path,
                Failure::JournalUnavailable,
                |state: &FeedState| { state.format_version == 1 }
            )
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
