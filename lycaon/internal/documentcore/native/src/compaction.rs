use yrs::updates::{
    decoder::Decode,
    encoder::{Encoder, EncoderV1},
};
use yrs::{Doc, IdSet, ReadTxn, Transact};

pub fn retained_units(encoded: &str) -> Result<u64, String> {
    if encoded.is_empty() {
        return Ok(0);
    }
    let items = super::undo::decode(encoded)?;
    Ok(items
        .iter()
        .flat_map(|item| item.deletions().iter())
        .flat_map(|(_, ranges)| ranges.iter())
        .map(|range| u64::from(range.end - range.start))
        .sum())
}

pub fn compact(doc: &Doc, retained: &[String]) -> Result<Doc, String> {
    let mut protected = IdSet::new();
    for encoded in retained {
        let items = super::undo::decode(encoded)?;
        for item in items {
            protected.merge_with(item.deletions().clone());
        }
    }
    let txn = doc.transact();
    let original = txn.encode_state_as_update_v1(&yrs::StateVector::default());
    let mut snapshot = txn.snapshot();
    snapshot.delete_set.diff_with(&protected);
    let mut encoder = EncoderV1::new();
    txn.encode_state_from_snapshot(&snapshot, &mut encoder)
        .map_err(|_| "invalid_snapshot")?;
    let fresh = super::new_document(doc.client_id().get() as u32);
    // Keep retained undo content visible while collecting the other tombstones.
    // Reapplying the original delete set restores exactly the accepted state.
    fresh
        .transact_mut()
        .apply_update(yrs::Update::decode_v1(&encoder.to_vec()).map_err(|_| "invalid_update")?)
        .map_err(|_| "invalid_update")?;
    fresh.transact_mut().gc(None);
    fresh
        .transact_mut()
        .apply_update(yrs::Update::decode_v1(&original).map_err(|_| "invalid_update")?)
        .map_err(|_| "invalid_update")?;
    Ok(fresh)
}
