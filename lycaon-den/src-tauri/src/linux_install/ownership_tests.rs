//! Ownership that the package database did not establish stays unknown and is asked again.
use super::*;
use source::{Output, Reader};
use std::collections::VecDeque;

const EXE: &str = "/opt/painted-wolf-code/bin/painted-wolf-code";

struct Scripted(VecDeque<Result<Output, QueryReason>>);
impl Reader for Scripted {
    fn read(&mut self, _: &str, _: &[&str]) -> Result<Output, QueryReason> {
        self.0.pop_front().expect("unexpected package-manager query")
    }
}

fn scripted(outputs: Vec<Result<Output, QueryReason>>) -> Scripted { Scripted(outputs.into()) }
fn exit(code: i32, stdout: &str) -> Result<Output, QueryReason> { Ok(Output { code, stdout: stdout.into() }) }
fn fresh_cache() -> &'static OnceLock<Option<InstalledPackage>> { Box::leak(Box::new(OnceLock::new())) }

#[test]
fn every_manager_reporting_no_owner_is_unpackaged() {
    let mut reader = scripted(vec![exit(1, ""), Err(QueryReason::AdapterUnavailable), exit(1, "")]);
    assert_eq!(owner(Path::new(EXE), &mut reader), Ok(None));
}

#[test]
fn an_unanswered_or_unreadable_database_is_never_unpackaged() {
    for (outputs, reason) in [
        (vec![Err(QueryReason::TimedOut)], QueryReason::TimedOut),
        (vec![Err(QueryReason::OutputLimit)], QueryReason::OutputLimit),
        (vec![Err(QueryReason::QueryFailed)], QueryReason::QueryFailed),
        (vec![exit(2, "")], QueryReason::QueryFailed),
        (vec![exit(1, ""), exit(0, "painted-wolf-code\t1.0.2\nother\t1.0\n")], QueryReason::InvalidResponse),
        (vec![exit(1, ""), Err(QueryReason::AdapterUnavailable), Err(QueryReason::TimedOut)], QueryReason::TimedOut),
        (vec![exit(0, &format!("painted-wolf-code:amd64, other: {EXE}\n"))], QueryReason::InvalidResponse),
        (vec![exit(0, &format!("painted-wolf-code: {EXE}\n")), Err(QueryReason::TimedOut)], QueryReason::TimedOut),
        (vec![exit(0, &format!("painted-wolf-code: {EXE}\n")), exit(1, "")], QueryReason::QueryFailed),
    ] {
        assert_eq!(owner(Path::new(EXE), &mut scripted(outputs)), Err(reason));
    }
}

#[test]
fn unknown_ownership_is_not_cached_and_the_next_request_queries_again() {
    let cache = fresh_cache();
    let timed_out = resolve(cache, || owner(Path::new(EXE), &mut scripted(vec![Err(QueryReason::TimedOut)])));
    assert_eq!(timed_out, Ownership::Unknown(QueryReason::TimedOut));
    assert_eq!(timed_out.package(), None);
    let found = resolve(cache, || {
        owner(Path::new(EXE), &mut scripted(vec![exit(0, &format!("painted-wolf-code: {EXE}\n")), exit(0, "1.0.2")]))
    });
    let package = found.package().expect("the retry establishes the owner");
    assert_eq!((package.family, package.name.as_str(), package.version.as_str()), (PackageFamily::Deb, "painted-wolf-code", "1.0.2"));
    assert_eq!(resolve(cache, || panic!("a definitive owner is not queried again")), found);
}

#[test]
fn a_definitive_unpackaged_answer_is_kept() {
    let cache = fresh_cache();
    assert_eq!(resolve(cache, || Ok(None)), Ownership::Unpackaged);
    assert_eq!(resolve(cache, || panic!("a definitive answer is not queried again")), Ownership::Unpackaged);
}

/// Runs stand-in package-manager executables through the bounded OS reader.
#[cfg(target_os = "linux")]
struct StandIns { dir: crate::test_support::TempDir, deadline: std::time::Duration }

#[cfg(target_os = "linux")]
impl Reader for StandIns {
    fn read(&mut self, program: &str, args: &[&str]) -> Result<Output, QueryReason> {
        let name = Path::new(program).file_name().unwrap();
        let stand_in = self.dir.join(name);
        if !stand_in.exists() { return Err(QueryReason::AdapterUnavailable); }
        source::PerQuery(self.deadline).read(stand_in.to_str().unwrap(), args)
    }
}

#[cfg(target_os = "linux")]
#[test]
fn a_package_manager_that_misses_the_deadline_is_unknown_until_it_answers() {
    use std::os::unix::fs::PermissionsExt;
    let dir = crate::test_support::TempDir::new("ownership-deadline");
    let dpkg = dir.join("dpkg-query");
    // The first query outlives the deadline; later ones answer like dpkg-query.
    std::fs::write(&dpkg, "#!/bin/sh\nif [ ! -e \"$0.slow\" ]; then : > \"$0.slow\"; exec sleep 30; fi\ncase \"$1\" in\n  -S) printf 'painted-wolf-code: %s\\n' \"$2\" ;;\n  -W) printf '1.0.2' ;;\nesac\n").unwrap();
    std::fs::set_permissions(&dpkg, std::fs::Permissions::from_mode(0o755)).unwrap();
    let mut stand_ins = StandIns { dir, deadline: std::time::Duration::from_millis(300) };
    let cache = fresh_cache();
    let started = std::time::Instant::now();
    assert_eq!(resolve(cache, || owner(Path::new(EXE), &mut stand_ins)), Ownership::Unknown(QueryReason::TimedOut));
    assert!(started.elapsed() < std::time::Duration::from_secs(5), "the stand-in is stopped at the deadline");
    let package = resolve(cache, || owner(Path::new(EXE), &mut stand_ins)).package().expect("the retry establishes the owner");
    assert_eq!((package.name.as_str(), package.version.as_str()), ("painted-wolf-code", "1.0.2"));
}
