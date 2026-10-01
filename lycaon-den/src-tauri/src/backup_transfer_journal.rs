//! Tracks native archive temporaries across process exits.

use std::fs::{self, File, OpenOptions, TryLockError};
use std::io::{self, Read, Write};
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

const DIRECTORY: &str = "native-backup-transfers";
const RECEIPT_LIMIT: u64 = 64 << 10;

#[derive(Deserialize, Serialize, PartialEq, Eq)]
struct Identity {
    volume: u64,
    object: u64,
}

#[cfg(unix)]
fn identity(file: &File) -> io::Result<Identity> {
    use std::os::unix::fs::MetadataExt;
    let metadata = file.metadata()?;
    Ok(Identity {
        volume: metadata.dev(),
        object: metadata.ino(),
    })
}

#[cfg(windows)]
fn identity(file: &File) -> io::Result<Identity> {
    use std::os::windows::io::AsRawHandle;
    use windows_sys::Win32::Storage::FileSystem::{
        GetFileInformationByHandle, BY_HANDLE_FILE_INFORMATION,
    };
    let mut info: BY_HANDLE_FILE_INFORMATION = unsafe { std::mem::zeroed() };
    if unsafe { GetFileInformationByHandle(file.as_raw_handle(), &mut info) } == 0 {
        return Err(io::Error::last_os_error());
    }
    Ok(Identity {
        volume: info.dwVolumeSerialNumber as u64,
        object: (info.nFileIndexHigh as u64) << 32 | info.nFileIndexLow as u64,
    })
}

#[derive(Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
struct Receipt {
    version: u32,
    temporary: PathBuf,
    identity: Identity,
}

fn private_options() -> OpenOptions {
    let mut options = OpenOptions::new();
    options.read(true).write(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options
            .mode(0o600)
            .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK);
    }
    #[cfg(windows)]
    {
        use std::os::windows::fs::OpenOptionsExt;
        use windows_sys::Win32::Storage::FileSystem::FILE_FLAG_OPEN_REPARSE_POINT;
        options.custom_flags(FILE_FLAG_OPEN_REPARSE_POINT);
    }
    options
}

fn sync_directory(path: &Path) -> io::Result<()> {
    #[cfg(unix)]
    File::open(path)?.sync_all()?;
    Ok(())
}

pub struct TemporaryArchive {
    receipt: Receipt,
    record_path: PathBuf,
    record: Option<File>,
}

impl TemporaryArchive {
    pub fn create(config: &Path, parent: &Path) -> io::Result<(Self, File)> {
        let root = config.join(DIRECTORY);
        crate::config_dir::ensure_private_dir(&root)?;
        let id = uuid::Uuid::new_v4().to_string();
        let path = parent.canonicalize()?.join(format!(".backup-{id}.tmp"));
        let file = private_options().create_new(true).open(&path)?;
        let receipt = match identity(&file) {
            Ok(identity) => Receipt {
                version: 1,
                temporary: path.clone(),
                identity,
            },
            Err(error) => {
                let _ = fs::remove_file(path);
                return Err(error);
            }
        };
        let mut temporary = Self {
            receipt,
            record_path: root.join(format!("{id}.json")),
            record: None,
        };
        temporary.record = Some(
            private_options()
                .create_new(true)
                .open(&temporary.record_path)?,
        );
        let record = temporary.record.as_mut().expect("created receipt");
        record.try_lock()?;
        serde_json::to_writer(&mut *record, &temporary.receipt)?;
        record.flush()?;
        record.sync_all()?;
        sync_directory(&root)?;
        sync_directory(config)?;
        Ok((temporary, file))
    }

    pub fn path(&self) -> &Path {
        &self.receipt.temporary
    }
}

impl Drop for TemporaryArchive {
    fn drop(&mut self) {
        if remove_recorded_temporary(&self.receipt).is_ok() {
            if self.record.is_some() {
                let _ = fs::remove_file(&self.record_path);
            }
            self.record.take();
            if let Some(root) = self.record_path.parent() {
                let _ = sync_directory(root);
            }
        }
    }
}

