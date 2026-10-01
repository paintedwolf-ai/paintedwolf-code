//! Resolves the host config root for each build channel.

use std::path::PathBuf;

/// Profile segment for release builds.
pub const DIR_NAME_PROD: &str = "paintedwolf";
/// Profile segment for development builds.
pub const DIR_NAME_DEV: &str = "paintedwolf-dev";

fn is_dev_channel() -> bool {
    cfg!(debug_assertions)
}

/// Leaf name for this process's build channel.
pub fn channel_dir_name() -> &'static str {
    if is_dev_channel() {
        DIR_NAME_DEV
    } else {
        DIR_NAME_PROD
    }
}

/// Resolve the host config directory for this process.
pub fn host_config_dir() -> Result<PathBuf, String> {
    let raw_override = std::env::var("LYCAON_CONFIG_DIR").ok();
    if let Some(dir) = config_dir_override_for_build(!is_dev_channel(), raw_override.as_deref()) {
        return Ok(dir);
    }
    let home = std::env::var("HOME")
        .or_else(|_| std::env::var("USERPROFILE"))
        .map_err(|_| "no home dir".to_string())?;
    Ok(PathBuf::from(home).join(".config").join(channel_dir_name()))
}

fn config_dir_override_for_build(release: bool, raw: Option<&str>) -> Option<PathBuf> {
    if release {
        return None;
    }
    raw.map(str::trim)
        .filter(|value| !value.is_empty())
        .map(PathBuf::from)
}

/// Create the host config directory with private permissions.
pub fn ensure_host_config_dir() -> Result<PathBuf, String> {
    let dir = host_config_dir()?;
    ensure_private_dir(&dir).map_err(|error| error.to_string())?;
    Ok(dir)
}

/// Create `dir` and enforce private permissions.
pub fn ensure_private_dir(dir: &std::path::Path) -> std::io::Result<()> {
    std::fs::create_dir_all(dir)?;
    restrict_config_dir(dir)
}

/// Private mode for the configuration root.
#[cfg(unix)]
pub const CONFIG_DIR_MODE: u32 = 0o700;

#[cfg(unix)]
fn restrict_config_dir(dir: &std::path::Path) -> std::io::Result<()> {
    use std::os::unix::fs::PermissionsExt;

    // `create_dir_all` leaves an existing directory's mode alone.
    if std::fs::metadata(dir)?.permissions().mode() & 0o777 == CONFIG_DIR_MODE {
        return Ok(());
    }
    std::fs::set_permissions(dir, std::fs::Permissions::from_mode(CONFIG_DIR_MODE))
}

/// Non-Unix platforms use the profile directory ACL.
#[cfg(not(unix))]
fn restrict_config_dir(_dir: &std::path::Path) -> std::io::Result<()> {
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_support::TempDir;

    #[test]
    fn leaf_names_match_product_ssot() {
        assert_eq!(DIR_NAME_PROD, "paintedwolf");
        assert_eq!(DIR_NAME_DEV, "paintedwolf-dev");
        assert_eq!(channel_dir_name(), DIR_NAME_DEV);
    }

    #[test]
    fn override_wins() {
        // SAFETY: test-only env mutation in a single-threaded unit test.
        unsafe {
            std::env::set_var("LYCAON_CONFIG_DIR", "/tmp/paintedwolf-config-dir-test");
        }
        let got = host_config_dir().expect("host_config_dir");
        unsafe {
            std::env::remove_var("LYCAON_CONFIG_DIR");
        }
        assert_eq!(got, PathBuf::from("/tmp/paintedwolf-config-dir-test"));
    }

    #[cfg(unix)]
    #[test]
    fn an_existing_world_readable_root_is_tightened() {
        use std::os::unix::fs::PermissionsExt;

        let dir = TempDir::new("config-mode");
        std::fs::set_permissions(&dir, std::fs::Permissions::from_mode(0o755)).expect("loosen dir");

        restrict_config_dir(&dir).expect("restrict_config_dir");

        let mode = std::fs::metadata(&dir)
            .expect("metadata")
            .permissions()
            .mode()
            & 0o777;
        assert_eq!(mode, CONFIG_DIR_MODE);
    }

    #[test]
    fn release_ignores_override() {
        assert_eq!(
            config_dir_override_for_build(true, Some("/tmp/override")),
            None
        );
    }
}
