use super::{digest, field, format, record_kind, record_name, Header};
use serde_json::Value;
use std::collections::HashSet;
use std::fs;
use std::io::{Read, Seek, SeekFrom};
use std::path::{Path, PathBuf};

pub(super) fn require_directory(path: &Path) -> Result<(), String> {
    match fs::symlink_metadata(path) {
        Ok(info) if info.is_dir() => Ok(()),
        Ok(_) => Err("Preserved editor storage must be a directory.".into()),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(error) => Err(error.to_string()),
    }
}

pub(super) fn document_dir(root: &Path, id: &str) -> Result<PathBuf, String> {
    if id.is_empty() || id.len() > 256 {
        return Err("Invalid document identity.".into());
    }
    let base = root.join(&format().directory);
    require_directory(&base)?;
    Ok(base.join(digest(id.as_bytes())))
}

pub(super) fn open_lock(path: &Path) -> Result<fs::File, String> {
    crate::config_dir::ensure_private_dir(path.parent().ok_or("Missing editor lock directory.")?)
        .map_err(|e| e.to_string())?;
    let mut options = fs::OpenOptions::new();
    options.read(true).write(true).create(true).truncate(false);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    options.open(path).map_err(|e| e.to_string())
}

// Capture, restore, and pruning exclude readers and writers across every process.
pub(super) fn store_lock(root: &Path) -> Result<fs::File, String> {
    let file = open_lock(&root.join(&format().lock))?;
    file.lock_shared().map_err(|e| e.to_string())?;
    Ok(file)
}

pub(super) fn document_lock(root: &Path, id: &str) -> Result<fs::File, String> {
    let directory = document_dir(root, id)?;
    let locks = root.join(&format().lock).with_extension("d");
    let file = open_lock(&locks.join(directory.file_name().ok_or("Missing document identity.")?))?;
    file.lock().map_err(|e| e.to_string())?;
    Ok(file)
}

pub(super) fn log_name(generation: u64) -> String {
    format!("{}{generation}{}", format().log_prefix, format().log_suffix)
}

/// Generation number of a log file name; temporary files count as their target.
pub(super) fn log_generation(name: &str) -> Option<u64> {
    let name = name.strip_suffix(".tmp").unwrap_or(name);
    name.strip_prefix(format().log_prefix.as_str())?
        .strip_suffix(format().log_suffix.as_str())?
        .parse()
        .ok()
        .filter(|generation| *generation > 0)
}

pub(super) fn is_header_name(name: &str) -> bool {
    name == format().header || name == format!("{}.tmp", format().header)
}

