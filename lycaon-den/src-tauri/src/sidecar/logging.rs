use std::fs;
use std::io::{Read, Seek};
use std::path::{Path, PathBuf};
use crate::diagnostic_log::append as append_engine_log;

pub(super) const ENGINE_LOG_NAME: &str = "engine.log";
pub(super) const ENGINE_LOG_TAIL_BYTES: usize = 4096;

pub(super) fn engine_failure_message(reason: String) -> String {
    let mut err = reason;
    if let Some(tail) = read_engine_log_tail() {
        err.push_str("\n\nengine log tail:\n");
        err.push_str(&tail);
    }
    err
}

pub(super) fn engine_log_path() -> Option<PathBuf> {
    let dir = crate::config_dir::host_config_dir().ok()?;
    Some(dir.join(ENGINE_LOG_NAME))
}

pub(super) fn drain_engine_stderr(mut source: impl Read, path: Option<PathBuf>) {
    let mut buffer = [0; 8192];
    loop {
        match source.read(&mut buffer) {
            Ok(0) => break,
            Ok(size) => {
                if let Some(path) = &path {
                    let _ = append_engine_log(path, &buffer[..size]);
                }
            }
            Err(error) if error.kind() == std::io::ErrorKind::Interrupted => continue,
            Err(_) => break,
        }
    }
}

pub(super) fn read_engine_log_tail() -> Option<String> {
    let path = engine_log_path()?;
    let tail = read_log_tail(&path, ENGINE_LOG_TAIL_BYTES).ok()?;
    if tail.is_empty() {
        return None;
    }
    Some(tail)
}

pub(super) fn read_log_tail(path: &Path, max_bytes: usize) -> std::io::Result<String> {
    if max_bytes == 0 {
        return Ok(String::new());
    }
    let mut file = fs::File::open(path)?;
    let len = file.metadata()?.len();
    let start = len.saturating_sub(max_bytes as u64);
    if start > 0 {
        file.seek(std::io::SeekFrom::Start(start))?;
    }
    let capacity = usize::try_from(len).unwrap_or(max_bytes).min(max_bytes);
    let mut bytes = Vec::with_capacity(capacity);
    file.take(max_bytes as u64).read_to_end(&mut bytes)?;
    Ok(String::from_utf8_lossy(&bytes).into_owned())
}

pub(super) fn write_engine_log_header(bin: &Path) {
    if let Some(path) = engine_log_path() {
        let _ = append_engine_log(
            &path,
            format!(
                "\n--- spawn {} serve on an ephemeral loopback port ---\n",
                bin.display()
            )
            .as_bytes(),
        );
    }
}
