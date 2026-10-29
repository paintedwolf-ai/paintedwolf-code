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
#[cfg(test)]
mod apt_probe_tests;
#[cfg(test)]
mod produced_fixtures;
pub use query::refresh;

pub(super) use command::{Output, Reader};

/// Each query gets its own deadline, so one slow package manager cannot spend another's.
pub(super) struct PerQuery(pub(super) std::time::Duration);

impl Reader for PerQuery {
    fn read(&mut self, program: &str, args: &[&str]) -> Result<Output, QueryReason> {
        command::OsReader::new(self.0, std::sync::Arc::new(std::sync::atomic::AtomicBool::new(false))).read(program, args)
    }
}

pub(super) fn valid_package_name(value: &str) -> bool {
    !value.is_empty() && value.len() <= 255 && value.bytes().all(|c| c.is_ascii_alphanumeric() || b"+._-".contains(&c)) && !value.starts_with('-')
}

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
