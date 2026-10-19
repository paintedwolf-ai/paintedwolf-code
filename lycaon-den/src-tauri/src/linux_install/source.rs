//! Source availability is independent of an upstream release offer. The running
//! Nix output establishes layout, never the configuration that selected it.

use super::PackageFamily;
use serde::Serialize;

mod apt;
mod command;
mod dnf;
mod pacman;
mod ownership;
mod query;
#[cfg(test)]
mod tests;
pub use query::refresh;

pub(super) fn read(program: &str, args: &[&str]) -> Result<String, QueryReason> {
    let mut reader = command::OsReader::new(std::time::Duration::from_secs(2), std::sync::Arc::new(std::sync::atomic::AtomicBool::new(false)));
    command::success(&mut reader, program, args)
}

pub(super) fn valid_package_name(name: &str) -> bool { command::package_name(name) }

#[derive(Debug, Default, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum SourceAvailability {
    #[default]
    NotChecked,
    CandidateFound,
    NoNewerCandidate,
    Blocked,
    Unknown,
    Failed,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum QueryReason {
    UnsupportedOwner,
    AdapterUnavailable,
    NotInstalled,
    NoCachedCandidate,
    PolicyUnobserved,
    Held,
    UntrustedSource,
    AmbiguousCandidate,
    WrongArchitecture,
    QueryFailed,
    TimedOut,
    OutputLimit,
    Cancelled,
    InvalidResponse,
    OwnerChanged,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Repository {
    pub id: String,
    /// The repository's declared suite, never an inference from a version or URL.
    pub suite: Option<String>,
    pub trusted: Option<bool>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct PackageCandidate {
    pub version: String,
    pub architecture: String,
    pub repositories: Vec<Repository>,
    pub newer_than_installed: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum PackageOwner {
    SystemPackageManager,
}

#[derive(Debug, Default, Clone, PartialEq, Eq, Serialize)]
pub struct PackageSourceFacts {
    pub availability: SourceAvailability,
    /// None means no structured fact identifies the installation owner.
    pub owner: Option<PackageOwner>,
    /// None means the app has not established whether the source is pinned.
    pub pinned: Option<bool>,
    pub checked_at: Option<u64>,
    /// A fresh package-database observation; the captured running version is unchanged.
    pub database_version: Option<String>,
    pub database_owner: Option<String>,
    pub architecture: Option<String>,
    pub candidate: Option<PackageCandidate>,
    pub held: Option<bool>,
    pub selection_priority: Option<i64>,
    /// Queries never refresh metadata. This is not a claim about the remote repository.
    pub cached_only: bool,
    pub reason: Option<QueryReason>,
}

impl PackageSourceFacts {
    pub(super) fn for_family(family: PackageFamily) -> Self {
        Self {
            availability: SourceAvailability::NotChecked,
            owner: match family {
                PackageFamily::Deb | PackageFamily::Rpm | PackageFamily::Pacman => Some(PackageOwner::SystemPackageManager),
                PackageFamily::Nix => None,
            },
            pinned: None,
            ..Self::default()
        }
    }

    fn fail(mut self, reason: QueryReason) -> Self {
        self.availability = SourceAvailability::Failed;
        self.reason = Some(reason);
        self.candidate = None;
        self
    }
}

#[cfg(test)]
mod generation_contracts {
    use super::*;

    #[test]
    fn a_nix_generation_establishes_neither_source_availability_nor_owner() {
        let package = super::super::InstalledPackage::new(
            PackageFamily::Nix,
            "painted-wolf-code".into(),
            "1.0.2".into(),
        );
        let facts = &package.source;
        assert_eq!(facts.availability, SourceAvailability::NotChecked);
        assert_eq!(facts.owner, None);
        assert_eq!(facts.pinned, None);
    }

    #[test]
    fn native_package_database_ownership_is_not_a_source_check_or_pin_check() {
        for family in [PackageFamily::Deb, PackageFamily::Rpm, PackageFamily::Pacman] {
            let facts = PackageSourceFacts::for_family(family);
            assert_eq!(facts.owner, Some(PackageOwner::SystemPackageManager));
            assert_eq!(facts.availability, SourceAvailability::NotChecked);
            assert_eq!(facts.pinned, None);
        }
    }
}
