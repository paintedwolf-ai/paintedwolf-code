use super::{command::{Output, Reader}, query::observe, *};
use std::collections::VecDeque;

struct Fake {
    outputs: VecDeque<Result<Output, QueryReason>>,
    calls: Vec<(String, Vec<String>)>,
}
impl Fake {
    fn new(outputs: Vec<Result<Output, QueryReason>>) -> Self { Self { outputs: outputs.into(), calls: Vec::new() } }
}
impl Reader for Fake {
    fn read(&mut self, program: &str, args: &[&str]) -> Result<Output, QueryReason> {
        self.calls.push((program.into(), args.iter().map(|a| (*a).into()).collect()));
        self.outputs.pop_front().expect("unexpected package-manager query")
    }
}
fn output(raw: &str) -> Result<Output, QueryReason> { Ok(Output { code: 0, stdout: raw.into() }) }
fn apt_report() -> serde_json::Value {
    serde_json::json!({"schema":1,"adapter_available":true,"installed":"1.0.2~rc.1","architecture":"amd64","held":false,
        "candidate":{"version":"1.0.2","architecture":"amd64","newer":true,"priority":700,
            "origins":[{"id":"Painted Wolf Code","suite":"stable","trusted":true}]}})
}
fn apt_facts(report: serde_json::Value) -> PackageSourceFacts {
    observe(PackageFamily::Deb, "painted-wolf-code", &mut Fake::new(vec![output(&report.to_string())]))
}

#[test]
fn a_generation_does_not_establish_an_owner_pin_or_package_source() {
    let mut fake = Fake::new(vec![]);
    let facts = observe(PackageFamily::Nix, "painted-wolf-code", &mut fake);
    assert_eq!(facts.availability, SourceAvailability::Unknown);
    assert_eq!(facts.reason, Some(QueryReason::UnsupportedOwner));
    assert_eq!(facts.owner, None);
    assert_eq!(facts.pinned, None);
    assert!(facts.cached_only);
    assert!(fake.calls.is_empty());
}

#[test]
fn initial_database_ownership_does_not_establish_source_policy() {
    for family in [PackageFamily::Deb, PackageFamily::Rpm, PackageFamily::Pacman] {
        let facts = PackageSourceFacts::for_family(family);
        assert_eq!(facts.owner, Some(PackageOwner::SystemPackageManager));
        assert_eq!(facts.availability, SourceAvailability::NotChecked);
        assert_eq!(facts.pinned, None);
        assert_eq!(facts.checked_at, None);
        assert_eq!(facts.candidate, None);
    }
}

#[test]
fn apt_uses_the_native_policy_candidate_and_preserves_package_version_and_suite() {
    let facts = apt_facts(apt_report());
    assert_eq!(facts.availability, SourceAvailability::CandidateFound);
    assert_eq!(facts.database_version.as_deref(), Some("1.0.2~rc.1"));
    assert_eq!(facts.selection_priority, Some(700));
    assert_eq!(facts.held, Some(false));
    assert_eq!(facts.pinned, None);
    assert!(facts.cached_only && facts.checked_at.is_some());
    let candidate = facts.candidate.unwrap();
    assert_eq!(candidate.version, "1.0.2");
    assert_eq!(candidate.repositories[0].suite.as_deref(), Some("stable"));
    assert_eq!(candidate.repositories[0].trusted, Some(true));
}

#[test]
fn apt_hold_untrusted_and_missing_cache_are_explicit_outcomes() {
    let mut report = apt_report(); report["held"] = true.into();
    let facts = apt_facts(report);
    assert_eq!((facts.availability, facts.reason), (SourceAvailability::Blocked, Some(QueryReason::Held)));
    assert!(facts.candidate.is_some());
    let mut report = apt_report(); report["candidate"]["origins"][0]["trusted"] = false.into();
    let facts = apt_facts(report);
    assert_eq!((facts.availability, facts.reason), (SourceAvailability::Blocked, Some(QueryReason::UntrustedSource)));
    let mut report = apt_report(); report["candidate"]["origins"] = serde_json::json!([]);
    assert_eq!(apt_facts(report).reason, Some(QueryReason::NoCachedCandidate));
    let mut report = apt_report(); report["candidate"] = serde_json::Value::Null;
    assert_eq!(apt_facts(report).reason, Some(QueryReason::NoCachedCandidate));
}

#[test]
fn apt_current_missing_install_and_wrong_architecture_cannot_become_an_upgrade() {
    let mut report = apt_report(); report["candidate"]["newer"] = false.into();
    assert_eq!(apt_facts(report).availability, SourceAvailability::NoNewerCandidate);
    let mut report = apt_report(); report["installed"] = serde_json::Value::Null;
    assert_eq!(apt_facts(report).reason, Some(QueryReason::NotInstalled));
    let mut report = apt_report(); report["candidate"]["architecture"] = "arm64".into();
    let facts = apt_facts(report);
    assert_eq!((facts.availability, facts.reason), (SourceAvailability::Failed, Some(QueryReason::WrongArchitecture)));
    assert_eq!(facts.candidate, None);
    assert_eq!(apt_facts(serde_json::json!({"schema":1,"adapter_available":false})).reason, Some(QueryReason::AdapterUnavailable));
}

