//! Read-only launcher status: exact package wrapper bytes and executable targets.
//! This is installation status, not credential or child-process admission.

use crate::sidecar::manifest::Manifest;
use std::path::Path;

const WRAPPER_LIMIT: u64 = 64 * 1024;

pub(crate) fn executable(path: &Path) -> bool {
    let Ok(info) = path.metadata() else {
        return false;
    };
    if !info.is_file() {
        return false;
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        info.permissions().mode() & 0o111 != 0
    }
    #[cfg(not(unix))]
    false
}

pub(crate) fn matches(root: &Path, entry_point: &Path) -> bool {
    verify(root, entry_point).is_some()
}

fn verify(root: &Path, entry_point: &Path) -> Option<()> {
    let role = entry_point.file_name()?.to_str()?;
    if !matches!(role, "pw" | "pw-logs")
        || !executable(entry_point)
        || !executable(&root.join("bin").join(role))
    {
        return None;
    }
    let manifest = Manifest::load(root).ok()?;
    let expected = manifest.executable(entry_point).ok()?;
    if expected.size > WRAPPER_LIMIT {
        return None;
    }
    expected.verify_file(entry_point).ok()?;
    Some(())
}
