use super::storage::{document_dir, header_at, log_generation, log_name, sync_directory};
use super::{digest, field, format, now, record_kind, record_name, Frame, Header};
use serde::Deserialize;
use serde_json::Value;
use std::collections::HashSet;
use std::fs;
use std::io::{Read, Seek, SeekFrom, Write};
use std::path::Path;

pub(super) fn address(records: &[Value], old: Option<Value>) -> Result<Value, String> {
    let Some(checkpoint) = records
        .iter()
        .rev()
        .find(|r| r["kind"] == "checkpoint")
        .or_else(|| records.last())
    else {
        return old.ok_or_else(|| "A new editor outbox requires a document address.".into());
    };
    let mut address = serde_json::Map::new();
    for key in ["documentId", "projectId", "rootId", "fileId", "path"] {
        let value = checkpoint
            .get(key)
            .and_then(Value::as_str)
            .filter(|v| !v.is_empty() && v.len() <= if key == "path" { 4096 } else { 256 })
            .ok_or_else(|| format!("Invalid preserved document {key}."))?;
        address.insert(key.into(), Value::String(value.into()));
    }
    Ok(Value::Object(address))
}

/// A record serialized once for its frame; checkpoints carry their own bookkeeping.
struct Encoded {
    frame: Frame,
    bytes: Vec<u8>,
}

fn encode(record: &Value) -> Result<Encoded, String> {
    let mut bytes = serde_json::to_vec(record).map_err(|e| e.to_string())?;
    bytes.push(b'\n');
    if bytes.len() as u64 > format().max_record_bytes {
        return Err("Editor record exceeds its preservation limit.".into());
    }
    let kind = record_kind(record)?.to_owned();
    let checkpoint = kind == "checkpoint";
    Ok(Encoded {
        frame: Frame {
            name: record_name(record)?,
            kind,
            offset: 0,
            length: bytes.len() as u64,
            sha256: digest(&bytes),
            pending: record["pendingOperations"]
                .as_array()
                .filter(|_| checkpoint)
                .map(|ids| {
                    ids.iter()
                        .filter_map(Value::as_str)
                        .map(str::to_owned)
                        .collect()
                }),
            synchronized: record["synchronized"].as_bool().filter(|_| checkpoint),
        },
        bytes,
    })
}

// Cross-window acknowledgements settle each checkpoint's bookkeeping without touching its bytes.
pub(super) fn acknowledge_checkpoints(frames: &mut [Frame], records: &[Value], remove: &[Value]) {
    let acknowledged: HashSet<&str> = records
        .iter()
        .filter(|r| r["kind"] == "update" && r["acknowledged"] == true)
        .filter_map(|r| r["operationId"].as_str())
        .collect();
    let format_finished = remove.iter().any(|r| r["kind"] == "format");
    if acknowledged.is_empty() && !format_finished {
        return;
    }
    let has_format = frames.iter().any(|frame| frame.kind == "format");
    for frame in frames.iter_mut().filter(|frame| frame.kind == "checkpoint") {
        let Some(pending) = frame.pending.as_mut() else {
            continue;
        };
        let before = pending.len();
        pending.retain(|id| !acknowledged.contains(id.as_str()));
        if pending.is_empty() && !has_format && (before > 0 || format_finished) {
            frame.synchronized = Some(true);
        }
    }
}

#[cfg(test)]
pub(super) fn commit_at(root: &Path, records: &[Value], remove: &[Value]) -> Result<(), String> {
    commit_with(root, records, remove, format().compaction_bytes)
}

/// Appends the transaction's records, then publishes it by replacing the header.
#[cfg(test)]
pub(super) fn commit_with(
    root: &Path,
    records: &[Value],
    remove: &[Value],
    compaction_bytes: u64,
) -> Result<(), String> {
    commit_transaction(root, records, remove, compaction_bytes, None)
}

