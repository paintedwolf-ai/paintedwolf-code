//! Facts about a Linux installation that the system package manager owns.
//!
//! The package database, not the executable's directory name, decides whether
//! this process runs from the `.deb` or `.rpm` package. Layout constants here
//! mirror `packaging/linux/` and the Go engine's `configlayout`.

use serde::Serialize;
use std::path::{Path, PathBuf};
use std::sync::OnceLock;

mod source;
pub use source::{PackageSourceFacts, QueryReason};
pub use source::refresh as refresh_source;

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
    Pacman,
    Nix,
}

impl PackageFamily {
    /// The package owner's general update workflow; it is not a source offer.
    pub fn update_command(self, package: &str) -> String {
        match self {
            Self::Deb => format!("sudo apt update && sudo apt install --only-upgrade {package}"),
            Self::Rpm => format!("sudo dnf upgrade {package}"),
            Self::Pacman => "sudo pacman -Syu".into(),
            Self::Nix => "Update the Nix profile, Home Manager configuration, or NixOS configuration that owns this installation.".into(),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct InstalledPackage {
    pub family: PackageFamily,
    pub name: String,
    pub version: String,
    pub update_command: String,
    pub source: PackageSourceFacts,
}

impl InstalledPackage {
    fn new(family: PackageFamily, name: String, version: String) -> Self {
        let update_command = family.update_command(&name);
        Self {
            family,
            name,
            version,
            update_command,
            source: PackageSourceFacts::for_family(family),
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

/// Whether the package database owns the running executable.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Ownership {
    Package(&'static InstalledPackage),
    Unpackaged,
    /// The database did not answer; no decision that depends on ownership may proceed.
    Unknown(QueryReason),
}

impl Ownership {
    pub fn package(self) -> Option<&'static InstalledPackage> {
        match self {
            Self::Package(package) => Some(package),
            Self::Unpackaged | Self::Unknown(_) => None,
        }
    }
}

const OWNER_QUERY_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(2);

/// A definitive answer is kept for the process; an unknown one is asked again next time.
pub fn installed() -> Ownership {
    static RESOLVED: OnceLock<Option<InstalledPackage>> = OnceLock::new();
    resolve(&RESOLVED, || {
        if !cfg!(target_os = "linux") {
            return Ok(None);
        }
        let executable = running_executable().ok_or(QueryReason::InvalidResponse)?;
        owner(executable, &mut source::PerQuery(OWNER_QUERY_TIMEOUT))
    })
}

fn resolve(
    cache: &'static OnceLock<Option<InstalledPackage>>,
    query: impl FnOnce() -> Result<Option<InstalledPackage>, QueryReason>,
) -> Ownership {
    let resolved = match cache.get() {
        Some(resolved) => resolved,
        None => match query() {
            // Concurrent definitive answers read the same database; the first one stays.
            Ok(found) => cache.get_or_init(|| found),
            Err(reason) => return Ownership::Unknown(reason),
        },
    };
    resolved.as_ref().map_or(Ownership::Unpackaged, Ownership::Package)
}

/// Preserve the executable's original installed path even after replacement unlinks it.
fn running_executable() -> Option<&'static Path> {
    static EXECUTABLE: OnceLock<Option<PathBuf>> = OnceLock::new();
    EXECUTABLE.get_or_init(|| std::env::current_exe().ok()).as_deref()
}

fn owner(executable: &Path, reader: &mut impl source::Reader) -> Result<Option<InstalledPackage>, QueryReason> {
    if !executable.is_absolute() {
        return Ok(None);
    }
    if let Some(package) = query_dpkg(executable, reader)? {
        return Ok(Some(package));
    }
    if let Some(package) = query_rpm(executable, reader)? {
        return Ok(Some(package));
    }
    query_pacman(executable, reader)
}

/// Exit status 1 is each manager's no-owner outcome and an absent manager owns nothing.
/// Any other outcome leaves ownership unknown rather than unpackaged.
fn claimed(reader: &mut impl source::Reader, program: &str, args: &[&str]) -> Result<Option<String>, QueryReason> {
    match reader.read(program, args) {
        Err(QueryReason::AdapterUnavailable) => Ok(None),
        Err(reason) => Err(reason),
        Ok(output) if output.code == 0 => Ok(Some(output.stdout)),
        Ok(output) if output.code == 1 => Ok(None),
        Ok(_) => Err(QueryReason::QueryFailed),
    }
}

fn required(reader: &mut impl source::Reader, program: &str, args: &[&str]) -> Result<String, QueryReason> {
    let output = reader.read(program, args)?;
    if output.code != 0 {
        return Err(QueryReason::QueryFailed);
    }
    Ok(output.stdout)
}

fn query_dpkg(executable: &Path, reader: &mut impl source::Reader) -> Result<Option<InstalledPackage>, QueryReason> {
    let Some(search) = claimed(reader, "/usr/bin/dpkg-query", &["-S", &executable.to_string_lossy()])? else {
        return Ok(None);
    };
    let name = parse_dpkg_owner(&search, executable).ok_or(QueryReason::InvalidResponse)?;
    let version = required(reader, "/usr/bin/dpkg-query", &["-W", "-f=${Version}", &name])?;
    let version = version.trim();
    if version.is_empty() {
        return Err(QueryReason::InvalidResponse);
    }
    Ok(Some(InstalledPackage::new(PackageFamily::Deb, name, version.to_owned())))
}
/// `dpkg-query -S` prints `name[:arch][, name...]: path` per owning package.
fn parse_dpkg_owner(output: &str, executable: &Path) -> Option<String> {
    let wanted = executable.to_string_lossy();
    let mut names = std::collections::BTreeSet::new();
    for line in output.lines() {
        let Some((owners, path)) = line.rsplit_once(": ") else { continue; };
        if path.trim() != wanted {
            continue;
        }
        for owner in owners.split(',') {
            let name = owner.trim().split(':').next()?.trim();
            if !source::valid_package_name(name) { return None; }
            names.insert(name.to_owned());
        }
    }
    if names.len() != 1 { return None; }
    names.into_iter().next()
}

fn query_rpm(executable: &Path, reader: &mut impl source::Reader) -> Result<Option<InstalledPackage>, QueryReason> {
    let args = ["-qf", "--qf", "%{NAME}\t%{VERSION}\n", &executable.to_string_lossy()];
    let Some(output) = claimed(reader, "/usr/bin/rpm", &args)? else {
        return Ok(None);
    };
    parse_rpm_owner(&output).map(Some).ok_or(QueryReason::InvalidResponse)
}

fn query_pacman(executable: &Path, reader: &mut impl source::Reader) -> Result<Option<InstalledPackage>, QueryReason> {
    let Some(name) = claimed(reader, "/usr/bin/pacman", &["-Qoq", "--", &executable.to_string_lossy()])? else {
        return Ok(None);
    };
    let name = name.trim();
    if !source::valid_package_name(name) { return Err(QueryReason::InvalidResponse); }
    let raw = required(reader, "/usr/bin/pacman", &["-Q", "--", name])?;
    let fields: Vec<_> = raw.split_whitespace().collect();
    if fields.len() != 2 || fields[0] != name { return Err(QueryReason::InvalidResponse); }
    Ok(Some(InstalledPackage::new(PackageFamily::Pacman, name.into(), fields[1].into())))
}

/// `rpm -qf --qf '%{NAME}\t%{VERSION}\n'` prints one line per owning package.
fn parse_rpm_owner(output: &str) -> Option<InstalledPackage> {
    let lines: Vec<_> = output.lines().filter(|line| !line.trim().is_empty()).collect();
    if lines.len() != 1 { return None; }
    let line = lines[0];
    let (name, version) = line.split_once('\t')?;
    let (name, version) = (name.trim(), version.trim());
    (source::valid_package_name(name) && !version.is_empty() && !version.chars().any(char::is_control))
        .then(|| InstalledPackage::new(PackageFamily::Rpm, name.to_owned(), version.to_owned()))
}

#[cfg(test)]
mod ownership_tests;

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn dpkg_owner_is_read_only_for_the_queried_path() {
        let exe = Path::new("/opt/painted-wolf-code/bin/painted-wolf-code");
        assert_eq!(
            parse_dpkg_owner(
                "painted-wolf-code: /opt/painted-wolf-code/bin/painted-wolf-code\n",
                exe
            )
            .as_deref(),
            Some("painted-wolf-code")
        );
        assert_eq!(
            parse_dpkg_owner(
                "painted-wolf-code:amd64, other: /opt/painted-wolf-code/bin/painted-wolf-code\n",
                exe
            )
            .as_deref(),
            None
        );
        assert_eq!(
            parse_dpkg_owner(
                "diversion by foo from: /opt/painted-wolf-code/bin/painted-wolf-code\n",
                exe
            )
            .as_deref(),
            None
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
        assert_eq!(owner(Path::new("painted-wolf-code"), &mut source::PerQuery(OWNER_QUERY_TIMEOUT)), Ok(None));
    }
}
