//! Single-use transfer grants for paths selected in native panels.

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::{LazyLock, Mutex};
use std::time::{Duration, Instant};

use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine;
use rand::RngCore;

/// Bounds how long a handle left in renderer state stays redeemable.
const GRANT_TTL: Duration = Duration::from_secs(5 * 60);

/// Each selection authorizes one transfer direction.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum GrantMode {
    Read,
    Write,
}

impl GrantMode {
    fn describe(self) -> &'static str {
        match self {
            GrantMode::Read => "read",
            GrantMode::Write => "write",
        }
    }
}

struct Grant {
    path: PathBuf,
    mode: GrantMode,
    minted: Instant,
}

/// Redeeming removes the entry, so one pick authorizes one transfer.
struct GrantTable {
    entries: HashMap<String, Grant>,
}

impl GrantTable {
    fn new() -> Self {
        Self {
            entries: HashMap::new(),
        }
    }

    fn mint_at(&mut self, path: &Path, mode: GrantMode, now: Instant) -> String {
        self.entries
            .retain(|_, g| now.duration_since(g.minted) < GRANT_TTL);
        let mut bytes = [0u8; 32];
        rand::thread_rng().fill_bytes(&mut bytes);
        let handle = URL_SAFE_NO_PAD.encode(bytes);
        self.entries.insert(
            handle.clone(),
            Grant {
                path: path.to_path_buf(),
                mode,
                minted: now,
            },
        );
        handle
    }

    fn redeem_at(
        &mut self,
        handle: &str,
        mode: GrantMode,
        now: Instant,
    ) -> Result<PathBuf, String> {
        let Some(grant) = self.entries.remove(handle) else {
            return Err("this file selection is no longer available".to_string());
        };
        if now.duration_since(grant.minted) >= GRANT_TTL {
            return Err("this file selection has expired; choose the file again".to_string());
        }
        if grant.mode != mode {
            return Err(format!(
                "this file selection does not permit {}",
                mode.describe()
            ));
        }
        Ok(grant.path)
    }
}

static GRANTS: LazyLock<Mutex<GrantTable>> = LazyLock::new(|| Mutex::new(GrantTable::new()));

pub fn mint(path: &Path, mode: GrantMode) -> Result<String, String> {
    let mut table = GRANTS
        .lock()
        .map_err(|_| "file grant registry poisoned".to_string())?;
    Ok(table.mint_at(path, mode, Instant::now()))
}

pub fn redeem(handle: &str, mode: GrantMode) -> Result<PathBuf, String> {
    let mut table = GRANTS
        .lock()
        .map_err(|_| "file grant registry poisoned".to_string())?;
    table.redeem_at(handle, mode, Instant::now())
}

/// Publishes a complete synced export, preserving the destination if writing fails.
pub fn save_atomic(path: &Path, mut source: impl std::io::Read) -> Result<(), String> {
    let parent = path.parent().ok_or("Export destination has no parent")?;
    std::fs::create_dir_all(parent).map_err(|e| e.to_string())?;
    let temporary = parent.join(format!(".export-{}.tmp", uuid::Uuid::new_v4()));
    let result = (|| -> std::io::Result<()> {
        let mut options = std::fs::OpenOptions::new();
        options.create_new(true).write(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(0o600);
        }
        let mut file = options.open(&temporary)?;
        std::io::copy(&mut source, &mut file)?;
        file.sync_all()?;
        drop(file);
        crate::atomic_file::replace(&temporary, path, true)?;
        Ok(())
    })();
    let _ = std::fs::remove_file(temporary);
    result.map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn table() -> GrantTable {
        GrantTable::new()
    }

    #[test]
    fn failed_export_preserves_existing_destination_and_removes_temporary_file() {
        struct FailedRead;
        impl std::io::Read for FailedRead {
            fn read(&mut self, _: &mut [u8]) -> std::io::Result<usize> {
                Err(std::io::Error::other("interrupted source"))
            }
        }
        let dir = crate::test_support::TempDir::new("export-interruption");
        let target = dir.join("saved.txt");
        std::fs::write(&target, b"original").expect("seed destination");
        save_atomic(&target, FailedRead).expect_err("source failure");
        assert_eq!(std::fs::read(&target).expect("read preserved"), b"original");
        assert_eq!(std::fs::read_dir(&dir).expect("directory").count(), 1);
        save_atomic(&target, &b"complete"[..]).expect("complete export");
        assert_eq!(
            std::fs::read(&target).expect("read replacement"),
            b"complete"
        );
    }

    #[test]
    fn a_handle_redeems_once_to_the_picked_path() {
        let mut t = table();
        let now = Instant::now();
        let handle = t.mint_at(Path::new("/tmp/export.zip"), GrantMode::Write, now);

        assert_eq!(
            t.redeem_at(&handle, GrantMode::Write, now).expect("redeem"),
            PathBuf::from("/tmp/export.zip")
        );
        t.redeem_at(&handle, GrantMode::Write, now)
            .expect_err("second redemption refused");
    }

    #[test]
    fn an_unknown_handle_is_refused() {
        let mut t = table();
        t.redeem_at("not-a-handle", GrantMode::Write, Instant::now())
            .expect_err("unknown handle refused");
    }

    #[test]
    fn a_grant_does_not_cross_direction() {
        let mut t = table();
        let now = Instant::now();
        let read_handle = t.mint_at(Path::new("/tmp/backup.zip"), GrantMode::Read, now);
        t.redeem_at(&read_handle, GrantMode::Write, now)
            .expect_err("a read pick may not be written through");

        let write_handle = t.mint_at(Path::new("/tmp/out.zip"), GrantMode::Write, now);
        t.redeem_at(&write_handle, GrantMode::Read, now)
            .expect_err("a save pick may not be read out");
    }

    #[test]
    fn a_stale_grant_is_refused() {
        let mut t = table();
        let now = Instant::now();
        let handle = t.mint_at(Path::new("/tmp/export.zip"), GrantMode::Write, now);
        let later = now + GRANT_TTL + Duration::from_secs(1);
        t.redeem_at(&handle, GrantMode::Write, later)
            .expect_err("expired grant refused");
    }

    #[test]
    fn minting_sweeps_expired_entries() {
        let mut t = table();
        let now = Instant::now();
        let stale = t.mint_at(Path::new("/tmp/a.zip"), GrantMode::Write, now);
        let later = now + GRANT_TTL + Duration::from_secs(1);
        let _fresh = t.mint_at(Path::new("/tmp/b.zip"), GrantMode::Write, later);

        assert_eq!(t.entries.len(), 1, "stale entry swept on mint");
        t.redeem_at(&stale, GrantMode::Write, later)
            .expect_err("swept grant refused");
    }

    #[test]
    fn handles_are_unique_and_unguessable_in_shape() {
        let mut t = table();
        let now = Instant::now();
        let a = t.mint_at(Path::new("/tmp/a"), GrantMode::Read, now);
        let b = t.mint_at(Path::new("/tmp/b"), GrantMode::Read, now);
        assert_ne!(a, b);
        // 32 random bytes, URL-safe base64, unpadded.
        assert_eq!(a.len(), 43);
        assert!(a
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_'));
    }
}