pub(super) fn commit_retaining(
    root: &Path,
    records: &[Value],
    remove: &[Value],
    retention: Option<&Retention>,
) -> Result<(), String> {
    commit_transaction(root, records, remove, format().compaction_bytes, retention)
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct Retention {
    pub(super) document: Value,
    pub(super) client_id: String,
    pub(super) retained: bool,
}

pub(super) fn commit_transaction(
    root: &Path,
    records: &[Value],
    remove: &[Value],
    compaction_bytes: u64,
    retention: Option<&Retention>,
) -> Result<(), String> {
    let Some(first) = records
        .first()
        .or_else(|| remove.first())
        .or_else(|| retention.map(|r| &r.document))
    else {
        return Ok(());
    };
    let id = field(first, "documentId")?;
    for record in records.iter().chain(remove) {
        if field(record, "documentId")? != id {
            return Err("An editor transaction must belong to one document.".into());
        }
        record_name(record)?;
    }
    let directory = document_dir(root, id)?;
    let previous = header_at(&directory)?;
    let encoded = records.iter().map(encode).collect::<Result<Vec<_>, _>>()?;
    let removed: HashSet<String> = remove.iter().map(record_name).collect::<Result<_, _>>()?;
    let replaced: HashSet<&str> = encoded
        .iter()
        .map(|entry| entry.frame.name.as_str())
        .collect();

    let (mut frames, log, mut log_bytes) = match &previous {
        Some(header) => (header.frames.clone(), header.log.clone(), header.log_bytes),
        None => (Vec::new(), log_name(next_generation(&directory)?), 0),
    };
    frames
        .retain(|frame| !replaced.contains(frame.name.as_str()) && !removed.contains(&frame.name));
    let mut append_pending = Vec::with_capacity(encoded.len());
    for mut entry in encoded {
        entry.frame.offset = log_bytes;
        log_bytes += entry.frame.length;
        if log_bytes > format().max_document_bytes {
            return Err("Editor document exceeds its preservation limit.".into());
        }
        frames.push(entry.frame);
        append_pending.push(entry.bytes);
    }
    acknowledge_checkpoints(&mut frames, records, remove);

    let mut retained_clients = previous
        .as_ref()
        .map_or_else(Vec::new, |h| h.retained_clients.clone());
    if let Some(retention) = retention {
        if field(&retention.document, "documentId")? != id
            || retention.client_id.is_empty()
            || retention.client_id.len() > 256
        {
            return Err("Invalid editor retention identity.".into());
        }
        retained_clients.retain(|client| client != &retention.client_id);
        if retention.retained {
            let name =
                digest(format!("{}\0checkpoint\0checkpoint", retention.client_id).as_bytes());
            if !frames
                .iter()
                .any(|frame| frame.kind == "checkpoint" && frame.name == name)
            {
                return Err("The editor recovery checkpoint is unavailable.".into());
            }
            retained_clients.push(retention.client_id.clone());
        }
        retained_clients.sort();
    }
    retained_clients.retain(|client| {
        frames.iter().any(|frame| {
            frame.kind == "checkpoint"
                && frame.name == digest(format!("{client}\0checkpoint\0checkpoint").as_bytes())
        })
    });
    if frames.is_empty() {
        if directory.exists() {
            fs::remove_dir_all(&directory).map_err(|e| e.to_string())?;
            sync_directory(&root.join(&format().directory))?;
        }
        return Ok(());
    }

    let mut header = Header {
        format: format().version,
        address: address(
            records,
            previous.as_ref().map(|header| header.address.clone()),
        )?,
        updated_at: now(),
        synchronized: Header::synchronized_by_frames(&frames),
        log,
        log_bytes,
        frames,
        retained_clients,
    };
    crate::config_dir::ensure_private_dir(root).map_err(|e| e.to_string())?;
    crate::config_dir::ensure_private_dir(&root.join(&format().directory))
        .map_err(|e| e.to_string())?;
    sync_directory(root)?;
    crate::config_dir::ensure_private_dir(&directory).map_err(|e| e.to_string())?;
    sync_directory(&root.join(&format().directory))?;
    append_frames(
        &directory,
        &header.log,
        previous.as_ref().map_or(0, |header| header.log_bytes),
        &append_pending,
    )?;

    let dead = header.log_bytes - header.live_bytes();
    let retired = if dead >= compaction_bytes && dead >= header.live_bytes() {
        Some(compact(&directory, &mut header)?)
    } else {
        None
    };
    write_header(&directory, &header)?;
    if let Some(retired) = retired {
        // Late readers of the retired generation retry against the one the header names.
        let _ = fs::remove_file(directory.join(retired));
    }
    Ok(())
}

pub(super) fn next_generation(directory: &Path) -> Result<u64, String> {
    let mut generation = 0;
    if let Ok(entries) = fs::read_dir(directory) {
        for entry in entries.flatten() {
            if let Some(found) = log_generation(&entry.file_name().to_string_lossy()) {
                generation = generation.max(found);
            }
        }
    }
    Ok(generation + 1)
}

/// Writes past the committed length; an unpublished tail from an interrupted commit is dropped first.
pub(super) fn append_frames(
    directory: &Path,
    log: &str,
    committed: u64,
    frames: &[Vec<u8>],
) -> Result<(), String> {
    let path = directory.join(log);
    let mut options = fs::OpenOptions::new();
    options.read(true).write(true).create(true).truncate(false);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let mut file = options.open(&path).map_err(|e| e.to_string())?;
    if !file.metadata().map_err(|e| e.to_string())?.is_file() {
        return Err("Preserved editor records must be a regular file.".into());
    }
    if file.metadata().map_err(|e| e.to_string())?.len() < committed {
        return Err("Preserved editor records are shorter than their header.".into());
    }
    file.set_len(committed).map_err(|e| e.to_string())?;
    if frames.is_empty() {
        return Ok(());
    }
    file.seek(SeekFrom::Start(committed))
        .map_err(|e| e.to_string())?;
    for raw in frames {
        file.write_all(raw).map_err(|e| e.to_string())?;
    }
    file.sync_data().map_err(|e| e.to_string())
}

/// Rewrites the live frames into the next generation and returns the retired log name.
pub(super) fn compact(directory: &Path, header: &mut Header) -> Result<String, String> {
    let mut source = fs::File::open(directory.join(&header.log)).map_err(|e| e.to_string())?;
    let generation = log_generation(&header.log).ok_or("Invalid preserved editor log name.")? + 1;
    let name = log_name(generation);
    let mut raw = Vec::with_capacity(header.live_bytes() as usize);
    for frame in &mut header.frames {
        source
            .seek(SeekFrom::Start(frame.offset))
            .map_err(|e| e.to_string())?;
        let start = raw.len();
        raw.resize(start + frame.length as usize, 0);
        source
            .read_exact(&mut raw[start..])
            .map_err(|e| e.to_string())?;
        if digest(&raw[start..]) != frame.sha256 {
            return Err("Preserved editor checksum does not match.".into());
        }
        frame.offset = start as u64;
    }
    write_bytes(directory, &name, &raw)?;
    let retired = std::mem::replace(&mut header.log, name);
    header.log_bytes = raw.len() as u64;
    Ok(retired)
}

pub(super) fn write_header(directory: &Path, header: &Header) -> Result<(), String> {
    let raw = serde_json::to_vec(header).map_err(|e| e.to_string())?;
    if raw.len() as u64 > format().max_header_bytes {
        return Err("Editor document index exceeds its preservation limit.".into());
    }
    write_bytes(directory, &format().header, &raw)
}

pub(super) fn write_bytes(directory: &Path, name: &str, raw: &[u8]) -> Result<(), String> {
    let temporary = directory.join(format!("{name}.tmp"));
    // Exclusive creation rejects links substituted after temporary-file cleanup.
    match fs::symlink_metadata(&temporary) {
        Ok(info) if info.is_file() => fs::remove_file(&temporary).map_err(|e| e.to_string())?,
        Ok(_) => return Err("Invalid editor temporary file.".into()),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
        Err(error) => return Err(error.to_string()),
    }
    let mut options = fs::OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let mut file = options.open(&temporary).map_err(|e| e.to_string())?;
    let result = (|| {
        file.write_all(raw)
            .and_then(|()| file.sync_data())
            .map_err(|e| e.to_string())?;
        drop(file);
        crate::atomic_file::replace(&temporary, &directory.join(name), true)
            .map_err(|e| e.to_string())
    })();
    if result.is_err() {
        let _ = fs::remove_file(temporary);
    }
    result
}