fn remove_recorded_temporary(receipt: &Receipt) -> io::Result<()> {
    let metadata = match fs::symlink_metadata(&receipt.temporary) {
        Ok(value) => value,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(()),
        Err(error) => return Err(error),
    };
    if !metadata.is_file() || metadata.file_type().is_symlink() {
        return Ok(());
    }
    let file = private_options().open(&receipt.temporary)?;
    let matches = identity(&file)? == receipt.identity;
    drop(file);
    if matches {
        fs::remove_file(&receipt.temporary)?;
    }
    Ok(())
}

fn clean_record(path: &Path, id: &str) -> io::Result<()> {
    let record = private_options().open(path)?;
    if !record.metadata()?.is_file() {
        return Ok(());
    }
    match record.try_lock() {
        Ok(()) => {}
        Err(TryLockError::WouldBlock) => return Ok(()),
        Err(TryLockError::Error(error)) => return Err(error),
    }
    let mut bytes = Vec::new();
    (&record).take(RECEIPT_LIMIT + 1).read_to_end(&mut bytes)?;
    if bytes.len() as u64 > RECEIPT_LIMIT {
        return Err(io::Error::other(
            "Backup transfer receipt exceeds its limit",
        ));
    }
    let receipt: Receipt = serde_json::from_slice(&bytes)?;
    if receipt.version != 1
        || !receipt.temporary.is_absolute()
        || receipt.temporary.file_name().and_then(|name| name.to_str())
            != Some(format!(".backup-{id}.tmp").as_str())
    {
        return Err(io::Error::other("Invalid backup transfer receipt"));
    }
    remove_recorded_temporary(&receipt)?;
    fs::remove_file(path)?;
    Ok(())
}

pub fn cleanup(config: &Path) -> io::Result<()> {
    let root = config.join(DIRECTORY);
    let entries = match fs::read_dir(&root) {
        Ok(entries) => entries,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(()),
        Err(error) => return Err(error),
    };
    let mut failure = None;
    for entry in entries {
        let entry = entry?;
        if !entry.file_type()?.is_file() {
            continue;
        }
        let path = entry.path();
        if path.extension().and_then(|value| value.to_str()) != Some("json") {
            continue;
        }
        let Some(id) = path.file_stem().and_then(|value| value.to_str()) else {
            continue;
        };
        if uuid::Uuid::parse_str(id).is_err() {
            continue;
        }
        if let Err(error) = clean_record(&path, id) {
            failure = Some(error);
        }
    }
    sync_directory(&root)?;
    failure.map_or(Ok(()), Err)
}

