//! The source facts Den's package notice tests consume are the facts these adapters produce.
use super::{command::{Output, Reader}, query::observe, *};
use std::collections::VecDeque;

/// Exit status and standard output of each package-manager query, in order.
struct Replies(VecDeque<(i32, &'static str)>);
impl Reader for Replies {
    fn read(&mut self, _: &str, _: &[&str]) -> Result<Output, QueryReason> {
        let (code, stdout) = self.0.pop_front().expect("unexpected package-manager query");
        Ok(Output { code, stdout: stdout.into() })
    }
}

fn produced(family: PackageFamily, replies: Vec<(i32, &'static str)>) -> serde_json::Value {
    let mut facts = observe(family, "painted-wolf-code", &mut Replies(replies.into()));
    assert!(facts.checked_at.is_some());
    facts.checked_at = None;
    serde_json::to_value(facts).unwrap()
}

fn fixture(raw: &str) -> serde_json::Value { serde_json::from_str(raw).unwrap() }

const APT_CANDIDATE: &str = r#"{"schema":1,"adapter_available":true,"installed":"1.0.2~rc.1","architecture":"amd64","held":false,
    "candidate":{"version":"1.0.2","architecture":"amd64","newer":true,"priority":700,
    "origins":[{"id":"Painted Wolf Code","suite":"stable","trusted":true}]}}"#;
const APT_HELD: &str = r#"{"schema":1,"adapter_available":true,"installed":"1.0.2~rc.1","architecture":"amd64","held":true,
    "candidate":{"version":"1.0.2","architecture":"amd64","newer":true,"priority":700,
    "origins":[{"id":"Painted Wolf Code","suite":"stable","trusted":true}]}}"#;
const PACMAN_INSTALLED: &str = "painted-wolf-code 1.0.2pre.rc.1-2\n";

#[test]
fn apt_policy_candidates_and_holds_match_the_den_fixtures() {
    assert_eq!(produced(PackageFamily::Deb, vec![(0, APT_CANDIDATE)]),
        fixture(include_str!("../../../../src/settings/system/fixtures/package-source-apt-candidate.json")));
    assert_eq!(produced(PackageFamily::Deb, vec![(0, APT_HELD)]),
        fixture(include_str!("../../../../src/settings/system/fixtures/package-source-apt-held.json")));
}

#[test]
fn pacman_selection_and_ignore_rules_match_the_den_fixtures() {
    let upgrade: &'static str = "painted-wolf-code\t1.0.2-2\tx86_64\tconfigured-source\n";
    assert_eq!(produced(PackageFamily::Pacman, vec![(0, PACMAN_INSTALLED), (0, ""), (0, ""), (0, "x86_64\n"), (0, upgrade)]),
        fixture(include_str!("../../../../src/settings/system/fixtures/package-source-pacman-candidate.json")));
    assert_eq!(produced(PackageFamily::Pacman, vec![(0, PACMAN_INSTALLED), (0, "painted-*\n"), (0, ""), (0, "x86_64\n"), (0, "")]),
        fixture(include_str!("../../../../src/settings/system/fixtures/package-source-pacman-ignored.json")));
}

#[test]
fn dnf_candidates_without_versionlock_policy_match_the_den_fixture() {
    let updates: &'static str = r#"{"updates":[{"name":"painted-wolf-code","evr":"1.0.2-1","arch":"x86_64","repository":"painted-wolf-code-stable"}]}"#;
    assert_eq!(produced(PackageFamily::Rpm, vec![(0, "painted-wolf-code\t1.0.2~rc.1-1\tx86_64\n"), (100, updates)]),
        fixture(include_str!("../../../../src/settings/system/fixtures/package-source-dnf-candidate.json")));
}
