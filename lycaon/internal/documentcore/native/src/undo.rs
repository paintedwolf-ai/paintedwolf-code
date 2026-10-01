use std::{cell::RefCell, rc::Rc};

use base64::{engine::general_purpose::STANDARD, Engine};
use serde::{Deserialize, Serialize};
use yrs::{types::Delta, Any, Doc, IdSet, Observable, Out, ReadTxn, Subscription, Text, Transact, ID};

use super::authorship::Span;

#[derive(Clone, Default, Deserialize, Serialize)]
pub struct Metadata {
    groups: Vec<Group>,
}

#[derive(Clone, Deserialize, Serialize)]
struct Group {
    inserted: IdSet,
    deleted: IdSet,
    left: Option<ID>,
    right: Option<ID>,
}

pub type Item = yrs::undo::StackItem<Metadata>;

pub fn decode(encoded: &str) -> Result<Vec<Item>, String> {
    if encoded.is_empty() { return Ok(Vec::new()); }
    serde_json::from_slice(&super::decode(encoded)?).map_err(|_| "invalid_undo".into())
}

pub fn applicable(doc: &Doc, encoded: &str) -> Result<Vec<Item>, String> {
    let recorded = decode(encoded)?;
    if recorded.is_empty() { return Ok(recorded); }
    let txn = doc.transact();
    let snapshot = txn.snapshot();
    let mut items = Vec::new();
    for item in recorded {
        let mut deletions = IdSet::new();
        for group in &item.meta.groups {
            let mut remaining = group.inserted.clone();
            remaining.diff_with(&snapshot.delete_set);
            // A replacement that was itself replaced no longer owns a restoration.
            let restore = if !group.inserted.is_empty() {
                !remaining.is_empty()
            } else {
                [group.left, group.right].into_iter().flatten().any(|id| !snapshot.delete_set.contains(&id))
                    || (group.left.is_none() && group.right.is_none()
                        && txn.get_text("text").ok_or("invalid_schema")?.len(&txn) == 0)
            };
            if restore { deletions.merge_with(group.deleted.clone()); }
        }
        items.push(Item::with_meta(doc.guid(), deletions, item.insertions().clone(), Metadata::default()));
    }
    Ok(items)
}

pub struct Capture {
    before: Vec<Span>,
    delta: Rc<RefCell<Vec<Delta>>>,
    _subscription: Subscription,
}

impl Capture {
    pub fn new(doc: &Doc) -> Result<Self, String> {
        let before = super::authorship::spans(doc)?;
        let delta = Rc::new(RefCell::new(Vec::new()));
        let observed = delta.clone();
        let subscription = doc.get_or_insert_text("text").observe(move |txn, event| {
            *observed.borrow_mut() = event.delta(txn).to_vec();
        });
        Ok(Self { before, delta, _subscription: subscription })
    }

    pub fn encode(&self, doc: &Doc, stack: &[Item]) -> Result<String, String> {
        let after = super::authorship::spans(doc)?;
        let mut groups = Vec::new();
        let (mut old, mut new, mut deleted, mut inserted) = (0, 0, 0, 0);
        let delta = self.delta.borrow();
        for part in delta.iter().chain(std::iter::once(&Delta::Retain(0, None))) {
            match part {
                Delta::Deleted(len) => deleted += len,
                Delta::Inserted(Out::Any(Any::String(text)), _) => inserted += text.encode_utf16().count() as u32,
                Delta::Retain(len, _) => {
                    if deleted != 0 || inserted != 0 {
                        groups.push(Group {
                            inserted: identities(&after, new, inserted),
                            deleted: identities(&self.before, old, deleted),
                            left: old.checked_sub(1).and_then(|index| identity(&self.before, index)),
                            right: identity(&self.before, old + deleted),
                        });
                    }
                    old += deleted + len;
                    new += inserted + len;
                    deleted = 0;
                    inserted = 0;
                }
                _ => return Err("invalid_schema".into()),
            }
        }
        let mut stack = stack.to_vec();
        if let Some(item) = stack.last_mut() { item.meta = Metadata { groups }; }
        Ok(STANDARD.encode(serde_json::to_vec(&stack).map_err(|_| "invalid_undo")?))
    }
}

fn identity(spans: &[Span], index: u32) -> Option<ID> {
    spans.get(spans.partition_point(|span| span.index + span.length <= index))
        .filter(|span| span.index <= index)
        .map(|span| ID::new(yrs::ClientID::new(span.client as u64), span.clock + index - span.index))
}

fn identities(spans: &[Span], start: u32, length: u32) -> IdSet {
    let mut ids = IdSet::new();
    for span in &spans[spans.partition_point(|span| span.index + span.length <= start)..] {
        if span.index >= start + length { break; }
        let from = start.max(span.index);
        let to = (start + length).min(span.index + span.length);
        if from < to {
            ids.insert(ID::new(yrs::ClientID::new(span.client as u64), span.clock + from - span.index), to - from);
        }
    }
    ids
}
