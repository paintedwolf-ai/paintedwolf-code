use objc2::runtime::ProtocolObject;
use objc2_app_kit::{NSPasteboard, NSPasteboardItem, NSPasteboardWriting};
use objc2_foundation::{NSArray, NSString};

use super::service::ClipboardError;

const TEXT_TYPE: &str = "public.utf8-plain-text";
const SECRET_TYPES: [&str; 2] = [
    "org.nspasteboard.ConcealedType",
    "org.nspasteboard.TransientType",
];

pub(super) fn write(text: String) -> Result<u64, ClipboardError> {
    write_to(&NSPasteboard::generalPasteboard(), &text)
}

fn write_to(board: &NSPasteboard, text: &str) -> Result<u64, ClipboardError> {
    let item = NSPasteboardItem::new();
    if !item.setString_forType(&NSString::from_str(text), &NSString::from_str(TEXT_TYPE)) {
        return Err(ClipboardError::Backend(
            "Could not prepare clipboard text".into(),
        ));
    }
    for hint in SECRET_TYPES {
        if !item.setString_forType(&NSString::from_str(""), &NSString::from_str(hint)) {
            return Err(ClipboardError::Backend(
                "Could not mark clipboard text as secret".into(),
            ));
        }
    }
    let writing: &ProtocolObject<dyn NSPasteboardWriting> = ProtocolObject::from_ref(&*item);
    board.clearContents();
    if !board.writeObjects(&NSArray::from_slice(&[writing])) {
        return Err(ClipboardError::Backend("Could not copy secret text".into()));
    }
    Ok(board.changeCount() as u64)
}

pub(super) fn clear(copy: u64) {
    clear_from(&NSPasteboard::generalPasteboard(), copy);
}

fn clear_from(board: &NSPasteboard, copy: u64) {
    if board.changeCount() as u64 == copy {
        board.clearContents();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn secret_item_has_hints_and_clears_only_its_own_generation() {
        let board = NSPasteboard::pasteboardWithUniqueName();
        let first = write_to(&board, "first").unwrap();
        let items = board.pasteboardItems().unwrap();
        let item = items.objectAtIndex(0);
        for hint in SECRET_TYPES {
            assert!(item.stringForType(&NSString::from_str(hint)).is_some());
        }
        let second = write_to(&board, "second").unwrap();
        clear_from(&board, first);
        assert_eq!(board.changeCount() as u64, second);
        board.clearContents();
        board.setString_forType(
            &NSString::from_str("other app"),
            &NSString::from_str(TEXT_TYPE),
        );
        let other = board.changeCount();
        clear_from(&board, second);
        assert_eq!(board.changeCount(), other);
        let owned = write_to(&board, "owned").unwrap();
        clear_from(&board, owned);
        assert!(board.pasteboardItems().is_none_or(|items| items.is_empty()));
    }
}
