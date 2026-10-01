mod authorship;
mod compaction;
mod undo;
use base64::{engine::general_purpose::STANDARD, Engine};
use serde::{Deserialize, Serialize};
use std::{cell::RefCell, collections::BTreeMap, sync::Arc};
use yrs::updates::{decoder::Decode, encoder::Encode};
use yrs::{
    Any, Assoc, Doc, GetString, IndexScope, IndexedSequence, OffsetKind, Options, Out, ReadTxn,
    StateVector, StickyIndex, Text, Transact, Update,
};

const MAX_BYTES: usize = 32 * 1024 * 1024;

thread_local! {
    static DOCUMENTS: RefCell<BTreeMap<u32, Doc>> = const { RefCell::new(BTreeMap::new()) };
}

#[derive(Deserialize)]
struct Request {
    action: String,
    handle: u32,
    #[serde(default)]
    client: u32,
    #[serde(default)]
    update: String,
    #[serde(default)]
    vector: String,
    #[serde(default)]
    edits: Vec<Edit>,
    #[serde(default)]
    checkpoint: bool,
    #[serde(default)]
    anchors: Vec<AnchoredEdit>,
    #[serde(default)]
    guards: Vec<AnchoredEdit>,
    #[serde(default)]
    capture_undo: bool,
    #[serde(default)]
    track_changes: bool,
    #[serde(default)]
    undo: String,
    #[serde(default)]
    retained_undo: Vec<String>,
    #[serde(default)]
    authorship: bool,
    #[serde(default)]
    omit_text: bool,
}

#[derive(Deserialize, Serialize)]
struct Edit {
    index: u32,
    delete: u32,
    insert: String,
}

#[derive(Deserialize, Serialize)]
struct AnchoredEdit {
    start: String,
    end: String,
    expected: String,
    insert: String,
}

#[derive(Default, Serialize)]
struct Response {
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<String>,
    text: String,
    vector: String,
    update: String,
    checkpoint: String,
    anchors: Vec<AnchoredEdit>,
    resolved: Vec<Edit>,
    undo: String,
    undo_units: u64,
    authors: Vec<authorship::Span>,
    inserted: Vec<authorship::IdentityRange>,
    deleted: Vec<authorship::IdentityRange>,
}

fn anchored_edits(doc: &Doc, edits: Vec<Edit>) -> Result<Vec<AnchoredEdit>, String> {
    let txn = doc.transact();
    let text = txn.get_text("text").ok_or("invalid_schema")?;
    let content: Vec<u16> = text.get_string(&txn).encode_utf16().collect();
    let mut result = Vec::with_capacity(edits.len());
    for edit in edits {
        let end = edit.index.checked_add(edit.delete).ok_or("invalid_range")?;
        let expected = content
            .get(edit.index as usize..end as usize)
            .ok_or("invalid_range")?;
        let start = text
            .sticky_index(&txn, edit.index, if edit.delete == 0 { Assoc::Before } else { Assoc::After })
            .ok_or("invalid_range")?;
        let end = text.sticky_index(&txn, end, Assoc::Before).ok_or("invalid_range")?;
        result.push(AnchoredEdit {
            start: STANDARD.encode(start.encode_v1()),
            end: STANDARD.encode(end.encode_v1()),
            expected: String::from_utf16(expected).map_err(|_| "invalid_unicode_boundary")?,
            insert: edit.insert,
        });
    }
    Ok(result)
}

fn resolve_anchored(doc: &Doc, edits: Vec<AnchoredEdit>, whole_lines: bool) -> Result<Vec<Edit>, String> {
    let txn = doc.transact();
    let deleted = txn.snapshot().delete_set;
    let text = txn.get_text("text").ok_or("invalid_schema")?;
    let content: Vec<u16> = text.get_string(&txn).encode_utf16().collect();
    let mut result = Vec::with_capacity(edits.len());
    for edit in edits {
        let start = StickyIndex::decode_v1(&decode(&edit.start)?).map_err(|_| "invalid_anchor")?;
        let end = StickyIndex::decode_v1(&decode(&edit.end)?).map_err(|_| "invalid_anchor")?;
        for anchor in [&start, &end] {
            if let IndexScope::Relative(id) = anchor.scope() {
                if deleted.contains(id) {
                    return Err("anchor_conflict".into());
                }
            }
        }
        let mut start = start.get_offset(&txn).ok_or("anchor_conflict")?;
        let mut end = end.get_offset(&txn).ok_or("anchor_conflict")?;
        if start.branch != end.branch {
            return Err("anchor_conflict".into());
        }
        if whole_lines && !edit.expected.is_empty() {
            while start.index > 0 && content.get(start.index as usize - 1) != Some(&10) {
                start.index -= 1;
            }
            if !edit.expected.ends_with('\n') {
                while let Some(value) = content.get(end.index as usize) {
                    end.index += 1;
                    if *value == 10 { break; }
                }
            }
        }
        let current = content
            .get(start.index as usize..end.index as usize)
            .ok_or("anchor_conflict")?;
        if current.iter().copied().ne(edit.expected.encode_utf16()) {
            return Err("anchor_conflict".into());
        }
        result.push(Edit {
            index: start.index,
            delete: end.index - start.index,
            insert: edit.insert,
        });
    }
    result.sort_by(|a, b| b.index.cmp(&a.index));
    Ok(result)
}

