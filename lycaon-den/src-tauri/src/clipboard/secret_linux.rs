use std::cell::Cell;
use std::sync::atomic::{AtomicU64, Ordering};

use gtk::prelude::*;

use super::service::ClipboardError;

const SECRET_HINT: &str = "x-kde-passwordManagerHint";
const TEXT_TARGETS: [&str; 6] = [
    "UTF8_STRING",
    "COMPOUND_TEXT",
    "TEXT",
    "STRING",
    "text/plain;charset=utf-8",
    "text/plain",
];
static NEXT_COPY: AtomicU64 = AtomicU64::new(1);
thread_local! { static OWNED_COPY: Cell<Option<u64>> = const { Cell::new(None) }; }

// GTK drops the selection callback when this application loses ownership.
struct SelectionOwner(u64);
impl Drop for SelectionOwner {
    fn drop(&mut self) {
        OWNED_COPY.with(|copy| {
            if copy.get() == Some(self.0) {
                copy.set(None);
            }
        });
    }
}

fn target_specs() -> Vec<(&'static str, u32)> {
    let mut targets: Vec<_> = TEXT_TARGETS.iter().map(|name| (*name, 0)).collect();
    targets.push((SECRET_HINT, 1));
    targets
}

fn targets() -> Vec<gtk::TargetEntry> {
    target_specs()
        .iter()
        .map(|(name, info)| gtk::TargetEntry::new(name, gtk::TargetFlags::empty(), *info))
        .collect()
}

pub(super) fn write(clipboard: &gtk::Clipboard, text: String) -> Result<u64, ClipboardError> {
    let copy = NEXT_COPY.fetch_add(1, Ordering::Relaxed);
    let owner = SelectionOwner(copy);
    if !clipboard.set_with_data(&targets(), move |_, selection, target| {
        let _owner = &owner;
        if target == 1 {
            selection.set(&gtk::gdk::Atom::intern(SECRET_HINT), 8, b"secret");
        } else {
            selection.set_text(&text);
        }
    }) {
        return Err(ClipboardError::Backend("Could not copy secret text".into()));
    }
    OWNED_COPY.with(|owned| owned.set(Some(copy)));
    Ok(copy)
}

pub(super) fn clear(clipboard: &gtk::Clipboard, copy: u64) {
    OWNED_COPY.with(|owned| {
        if owned.get() == Some(copy) {
            clipboard.clear();
            owned.set(None);
        }
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn secret_targets_include_text_and_history_hint() {
        let entries = target_specs();
        assert_eq!(entries.len(), TEXT_TARGETS.len() + 1);
        assert!(entries
            .iter()
            .any(|entry| entry.0 == SECRET_HINT && entry.1 == 1));
        assert!(entries
            .iter()
            .any(|entry| entry.0 == "UTF8_STRING" && entry.1 == 0));
    }

    #[test]
    fn losing_an_old_selection_cannot_invalidate_a_new_copy() {
        OWNED_COPY.with(|owned| owned.set(Some(2)));
        drop(SelectionOwner(1));
        OWNED_COPY.with(|owned| assert_eq!(owned.get(), Some(2)));
        drop(SelectionOwner(2));
        OWNED_COPY.with(|owned| assert_eq!(owned.get(), None));
    }
}