/// Refuses foreign entries so a restored tree cannot redirect preservation.
pub(super) fn check_document_entries(directory: &Path) -> Result<bool, String> {
    require_directory(directory)?;
    match fs::read_dir(directory) {
        Ok(entries) => {
            for entry in entries {
                let entry = entry.map_err(|error| error.to_string())?;
                let name = entry.file_name();
                let name = name.to_string_lossy();
                let kind = match entry.file_type() {
                    Ok(kind) => kind,
                    Err(error) if error.kind() == std::io::ErrorKind::NotFound => continue,
                    Err(error) => return Err(error.to_string()),
                };
                if (!is_header_name(&name) && log_generation(&name).is_none()) || !kind.is_file() {
                    return Err(
                        "Unsupported preserved editor entry; the original bytes remain intact."
                            .into(),
                    );
                }
            }
            Ok(true)
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(false),
        Err(error) => Err(error.to_string()),
    }
}

pub(super) fn valid_retention(header: &Header) -> bool {
    let mut clients = HashSet::new();
    header.retained_clients.iter().all(|client| {
        !client.is_empty()
            && client.len() <= 256
            && clients.insert(client)
            && header.frames.iter().any(|frame| {
                frame.kind == "checkpoint"
                    && frame.name == digest(format!("{client}\0checkpoint\0checkpoint").as_bytes())
            })
    })
}

pub(super) fn header_at(directory: &Path) -> Result<Option<Header>, String> {
    if !check_document_entries(directory)? {
        return Ok(None);
    }
    let path = directory.join(&format().header);
    let info = match fs::symlink_metadata(&path) {
        Ok(info) => info,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(e.to_string()),
    };
    if !info.is_file() || info.len() > format().max_header_bytes {
        return Err("Invalid preserved editor header.".into());
    }
    let raw = match fs::read(&path) {
        Ok(raw) => raw,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(error.to_string()),
    };
    let header: Header = serde_json::from_slice(&raw).map_err(|e| e.to_string())?;
    let identity = digest(field(&header.address, "documentId")?.as_bytes());
    let consistent = header.format == format().version
        && header.log_bytes <= format().max_document_bytes
        && log_generation(&header.log).is_some_and(|_| !header.log.ends_with(".tmp"))
        && directory.file_name().and_then(|n| n.to_str()) == Some(identity.as_str())
        && header.frames.iter().all(|frame| {
            frame.length > 0
                && frame.length <= format().max_record_bytes
                && frame
                    .offset
                    .checked_add(frame.length)
                    .is_some_and(|end| end <= header.log_bytes)
                && matches!(
                    frame.kind.as_str(),
                    "checkpoint" | "update" | "command" | "format"
                )
        })
        && valid_retention(&header)
        && header.synchronized == Header::synchronized_by_frames(&header.frames);
    if !consistent {
        return Err("Unsupported or inconsistent preserved editor state.".into());
    }
    Ok(Some(header))
}

/// Reads every live frame from one generation, returning None when the
/// generation was compacted away after the header was read.
pub(super) fn frames_at(
    directory: &Path,
    header: &Header,
    id: &str,
) -> Result<Option<Vec<Value>>, String> {
    let mut log = match fs::File::open(directory.join(&header.log)) {
        Ok(log) => log,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(error.to_string()),
    };
    if log.metadata().map_err(|e| e.to_string())?.len() < header.log_bytes {
        return Err("Preserved editor records are shorter than their header.".into());
    }
    let mut records = Vec::with_capacity(header.frames.len());
    let mut names = HashSet::new();
    for frame in &header.frames {
        log.seek(SeekFrom::Start(frame.offset))
            .map_err(|e| e.to_string())?;
        let mut raw = vec![0; frame.length as usize];
        log.read_exact(&mut raw).map_err(|e| e.to_string())?;
        if digest(&raw) != frame.sha256 || raw.last() != Some(&b'\n') {
            return Err("Preserved editor checksum does not match.".into());
        }
        let mut record: Value = serde_json::from_slice(&raw).map_err(|e| e.to_string())?;
        if field(&record, "documentId")? != id
            || record_name(&record)? != frame.name
            || record_kind(&record)? != frame.kind
            || !names.insert(frame.name.clone())
        {
            return Err("Preserved editor record identity does not match its location.".into());
        }
        if frame.kind == "checkpoint" {
            if let Some(pending) = &frame.pending {
                record["pendingOperations"] =
                    Value::Array(pending.iter().cloned().map(Value::String).collect());
            }
            if let Some(synchronized) = frame.synchronized {
                record["synchronized"] = synchronized.into();
            }
        }
        records.push(record);
    }
    Ok(Some(records))
}

#[cfg(test)]
pub(super) fn read_at(root: &Path, id: &str) -> Result<Vec<Value>, String> {
    read_bounded(root, id, format().max_document_bytes)
}

pub(super) fn read_bounded(root: &Path, id: &str, max_bytes: u64) -> Result<Vec<Value>, String> {
    let directory = document_dir(root, id)?;
    // A concurrent compaction retires the generation a header named; the next header names its replacement.
    for _ in 0..8 {
        let Some(header) = header_at(&directory)? else {
            return Ok(vec![]);
        };
        if header.live_bytes() > max_bytes {
            return Err(
                "The editor recovery record grew beyond its memory reservation. Retry opening it."
                    .into(),
            );
        }
        if let Some(records) = frames_at(&directory, &header, id)? {
            return Ok(records);
        }
    }
    Err("Preserved editor records were replaced while reading.".into())
}

pub(super) fn sync_directory(directory: &Path) -> Result<(), String> {
    #[cfg(unix)]
    fs::File::open(directory)
        .and_then(|file| file.sync_all())
        .map_err(|e| e.to_string())?;
    #[cfg(not(unix))]
    let _ = directory;
    Ok(())
}