fn decode(value: &str) -> Result<Vec<u8>, String> {
    if value.len() > MAX_BYTES * 2 {
        return Err("update_too_large".into());
    }
    STANDARD.decode(value).map_err(|_| "invalid_base64".into())
}

fn apply(doc: &Doc, encoded: &str) -> Result<(), String> {
    if encoded.is_empty() {
        return Ok(());
    }
    let update = Update::decode_v1(&decode(encoded)?).map_err(|_| "invalid_update")?;
    doc.transact_mut()
        .apply_update(update)
        .map_err(|_| "invalid_update".into())
}

fn validate(doc: &Doc) -> Result<(), String> {
    let txn = doc.transact();
    if txn.root_refs().any(|(name, _)| name != "text") || txn.subdoc_guids().next().is_some() {
        return Err("invalid_schema".into());
    }
    if txn.store().pending_update().is_some() || txn.store().pending_ds().is_some() {
        return Err("missing_dependencies".into());
    }
    let text = txn.get_text("text").ok_or("invalid_schema")?;
    let mut bytes = 0;
    for chunk in text.diff(&txn, |_| ()) {
        let Out::Any(Any::String(content)) = chunk.insert else {
            return Err("invalid_schema".into());
        };
        if chunk.attributes.is_some() {
            return Err("invalid_schema".into());
        }
        bytes += content.len();
    }
    if bytes > MAX_BYTES {
        return Err("document_too_large".into());
    }
    Ok(())
}

fn edit(doc: &Doc, edits: Vec<Edit>) -> Result<String, String> {
    let text = doc.get_or_insert_text("text");
    let mut txn = doc.transact_mut();
    // Ranges refer to the same frozen UTF-16 snapshot, applied from right to left.
    let mut boundary = text.len(&txn);
    let content: Vec<u16> = text.get_string(&txn).encode_utf16().collect();
    let is_boundary = |offset: u32| {
        let offset = offset as usize;
        offset == 0
            || offset == content.len()
            || !((0xd800..=0xdbff).contains(&content[offset - 1])
                && (0xdc00..=0xdfff).contains(&content[offset]))
    };
    for e in &edits {
        if e.index
            .checked_add(e.delete)
            .is_none_or(|end| end > boundary)
        {
            return Err("invalid_range".into());
        }
        if !is_boundary(e.index) || !is_boundary(e.index + e.delete) {
            return Err("invalid_unicode_boundary".into());
        }
        boundary = e.index;
    }
    for e in edits {
        if e.delete != 0 {
            text.remove_range(&mut txn, e.index, e.delete);
        }
        if !e.insert.is_empty() {
            text.insert(&mut txn, e.index, &e.insert);
        }
    }
    Ok(STANDARD.encode(txn.encode_update_v1()))
}

fn undo_manager(doc: &Doc, encoded: &str) -> Result<yrs::undo::UndoManager<undo::Metadata>, String> {
    let items = undo::applicable(doc, encoded)?;
    let mut manager = yrs::undo::UndoManager::with_options(yrs::undo::Options {
        capture_timeout_millis: 0,
        tracked_origins: Default::default(),
        capture_transaction: None,
        timestamp: Arc::new(|| 1),
        init_undo_stack: items,
        init_redo_stack: Vec::new(),
    });
    manager.expand_scope(doc, &doc.get_or_insert_text("text"));
    Ok(manager)
}

fn new_document(client: u32) -> Doc {
    let mut options =
        Options::with_guid_and_client_id(Arc::from("document"), yrs::ClientID::new(client as u64));
    options.offset_kind = OffsetKind::Utf16;
    options.skip_gc = true;
    let doc = Doc::with_options(options);
    doc.get_or_insert_text("text");
    doc
}

