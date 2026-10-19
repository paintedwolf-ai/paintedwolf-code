//! Facts about a Linux installation that the system package manager owns.
//!
//! The package database, not the executable's directory name, decides whether
//! this process runs from the `.deb` or `.rpm` package. Layout constants here
//! mirror `packaging/linux/` and the Go engine's `configlayout`.

pub(crate) mod entry_points;

use serde::Serialize;
use std::path::{Path, PathBuf};
use std::sync::OnceLock;
mod pacman;

pub const INSTALL_ROOT: &str = crate::installation_mode::LINUX_PACKAGE_ROOT;
pub const PACKAGE_NAME: &str = "painted-wolf-code";
const BIN_DIR: &str = "bin";
const ENGINE_ROOT_DIR: &str = "engine-root";
const ENGINE_MARKER: &str = "schemas/oar/oar.schema.json";

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum PackageFamily {
    Deb,
    Rpm,
    Nix,
    Pacman,
}

impl PackageFamily {
    /// The exact command the person runs to move to the version the feed announces.
    pub fn update_command(self, package: &str) -> String {
        match self {
            Self::Deb => format!("sudo apt update && sudo apt install --only-upgrade {package}"),
            Self::Rpm => format!("sudo dnf upgrade {package}"),
            Self::Nix => "Update the Nix profile, Home Manager configuration, or NixOS configuration that owns this installation.".into(),
            Self::Pacman => "sudo pacman -Syu".into(),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct InstalledPackage {
    pub family: PackageFamily,
    pub name: String,
    pub version: String,
    pub update_command: String,
}

impl InstalledPackage {
    fn new(family: PackageFamily, name: String, version: String) -> Self {
        let update_command = family.update_command(&name);
        Self {
            family,
            name,
            version,
            update_command,
        }
    }
}

/// Paths the package installs under its root; `None` when any is missing.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PackageLayout {
    pub engine: PathBuf,
    pub log_viewer: PathBuf,
    pub engine_root: PathBuf,
}

pub fn package_layout(install_root: &Path) -> Option<PackageLayout> {
    let layout = PackageLayout {
        engine: install_root.join(BIN_DIR).join("pw"),
        log_viewer: install_root.join(BIN_DIR).join("pw-logs"),
        engine_root: install_root.join(ENGINE_ROOT_DIR),
    };
    let present = layout.engine.is_file()
        && layout.log_viewer.is_file()
        && layout.engine_root.join(ENGINE_MARKER).is_file();
    present.then_some(layout)
}

/// The package that owns the running executable, resolved once per process.
pub fn installed() -> Option<&'static InstalledPackage> {
    static INSTALLED: OnceLock<Option<InstalledPackage>> = OnceLock::new();
    INSTALLED
        .get_or_init(|| {
            if !cfg!(target_os = "linux") {
                return None;
            }
            let executable = std::env::current_exe().ok()?;
            if let Ok(Some(generation)) = crate::linux_generation::running() {
                return Some(InstalledPackage::new(PackageFamily::Nix, PACKAGE_NAME.into(), generation.version.clone()));
            }
            owner(&executable)
        })
        .as_ref()
}

fn owner(executable: &Path) -> Option<InstalledPackage> {
    if !executable.is_absolute() {
        return None;
    }
    query_dpkg(executable).or_else(|| query_rpm(executable)).or_else(|| pacman::owner(executable))
}

fn run(program: &str, args: &[&str]) -> Option<String> {
    let output = std::process::Command::new(program)
        .args(args)
        .env_clear()
        .env("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
        .env("LC_ALL", "C")
        .stdin(std::process::Stdio::null())
        .output()
        .ok()?;
    output
        .status
        .success()
        .then(|| String::from_utf8_lossy(&output.stdout).into_owned())
}

fn query_dpkg(executable: &Path) -> Option<InstalledPackage> {
    let search = run("dpkg-query", &["-S", &executable.to_string_lossy()])?;
    let name = parse_dpkg_owner(&search, executable)?;
    let version = run("dpkg-query", &["-W", "-f=${Version}", &name])?;
    let version = version.trim();
    (!version.is_empty())
        .then(|| InstalledPackage::new(PackageFamily::Deb, name, version.to_owned()))
}

/// `dpkg-query -S` prints `name[:arch][, name...]: path` per owning package.
fn parse_dpkg_owner(output: &str, executable: &Path) -> Option<String> {
    let wanted = executable.to_string_lossy();
    output.lines().find_map(|line| {
        let (owners, path) = line.rsplit_once(": ")?;
        if path.trim() != wanted {
            return None;
        }
        let first = owners.split(',').next()?.trim();
        let name = first.split(':').next()?.trim();
        (!name.is_empty()).then(|| name.to_owned())
    })
}

fn query_rpm(executable: &Path) -> Option<InstalledPackage> {
    let output = run(
        "rpm",
        &[
            "-qf",
            "--qf",
            "%{NAME}\t%{VERSION}\n",
            &executable.to_string_lossy(),
        ],
    )?;
    parse_rpm_owner(&output)
}

/// `rpm -qf --qf '%{NAME}\t%{VERSION}\n'` prints one line per owning package.
fn parse_rpm_owner(output: &str) -> Option<InstalledPackage> {
    let line = output.lines().find(|line| !line.trim().is_empty())?;
    let (name, version) = line.split_once('\t')?;
    let (name, version) = (name.trim(), version.trim());
    (!name.is_empty() && !version.is_empty())
        .then(|| InstalledPackage::new(PackageFamily::Rpm, name.to_owned(), version.to_owned()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn dpkg_owner_is_read_only_for_the_queried_path() {
        let exe = Path::new("/opt/painted-wolf-code/bin/painted-wolf-code");
        assert_eq!(
            parse_dpkg_owner("painted-wolf-code: /opt/painted-wolf-code/bin/painted-wolf-code\n", exe).as_deref(),
            Some("painted-wolf-code")
        );
        assert_eq!(
            parse_dpkg_owner(
                "painted-wolf-code:amd64, other: /opt/painted-wolf-code/bin/painted-wolf-code\n",
                exe
            )
            .as_deref(),
            Some("painted-wolf-code")
        );
        assert_eq!(
            parse_dpkg_owner("diversion by foo from: /opt/painted-wolf-code/bin/painted-wolf-code\n", exe).as_deref(),
            Some("diversion by foo from")
        );
        assert!(parse_dpkg_owner("other: /usr/bin/other\n", exe).is_none());
        assert!(parse_dpkg_owner("dpkg-query: no path found matching pattern\n", exe).is_none());
        assert!(parse_dpkg_owner("", exe).is_none());
    }

    #[test]
    fn rpm_owner_carries_the_package_version_and_command() {
        let package = parse_rpm_owner("painted-wolf-code\t1.0.2~rc3\n").unwrap();
        assert_eq!(package.family, PackageFamily::Rpm);
        assert_eq!(package.name, "painted-wolf-code");
        assert_eq!(package.version, "1.0.2~rc3");
        assert_eq!(package.update_command, "sudo dnf upgrade painted-wolf-code");
        assert!(parse_rpm_owner("file /usr/bin/x is not owned by any package\n").is_none());
        assert!(parse_rpm_owner("\n").is_none());
    }

    #[test]
    fn update_commands_name_the_package_for_each_family() {
        assert_eq!(
            PackageFamily::Deb.update_command(PACKAGE_NAME),
            "sudo apt update && sudo apt install --only-upgrade painted-wolf-code"
        );
        assert_eq!(
            serde_json::to_value(PackageFamily::Deb).unwrap(),
            serde_json::json!("deb")
        );
    }

    #[test]
    fn package_layout_requires_every_installed_path() {
        let root = crate::test_support::TempDir::new("linux-package-layout");
        assert!(package_layout(&root).is_none());
        std::fs::create_dir_all(root.join("bin")).unwrap();
        std::fs::create_dir_all(root.join("engine-root/schemas/oar")).unwrap();
        std::fs::write(root.join("bin/pw"), b"engine").unwrap();
        std::fs::write(root.join("bin/pw-logs"), b"logs").unwrap();
        assert!(package_layout(&root).is_none());
        std::fs::write(root.join("engine-root/schemas/oar/oar.schema.json"), b"{}").unwrap();
        let layout = package_layout(&root).unwrap();
        assert_eq!(layout.engine, root.join("bin/pw"));
        assert_eq!(layout.engine_root, root.join("engine-root"));
    }

    #[test]
    fn relative_executables_are_never_package_owned() {
        assert!(owner(Path::new("painted-wolf-code")).is_none());
    }
}
