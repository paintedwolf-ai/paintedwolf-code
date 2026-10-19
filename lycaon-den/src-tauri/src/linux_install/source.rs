//! Source availability is independent of an upstream release offer. The running
//! Nix output establishes layout, never the configuration that selected it.

use super::PackageFamily;
use serde::Serialize;

#[derive(Debug, Default, Clone, Copy, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum SourceAvailability {
    #[default]
    NotChecked,
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
        }
    }
}

#[cfg(test)]
mod tests {
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
