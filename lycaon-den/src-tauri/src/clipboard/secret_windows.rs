use arboard::SetExtWindows;
use windows_sys::Win32::System::DataExchange::GetClipboardSequenceNumber;

use super::service::ClipboardError;

pub(super) fn write(board: &mut arboard::Clipboard, text: String) -> Result<u64, ClipboardError> {
    board
        .set()
        .exclude_from_monitoring()
        .exclude_from_history()
        .exclude_from_cloud()
        .text(text)
        .map_err(|error| ClipboardError::Backend(error.to_string()))?;
    // SAFETY: takes no arguments and only reads the clipboard's change counter.
    Ok(unsafe { GetClipboardSequenceNumber() } as u64)
}

pub(super) fn clear(copy: u64) -> Result<(), ClipboardError> {
    // SAFETY: as in `write`.
    if copy == 0 || unsafe { GetClipboardSequenceNumber() } as u64 != copy {
        return Ok(());
    }
    arboard::Clipboard::new()
        .and_then(|mut board| board.clear())
        .map_err(|error| ClipboardError::Backend(error.to_string()))
}
