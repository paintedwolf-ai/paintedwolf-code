//! Operating system pasteboard adapter for macOS and Windows.

use tokio::sync::oneshot;

use super::service::{
    ClipboardAdapter, ClipboardError, ClipboardFacts, DisplayProtocol, MainThread, Reply,
};

#[derive(Default)]
pub(crate) struct NativeClipboard {
    board: Option<arboard::Clipboard>,
    secret: Option<u64>,
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
        {
            let result = super::secret_macos::write(text);
            if let Ok(copy) = result {
                self.secret = Some(copy);
            }
            settled(result)
        }
        #[cfg(not(target_os = "macos"))]
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
        if self.secret == Some(copy) {
            self.secret = None;
        }
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
        #[cfg(target_os = "macos")]
        if let Some(copy) = self.secret.take() {
            super::secret_macos::clear(copy);
        }
        self.board.take();
    }
}
