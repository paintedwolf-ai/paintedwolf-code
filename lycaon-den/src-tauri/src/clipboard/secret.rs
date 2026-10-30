use super::service::{ClipboardError, ClipboardService, MainThread};
use super::NativeClipboard;

impl ClipboardService<NativeClipboard> {
    pub(super) async fn write_secret(
        &self,
        main: &dyn MainThread,
        text: String,
    ) -> Result<u64, ClipboardError> {
        self.run(main, move |adapter, main| {
            adapter.write_secret_text(main, text)
        })
        .await
    }

    pub(super) async fn clear_secret(
        &self,
        main: &dyn MainThread,
        copy: u64,
    ) -> Result<(), ClipboardError> {
        self.run(main, move |adapter, main| {
            adapter.clear_secret_text(main, copy)
        })
        .await
    }
}
