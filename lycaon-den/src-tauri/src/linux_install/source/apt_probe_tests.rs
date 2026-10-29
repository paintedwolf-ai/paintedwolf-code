//! Runs the embedded APT probe under a real interpreter against a stand-in `apt_pkg`, so the
//! script's output is held to the schema `apt::project` admits rather than to a hand-written report.
use super::{command::{Output, Reader}, query::observe, *};
use crate::test_support::TempDir;
use std::process::{Command, Stdio};

const APT_PKG: &str = include_str!("fixtures/apt_pkg.py");

/// Executes the probe's own argv. `-I` is dropped because it ignores PYTHONPATH by design,
/// and PYTHONPATH is how the stand-in replaces any installed python-apt.
struct Interpreter { modules: TempDir }

impl Interpreter {
    fn with_cache(cache: serde_json::Value) -> Self {
        let modules = TempDir::new("apt-probe");
        std::fs::write(modules.join("apt_pkg.py"), APT_PKG).unwrap();
        std::fs::write(modules.join("cache.json"), cache.to_string()).unwrap();
        Self { modules }
    }
}

impl Reader for Interpreter {
    fn read(&mut self, program: &str, args: &[&str]) -> Result<Output, QueryReason> {
        assert_eq!(program, "/usr/bin/python3");
        let ["-I", "-c", script, name] = args else { panic!("unexpected probe argv {args:?}") };
        let output = Command::new("python3").args(["-c", script, name]).env_clear()
            .env("PATH", std::env::var_os("PATH").unwrap_or_default()).env("LC_ALL", "C")
            .env("PYTHONPATH", self.modules.path()).env("PYTHONDONTWRITEBYTECODE", "1")
            .stdin(Stdio::null()).output().expect("python3 is required to run the APT probe");
        Ok(Output { code: output.status.code().unwrap_or(-1), stdout: String::from_utf8(output.stdout).unwrap() })
    }
}

fn origin(trusted: Option<bool>) -> serde_json::Value {
    serde_json::json!({"origin":"Painted Wolf Code","label":"Painted Wolf Code","site":"apt.example.test","archive":"stable","trusted":trusted})
}

fn installed_cache(candidate_arch: &str, origins: Vec<serde_json::Value>) -> serde_json::Value {
    let mut files = origins;
    files.push(serde_json::json!({"archive":"now","origin":"","label":"","site":""}));
    serde_json::json!({"packages":{"painted-wolf-code":{
        "installed":{"version":"1.0.2~rc.1","arch":"amd64","files":[{"archive":"now"}]},
        "candidate":{"version":"1.0.2","arch":candidate_arch,"priority":700,"files":files},
        "upgradable":true,"held":false}}})
}

fn probe(cache: serde_json::Value) -> PackageSourceFacts {
    observe(PackageFamily::Deb, "painted-wolf-code", &mut Interpreter::with_cache(cache))
}

#[test]
fn probe_reports_the_policy_candidate_and_its_configured_origins_but_not_the_status_file() {
    let facts = probe(installed_cache("amd64", vec![origin(Some(true))]));
    assert_eq!(facts.reason, None);
    assert_eq!(facts.availability, SourceAvailability::CandidateFound);
    assert_eq!(facts.database_version.as_deref(), Some("1.0.2~rc.1"));
    assert_eq!((facts.architecture.as_deref(), facts.held, facts.selection_priority), (Some("amd64"), Some(false), Some(700)));
    let candidate = facts.candidate.unwrap();
    assert_eq!((candidate.version.as_str(), candidate.newer_than_installed), ("1.0.2", true));
    assert_eq!(candidate.repositories, vec![Repository { id: "Painted Wolf Code".into(), suite: Some("stable".into()), trusted: Some(true) }]);
}

#[test]
fn probe_distinguishes_absent_and_versionless_packages_from_a_missing_candidate() {
    let facts = probe(serde_json::json!({"packages":{}}));
    assert_eq!((facts.availability, facts.reason), (SourceAvailability::Unknown, Some(QueryReason::NotInstalled)));
    let facts = probe(serde_json::json!({"packages":{"painted-wolf-code":{"has_versions":false,
        "installed":{"version":"1.0.2","arch":"amd64"}}}}));
    assert_eq!(facts.reason, Some(QueryReason::NotInstalled));
    assert_eq!(facts.database_version, None);
    let facts = probe(serde_json::json!({"packages":{"painted-wolf-code":{"installed":{"version":"1.0.2","arch":"amd64"}}}}));
    assert_eq!((facts.database_version.as_deref(), facts.reason), (Some("1.0.2"), Some(QueryReason::NoCachedCandidate)));
}

#[test]
fn probe_refuses_untrusted_unindexed_foreign_or_held_sources() {
    for trusted in [Some(false), None] {
        let facts = probe(installed_cache("amd64", vec![origin(Some(true)), origin(trusted)]));
        assert_eq!((facts.availability, facts.reason), (SourceAvailability::Blocked, Some(QueryReason::UntrustedSource)));
    }
    let facts = probe(installed_cache("arm64", vec![origin(Some(true))]));
    assert_eq!((facts.availability, facts.reason), (SourceAvailability::Failed, Some(QueryReason::WrongArchitecture)));
    assert_eq!(facts.candidate, None);
    let mut cache = installed_cache("amd64", vec![origin(Some(true))]);
    cache["packages"]["painted-wolf-code"]["held"] = true.into();
    assert_eq!(probe(cache).reason, Some(QueryReason::Held));
}

#[test]
fn probe_refuses_unreadable_or_malformed_cache_records() {
    let facts = probe(serde_json::json!({"unreadable":true,"packages":{}}));
    assert_eq!((facts.availability, facts.reason), (SourceAvailability::Failed, Some(QueryReason::QueryFailed)));
    let mut cache = installed_cache("amd64", vec![origin(Some(true))]);
    cache["packages"]["painted-wolf-code"]["installed"]["version"] = "1.0.2~rc.1\u{1b}[2K".into();
    let facts = probe(cache);
    assert_eq!((facts.availability, facts.reason), (SourceAvailability::Failed, Some(QueryReason::InvalidResponse)));
    assert!(facts.candidate.is_none() && facts.database_version.is_none());
    let mut cache = installed_cache("amd64", vec![origin(Some(true))]);
    cache["packages"]["painted-wolf-code"]["candidate"]["version"] = "".into();
    let facts = probe(cache);
    assert_eq!((facts.reason, facts.candidate), (Some(QueryReason::InvalidResponse), None));
    let origins = (0..17).map(|_| origin(Some(true))).collect();
    assert_eq!(probe(installed_cache("amd64", origins)).reason, Some(QueryReason::InvalidResponse));
}
