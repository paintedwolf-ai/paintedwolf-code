//! Shared test helpers.

use std::fs;
use std::path::{Path, PathBuf};
use std::process;
use std::sync::atomic::{AtomicU64, Ordering};

/// Separates directories created by one process.
static NEXT_ID: AtomicU64 = AtomicU64::new(0);

/// A temp directory isolated to one test and removed when dropped.
pub struct TempDir {
    path: PathBuf,
}

impl TempDir {
    /// Creates a unique labeled directory.
    pub fn new(label: &str) -> Self {
        let path = std::env::temp_dir().join(format!(
            "paintedwolf-{label}-{}-{}",
            process::id(),
            NEXT_ID.fetch_add(1, Ordering::Relaxed)
        ));
        let _ = fs::remove_dir_all(&path);
        fs::create_dir_all(&path).expect("create temp dir");
        Self { path }
    }

    pub fn path(&self) -> &Path {
        &self.path
    }
}

impl AsRef<Path> for TempDir {
    fn as_ref(&self) -> &Path {
        &self.path
    }
}

impl std::ops::Deref for TempDir {
    type Target = Path;

    fn deref(&self) -> &Self::Target {
        &self.path
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.path);
    }
}

#[cfg(test)]
mod tests {
    use super::TempDir;

    #[test]
    fn directories_are_unique_and_removed_on_drop() {
        let first = TempDir::new("lifecycle");
        let second = TempDir::new("lifecycle");
        assert_ne!(first.path(), second.path());
        let first_path = first.path().to_path_buf();
        assert!(first_path.is_dir());

        drop(first);

        assert!(!first_path.exists());
        assert!(second.path().is_dir());
    }
}
