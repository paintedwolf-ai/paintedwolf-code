use arboard::SetExtWindows;
use windows_sys::Win32::System::DataExchange::GetClipboardSequenceNumber;

use super::service::ClipboardError;

pub(super) fn write(board: &mut arboard::Clipboard, text: String) -> Result<u64, ClipboardError> {
    board
        .set()
        .exclude_from_monitoring()
        .text(text)
        .map_err(|error| ClipboardError::Backend(error.to_string()))?;
    let copy = unsafe { GetClipboardSequenceNumber() } as u64;
    tokio::spawn(async move {
        tokio::time::sleep(super::secret::lifetime()).await;
        let _ = clear(copy);
    });
    Ok(copy)
}

pub(super) fn clear(copy: u64) -> Result<(), ClipboardError> {
    if copy == 0 || unsafe { GetClipboardSequenceNumber() } as u64 != copy {
        return Ok(());
    }
    arboard::Clipboard::new()
        .and_then(|mut board| board.clear())
        .map_err(|error| ClipboardError::Backend(error.to_string()))
}
