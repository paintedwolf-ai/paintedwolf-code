use super::{command::{OsReader, package_name}, *};
use crate::linux_install::InstalledPackage;
use std::{sync::{Arc, atomic::{AtomicBool, Ordering}}, time::{Duration, SystemTime, UNIX_EPOCH}};

struct CancelOnDrop(Arc<AtomicBool>);
impl Drop for CancelOnDrop {
    fn drop(&mut self) { self.0.store(true, Ordering::Release); }
}

/// Dropping the future cancels its bounded OS query; stale facts never replace a later check.
pub async fn refresh(package: Option<InstalledPackage>) -> Option<InstalledPackage> {
    let mut package = package?;
    let cancelled = Arc::new(AtomicBool::new(false));
    let _cancel = CancelOnDrop(cancelled.clone());
    let family = package.family;
    let name = package.name.clone();
    let timeout = Duration::from_secs(8);
    let mut reader = OsReader::new(timeout, cancelled);
    let result = tokio::time::timeout(timeout, tauri::async_runtime::spawn_blocking(move || {
        observe(family, &name, &mut reader)
    })).await;
    package.source = match result {
        Ok(Ok(facts)) => facts,
        Ok(Err(_)) => PackageSourceFacts::for_family(family).fail(QueryReason::QueryFailed),
        Err(_) => PackageSourceFacts::for_family(family).fail(QueryReason::TimedOut),
    };
    Some(package)
}

pub(super) fn observe(family: PackageFamily, name: &str, reader: &mut impl command::Reader) -> PackageSourceFacts {
    let mut facts = PackageSourceFacts::for_family(family);
    facts.checked_at = SystemTime::now().duration_since(UNIX_EPOCH).ok().map(|t| t.as_secs());
    facts.cached_only = true;
    if !package_name(name) { return facts.fail(QueryReason::InvalidResponse); }
    let result = match family {
        PackageFamily::Deb => apt::observe(name, reader, &mut facts),
        PackageFamily::Rpm => dnf::observe(name, reader, &mut facts),
        PackageFamily::Pacman => pacman::observe(name, reader, &mut facts),
        PackageFamily::Nix => {
            facts.cached_only = false;
            facts.availability = SourceAvailability::Unknown;
            facts.reason = Some(QueryReason::UnsupportedOwner);
            Ok(())
        },
    };
    match result { Ok(()) => facts, Err(reason) => facts.fail(reason) }
}

pub(super) fn select(facts: &mut PackageSourceFacts, candidate: Option<PackageCandidate>, reason: Option<QueryReason>) {
    facts.availability = if matches!(reason, Some(QueryReason::Held | QueryReason::UntrustedSource)) {
        SourceAvailability::Blocked
    } else if reason.is_some() {
        SourceAvailability::Unknown
    } else if candidate.as_ref().is_some_and(|c| c.newer_than_installed) {
        SourceAvailability::CandidateFound
    } else { SourceAvailability::NoNewerCandidate };
    facts.candidate = candidate;
    facts.reason = reason;
}