fn execute(req: Request) -> Result<Response, String> {
    DOCUMENTS.with(|documents| {
        let mut documents = documents.borrow_mut();
        if req.action == "drop" {
            documents.remove(&req.handle);
            return Ok(Response::default());
        }
        if req.action == "open" {
            let doc = new_document(req.client);
            apply(&doc, &req.update)?;
            validate(&doc)?;
            documents.insert(req.handle, doc);
        }
        if req.action == "compact" {
            let doc = documents.get(&req.handle).ok_or("document_missing")?;
            let compacted = compaction::compact(doc, &req.retained_undo)?;
            documents.insert(req.handle, compacted);
        }
        if req.client != 0 && (req.action == "edit" || req.action == "undo") {
            let doc = documents.get(&req.handle).ok_or("document_missing")?;
            if doc.client_id().get() != req.client as u64 {
                let checkpoint = doc
                    .transact()
                    .encode_state_as_update_v1(&StateVector::default());
                let replacement = new_document(req.client);
                replacement
                    .transact_mut()
                    .apply_update(Update::decode_v1(&checkpoint).map_err(|_| "invalid_update")?)
                    .map_err(|_| "invalid_update")?;
                documents.insert(req.handle, replacement);
            }
        }
        let doc = documents.get(&req.handle).ok_or("document_missing")?;
        let capture = if req.capture_undo || req.action == "undo" {
            Some(undo::Capture::new(doc)?)
        } else { None };
        let mut manager = if req.capture_undo || req.track_changes || req.action == "undo" {
            Some(undo_manager(doc, &req.undo)?)
        } else {
            None
        };
        let mut delta = String::new();
        let mut anchors = Vec::new();
        let mut resolved = Vec::new();
        match req.action.as_str() {
            "open" | "inspect" | "compact" => (),
            "apply" => {
                let before = doc.transact().state_vector();
                apply(doc, &req.update)?;
                if req.client != 0 {
                    let after = doc.transact().state_vector();
                    for (client, clock) in after.iter() {
                        if client.get() != req.client as u64 && *clock > before.get(client) {
                            return Err("foreign_replica_update".into());
                        }
                    }
                }
                delta = req.update;
            }
            "edit" => delta = edit(doc, req.edits)?,
            "undo" => {
                let before = doc.transact().state_vector();
                manager.as_mut().ok_or("invalid_undo")?.undo_blocking();
                delta = STANDARD.encode(doc.transact().encode_state_as_update_v1(&before));
            }
            "anchor" => anchors = anchored_edits(doc, req.edits)?,
            "resolve" => {
                resolve_anchored(doc, req.guards, true)?;
                resolved = resolve_anchored(doc, req.anchors, false)?;
            }
            _ => return Err("invalid_action".into()),
        }
        if matches!(req.action.as_str(), "apply" | "edit" | "undo") {
            validate(doc)?;
        }
        let authors = if req.authorship {
            authorship::spans(doc)?
        } else {
            Vec::new()
        };
        let undo = if let (Some(manager), Some(capture)) = (&manager, &capture) {
            capture.encode(doc, if req.action == "undo" {
                    manager.redo_stack()
                } else {
                    manager.undo_stack()
                })?
        } else {
            String::new()
        };
        let txn = doc.transact();
        if !req.vector.is_empty() {
            let vector =
                StateVector::decode_v1(&decode(&req.vector)?).map_err(|_| "invalid_vector")?;
            delta = STANDARD.encode(txn.encode_state_as_update_v1(&vector));
        }
        let (inserted, deleted) = if let Some(manager) = &manager {
            authorship::changed_ranges(if req.action == "undo" { manager.redo_stack() } else { manager.undo_stack() })?
        } else { (Vec::new(), Vec::new()) };
        Ok(Response {
            error: None,
            undo_units: compaction::retained_units(&undo)?,
            undo,
            anchors,
            resolved,
            authors,
            inserted,
            deleted,
            text: if req.omit_text { String::new() } else { txn
                .get_text("text")
                .ok_or("invalid_schema")?
                .get_string(&txn) },
            vector: STANDARD.encode(txn.state_vector().encode_v1()),
            update: delta,
            checkpoint: if req.checkpoint {
                STANDARD.encode(txn.encode_state_as_update_v1(&StateVector::default()))
            } else {
                String::new()
            },
        })
    })
}

#[no_mangle]
pub extern "C" fn allocate(length: u32) -> u32 {
    if length as usize > MAX_BYTES * 2 {
        return 0;
    }
    Box::into_raw(vec![0u8; length as usize].into_boxed_slice()) as *mut u8 as u32
}

#[no_mangle]
pub unsafe extern "C" fn release(pointer: u32, length: u32) {
    drop(Box::from_raw(std::ptr::slice_from_raw_parts_mut(
        pointer as *mut u8,
        length as usize,
    )));
}

#[no_mangle]
pub unsafe extern "C" fn execute_request(pointer: u32, length: u32) -> u64 {
    let input = std::slice::from_raw_parts(pointer as *const u8, length as usize);
    let result = serde_json::from_slice(input)
        .map_err(|_| "invalid_request".into())
        .and_then(execute);
    let response = result.unwrap_or_else(|error| Response {
        error: Some(error),
        ..Response::default()
    });
    let bytes = serde_json::to_vec(&response)
        .expect("response is serializable")
        .into_boxed_slice();
    let length = bytes.len() as u32;
    let pointer = Box::into_raw(bytes) as *mut u8 as u32;
    ((pointer as u64) << 32) | length as u64
}
