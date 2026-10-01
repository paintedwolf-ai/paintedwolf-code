use serde::Serialize;
use yrs::types::text::YChange;
use yrs::{Any, Doc, IdSet, Out, ReadTxn, Snapshot, Text, Transact};

#[derive(Serialize)]
pub struct IdentityRange {
    pub client: u32,
    pub start: u32,
    pub end: u32,
}

pub fn changed_ranges(items: &[super::undo::Item]) -> Result<(Vec<IdentityRange>, Vec<IdentityRange>), String> {
    let mut inserted = IdSet::new();
    let mut deleted = IdSet::new();
    for item in items {
        inserted.merge_with(item.insertions().clone());
        deleted.merge_with(item.deletions().clone());
    }
    Ok((ranges(&inserted)?, ranges(&deleted)?))
}

fn ranges(ids: &IdSet) -> Result<Vec<IdentityRange>, String> {
    let mut result = Vec::new();
    for (client, ranges) in ids.iter() {
        let client = u32::try_from(client.get()).map_err(|_| "invalid_replica")?;
        for range in ranges.iter() {
            result.push(IdentityRange { client, start: range.start, end: range.end });
        }
    }
    Ok(result)
}

#[derive(Serialize)]
pub struct Span {
    pub index: u32,
    pub length: u32,
    pub client: u32,
    pub clock: u32,
}

pub fn spans(doc: &Doc) -> Result<Vec<Span>, String> {
    let mut txn = doc.transact_mut();
    let current = txn.snapshot();
    let empty = Snapshot::default();
    let text = txn.get_text("text").ok_or("invalid_schema")?;
    let chunks = text.diff_range(&mut txn, Some(&current), Some(&empty), YChange::identity);
    let mut index = 0;
    let mut result = Vec::with_capacity(chunks.len());
    for chunk in chunks {
        let Out::Any(Any::String(content)) = chunk.insert else {
            return Err("invalid_schema".into());
        };
        let change = chunk.ychange.ok_or("missing_authorship")?;
        let length = content.encode_utf16().count() as u32;
        result.push(Span {
            index,
            length,
            client: u32::try_from(change.id.client.get()).map_err(|_| "invalid_replica")?,
            clock: change.id.clock,
        });
        index += length;
    }
    Ok(result)
}