pub fn cleanup_on_launch() {
    if let Ok(config) = crate::config_dir::host_config_dir() {
        std::thread::spawn(move || {
            if let Err(error) = cleanup(&config) {
                eprintln!("Could not clean up an interrupted backup transfer: {error}");
            }
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_support::TempDir;

    #[test]
    fn active_transfer_is_preserved_and_drop_removes_its_receipt() {
        let config = TempDir::new("backup-journal-config");
        let output = TempDir::new("backup-journal-output");
        let (temporary, mut file) = TemporaryArchive::create(&config, &output).expect("temporary");
        file.write_all(b"partial").expect("partial bytes");
        cleanup(&config).expect("skip held receipt");
        assert!(temporary.path().exists());
        assert!(temporary.record_path.exists());
        drop(file);
        drop(temporary);
        assert_eq!(fs::read_dir(&output).expect("output").count(), 0);
        assert_eq!(
            fs::read_dir(config.join(DIRECTORY))
                .expect("empty journal")
                .count(),
            0
        );
    }

    #[test]
    fn crash_fixture_child() {
        let Some(config) = std::env::var_os("PW_BACKUP_CRASH_CONFIG") else {
            return;
        };
        let output = std::env::var_os("PW_BACKUP_CRASH_OUTPUT").expect("output");
        let (temporary, mut file) =
            TemporaryArchive::create(Path::new(&config), Path::new(&output))
                .expect("crash temporary");
        file.write_all(b"interrupted backup body").expect("body");
        file.sync_all().expect("durable body");
        if std::env::var("PW_BACKUP_CRASH_PUBLISHED").as_deref() == Ok("1") {
            drop(file);
            crate::atomic_file::replace(
                temporary.path(),
                &Path::new(&output).join("existing.zip"),
                true,
            )
            .expect("publish before exit");
        }
        std::process::exit(0);
    }

    #[test]
    fn process_exit_leaves_recoverable_receipt_and_cleanup_preserves_other_files() {
        for published in [false, true] {
            let config = TempDir::new("backup-crash-config");
            let output = TempDir::new("backup-crash-output");
            fs::write(output.join("existing.zip"), b"previous").expect("previous archive");
            fs::write(output.join(".backup-unrelated.tmp"), b"foreign").expect("foreign file");
            let status = std::process::Command::new(std::env::current_exe().expect("test binary"))
                .args([
                    "--exact",
                    "backup_transfer_journal::tests::crash_fixture_child",
                    "--nocapture",
                ])
                .env("PW_BACKUP_CRASH_CONFIG", &*config)
                .env("PW_BACKUP_CRASH_OUTPUT", &*output)
                .env(
                    "PW_BACKUP_CRASH_PUBLISHED",
                    if published { "1" } else { "0" },
                )
                .status()
                .expect("crash child");
            assert!(status.success());
            assert_eq!(
                fs::read_dir(&output).expect("interrupted output").count(),
                if published { 2 } else { 3 }
            );
            cleanup(&config).expect("recover interrupted transfer");
            assert_eq!(fs::read_dir(&output).expect("clean output").count(), 2);
            assert_eq!(
                fs::read(output.join("existing.zip")).expect("previous archive"),
                if published {
                    b"interrupted backup body".as_slice()
                } else {
                    b"previous".as_slice()
                }
            );
            assert_eq!(
                fs::read(output.join(".backup-unrelated.tmp")).expect("foreign file"),
                b"foreign"
            );
            cleanup(&config).expect("repeat cleanup");
        }
    }

    #[test]
    fn cleanup_does_not_remove_a_replacement_at_the_temporary_path() {
        let config = TempDir::new("backup-replaced-config");
        let output = TempDir::new("backup-replaced-output");
        let (mut temporary, file) = TemporaryArchive::create(&config, &output).expect("temporary");
        let path = temporary.path().to_path_buf();
        fs::rename(&path, output.join("moved-original")).expect("retain original inode");
        fs::write(&path, b"replacement").expect("foreign replacement");
        drop(file);
        temporary.record.take();
        cleanup(&config).expect("preserve replacement");
        assert_eq!(fs::read(&path).expect("replacement"), b"replacement");
        assert_eq!(
            fs::read_dir(config.join(DIRECTORY))
                .map(|entries| entries.count())
                .unwrap_or(0),
            0
        );
    }

    #[cfg(unix)]
    #[test]
    fn cleanup_does_not_follow_a_replaced_symlink() {
        let config = TempDir::new("backup-symlink-config");
        let output = TempDir::new("backup-symlink-output");
        let (mut temporary, file) = TemporaryArchive::create(&config, &output).expect("temporary");
        let path = temporary.path().to_path_buf();
        fs::remove_file(&path).expect("remove temporary");
        let foreign = output.join("foreign");
        fs::write(&foreign, b"foreign").expect("foreign content");
        std::os::unix::fs::symlink(&foreign, &path).expect("replacement link");
        drop(file);
        temporary.record.take();
        cleanup(&config).expect("preserve link");
        assert!(path.is_symlink());
        assert_eq!(fs::read(&foreign).expect("foreign remains"), b"foreign");
    }
}
