use std::fs::{self, OpenOptions};
use std::io::Write;
use std::path::Path;
use std::sync::Mutex;

pub(crate) const LOG_MAX_BYTES: u64 = 4 * 1024 * 1024;

static LOG_WRITER: Mutex<()> = Mutex::new(());

// Serialize native appends and rotation.
pub(crate) fn append(path: &Path, bytes: &[u8]) -> std::io::Result<()> {
    let _guard = LOG_WRITER
        .lock()
        .map_err(|_| std::io::Error::other("Diagnostic log lock failed."))?;
    if let Some(parent) = path.parent() {
        crate::config_dir::ensure_private_dir(parent)?;
    }
    for chunk in bytes.chunks(LOG_MAX_BYTES as usize) {
        let size = fs::metadata(path).map(|m| m.len()).unwrap_or(0);
        if size > 0 && size.saturating_add(chunk.len() as u64) > LOG_MAX_BYTES {
            crate::atomic_file::replace(path, &path.with_extension("log.1"), false)?;
        }
        let mut options = OpenOptions::new();
        options.create(true).append(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(0o600);
        }
        options.open(path)?.write_all(chunk)?;
    }
    Ok(())
}
