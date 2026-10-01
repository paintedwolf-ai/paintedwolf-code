use std::path::Path;

#[cfg(not(target_os = "windows"))]
pub fn replace(tmp: &Path, target: &Path, durable: bool) -> std::io::Result<()> {
    std::fs::rename(tmp, target)?;
    if durable {
        let parent = target.parent().ok_or_else(|| {
            std::io::Error::new(
                std::io::ErrorKind::InvalidInput,
                "atomic file target has no parent",
            )
        })?;
        std::fs::File::open(parent).and_then(|directory| directory.sync_all())?;
    }
    Ok(())
}

#[cfg(target_os = "windows")]
pub fn replace(tmp: &Path, target: &Path, durable: bool) -> std::io::Result<()> {
    use std::os::windows::ffi::OsStrExt;
    use windows_sys::Win32::Storage::FileSystem::{
        MoveFileExW, MOVEFILE_REPLACE_EXISTING, MOVEFILE_WRITE_THROUGH,
    };

    let source: Vec<u16> = tmp.as_os_str().encode_wide().chain(Some(0)).collect();
    let destination: Vec<u16> = target.as_os_str().encode_wide().chain(Some(0)).collect();
    let flags = MOVEFILE_REPLACE_EXISTING | if durable { MOVEFILE_WRITE_THROUGH } else { 0 };
    let moved = unsafe { MoveFileExW(source.as_ptr(), destination.as_ptr(), flags) };
    if moved == 0 {
        return Err(std::io::Error::last_os_error());
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_support::TempDir;
    use std::fs;

    #[test]
    fn replaces_an_existing_file() {
        let dir = TempDir::new("atomic-file-replace");
        let target = dir.join("target");
        let tmp = dir.join("target.tmp");
        fs::write(&target, "first").expect("write target");
        fs::write(&tmp, "second").expect("write temp");

        replace(&tmp, &target, true).expect("replace target");

        assert_eq!(fs::read_to_string(target).expect("read target"), "second");
        assert!(!tmp.exists());
    }
}