#[test]
fn dnf5_candidate_is_not_a_versionlock_or_transaction_policy_claim() {
    let mut fake = Fake::new(vec![output("painted-wolf-code\t1.0.2~rc.1-1\tx86_64\n"), Ok(Output { code: 100,
        stdout: serde_json::json!({"updates":[{"name":"painted-wolf-code","evr":"1.0.2-1","arch":"x86_64","repository":"painted-wolf-code-stable"}]}).to_string() })]);
    let facts = observe(PackageFamily::Rpm, "painted-wolf-code", &mut fake);
    assert_eq!(facts.availability, SourceAvailability::Unknown);
    assert_eq!(facts.reason, Some(QueryReason::PolicyUnobserved));
    assert_eq!(facts.held, None);
    assert_eq!(facts.candidate.unwrap().version, "1.0.2-1");
    assert!(fake.calls[1].1.iter().any(|a| a == "--cacheonly"));
}

#[test]
fn dnf4_fallback_is_only_for_an_absent_dnf5_interface() {
    let mut fake = Fake::new(vec![output("painted-wolf-code\t1.0.2-1\tx86_64\n"), Err(QueryReason::AdapterUnavailable), output("painted-wolf-code\t1.1.0-1\tx86_64\tconfigured-repo\n")]);
    let facts = observe(PackageFamily::Rpm, "painted-wolf-code", &mut fake);
    assert_eq!(facts.candidate.unwrap().repositories[0].id, "configured-repo");
    assert_eq!(fake.calls[2].0, "/usr/bin/dnf");
    for reason in [QueryReason::TimedOut, QueryReason::QueryFailed, QueryReason::InvalidResponse] {
        let mut fake = Fake::new(vec![output("painted-wolf-code\t1.0.2-1\tx86_64\n"), Err(reason)]);
        let facts = observe(PackageFamily::Rpm, "painted-wolf-code", &mut fake);
        assert_eq!(facts.reason, Some(reason));
        assert_eq!(fake.calls.len(), 2);
        assert_eq!(facts.candidate, None);
    }
}

fn pacman_facts(ignore: &str, groups: &str, candidate: &str) -> (PackageSourceFacts, Fake) {
    let mut fake = Fake::new(vec![output("painted-wolf-code 1.0.2pre.rc.1-2\n"), output(ignore), output(groups), output("x86_64\n"), output(candidate)]);
    (observe(PackageFamily::Pacman, "painted-wolf-code", &mut fake), fake)
}

#[test]
fn pacman_uses_print_only_full_upgrade_selection_without_refresh_or_partial_upgrade() {
    let (facts, fake) = pacman_facts("", "", "dependency\t3.0-1\tx86_64\tcore\npainted-wolf-code\t1.0.2-2\tx86_64\tconfigured-source\n");
    assert_eq!(facts.availability, SourceAvailability::CandidateFound);
    assert_eq!(facts.held, Some(false));
    assert_eq!(facts.database_version.as_deref(), Some("1.0.2pre.rc.1-2"));
    assert_eq!(facts.candidate.unwrap().version, "1.0.2-2");
    let upgrade = fake.calls.last().unwrap();
    assert_eq!(upgrade.0, "/usr/bin/pacman");
    assert!(upgrade.1.iter().any(|a| a == "--print"));
    assert!(!upgrade.1.iter().any(|a| a.contains('y') || a == "painted-wolf-code"));
}

#[test]
fn pacman_holds_globs_groups_and_missing_candidates_remain_distinct() {
    let (facts, _) = pacman_facts("painted-*\n", "", "");
    assert_eq!((facts.availability, facts.reason), (SourceAvailability::Blocked, Some(QueryReason::Held)));
    let (facts, _) = pacman_facts("unrelated\n", "desktop\n", "");
    assert_eq!(facts.held, None);
    assert_eq!(facts.reason, Some(QueryReason::PolicyUnobserved));
    let (facts, _) = pacman_facts("", "", "");
    assert_eq!(facts.availability, SourceAvailability::NoNewerCandidate);
    assert_eq!(facts.candidate, None);
    let (facts, _) = pacman_facts("", "", "painted-wolf-code\t1.1-1\taarch64\tcore\n");
    assert_eq!(facts.reason, Some(QueryReason::WrongArchitecture));
}

#[test]
fn failed_fresh_query_does_not_retain_an_old_positive_candidate() {
    for reason in [QueryReason::TimedOut, QueryReason::OutputLimit, QueryReason::Cancelled, QueryReason::QueryFailed] {
        let mut fake = Fake::new(vec![Err(reason)]);
        let facts = observe(PackageFamily::Deb, "painted-wolf-code", &mut fake);
        assert_eq!(facts.availability, SourceAvailability::Failed);
        assert_eq!(facts.reason, Some(reason));
        assert!(facts.candidate.is_none() && facts.database_version.is_none());
    }
    let mut fake = Fake::new(vec![]);
    assert_eq!(observe(PackageFamily::Rpm, "--option", &mut fake).reason, Some(QueryReason::InvalidResponse));
    assert!(fake.calls.is_empty());
}

#[cfg(target_os = "linux")]
#[test]
fn os_query_is_bounded_and_ignores_error_prose() {
    use std::{sync::{Arc, atomic::AtomicBool}, time::{Duration, Instant}};
    let mut reader = command::OsReader::new(Duration::from_millis(60), Arc::new(AtomicBool::new(false)));
    let started = Instant::now();
    assert!(matches!(reader.read("/bin/sh", &["-c", "sleep 20 & wait"]), Err(QueryReason::TimedOut)));
    assert!(started.elapsed() < Duration::from_secs(2));
    let mut reader = command::OsReader::new(Duration::from_secs(2), Arc::new(AtomicBool::new(false)));
    let result = reader.read("/bin/sh", &["-c", "printf held; printf 'not installed' >&2; exit 7"]).unwrap();
    assert_eq!(result.code, 7);
    assert_eq!(result.stdout, "held");
    let result = reader.read("/bin/sh", &["-c", "yes x"]).err();
    assert_eq!(result, Some(QueryReason::OutputLimit));
}
