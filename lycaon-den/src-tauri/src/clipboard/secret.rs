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

#[derive(serde::Deserialize)]
struct ClipboardPolicy {
    lifetime_ms: u64,
}

pub(super) fn lifetime() -> std::time::Duration {
    static POLICY: std::sync::LazyLock<std::time::Duration> = std::sync::LazyLock::new(|| {
        let policy: ClipboardPolicy =
            serde_json::from_str(include_str!("../../../shared/secret-clipboard.json"))
                .expect("valid clipboard policy");
        std::time::Duration::from_millis(policy.lifetime_ms)
    });
    *POLICY
}
