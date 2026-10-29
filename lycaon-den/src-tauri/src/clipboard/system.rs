//! Operating system pasteboard adapter for macOS and Windows.

use tokio::sync::oneshot;

use super::service::{
    ClipboardAdapter, ClipboardError, ClipboardFacts, DisplayProtocol, MainThread, Reply,
};

#[derive(Default)]
pub(crate) struct NativeClipboard {
    board: Option<arboard::Clipboard>,
}

pub(crate) fn startup_facts() -> Result<ClipboardFacts, ClipboardError> {
    Ok(ClipboardFacts {
        protocol: DisplayProtocol::System,
        persistence: true,
    })
}

fn settled<T>(result: Result<T, ClipboardError>) -> Reply<T> {
    let (reply, receiver) = oneshot::channel();
    let _ = reply.send(result);
    receiver
}

impl NativeClipboard {
    pub(super) fn write_secret_text(&mut self, _main: &dyn MainThread, text: String) -> Reply<u64> {
        #[cfg(target_os = "macos")]
        return settled(super::secret_macos::write(text));
        #[cfg(target_os = "windows")]
        return settled(
            self.board()
                .and_then(|board| super::secret_windows::write(board, text)),
        );
        #[cfg(not(any(target_os = "macos", target_os = "windows")))]
        {
            let _ = text;
            settled(Err(ClipboardError::Backend(
                "Secret clipboard is unavailable on this platform".into(),
            )))
        }
    }

    pub(super) fn clear_secret_text(&mut self, _main: &dyn MainThread, copy: u64) -> Reply<()> {
        #[cfg(target_os = "macos")]
        super::secret_macos::clear(copy);
        #[cfg(target_os = "windows")]
        return settled(super::secret_windows::clear(copy));
        #[cfg(not(any(target_os = "macos", target_os = "windows")))]
        let _ = copy;
        #[cfg(not(target_os = "windows"))]
        settled(Ok(()))
    }

    fn board(&mut self) -> Result<&mut arboard::Clipboard, ClipboardError> {
        if self.board.is_none() {
            let board =
                arboard::Clipboard::new().map_err(|e| ClipboardError::Backend(e.to_string()))?;
            self.board = Some(board);
        }
        Ok(self.board.as_mut().expect("pasteboard opened"))
    }
}

impl ClipboardAdapter for NativeClipboard {
    fn write_text(&mut self, _main: &dyn MainThread, text: String) -> Reply<()> {
        settled(self.board().and_then(|board| {
            board
                .set_text(text)
                .map_err(|e| ClipboardError::Backend(e.to_string()))
        }))
    }

    /// Empty when the board has no text.
    fn read_text(&mut self, _main: &dyn MainThread) -> Reply<String> {
        settled(self.board().and_then(|board| match board.get_text() {
            Ok(text) => Ok(text),
            Err(arboard::Error::ContentNotAvailable) => Ok(String::new()),
            Err(e) => Err(ClipboardError::Backend(e.to_string())),
        }))
    }

    fn release(&mut self, _main: &dyn MainThread) {
        self.board.take();
    }
}
