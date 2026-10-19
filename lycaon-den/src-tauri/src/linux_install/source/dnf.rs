//! DNF's read-only query does not establish transaction/versionlock eligibility.
use super::{command::{field, success, Reader}, query::select, *};
use serde::Deserialize;
use std::collections::BTreeMap;

#[derive(Deserialize)]
struct DnfCandidate {
    name: String,
    arch: String,
    evr: String,
    repository: String,
}

pub(super) fn observe(name: &str, reader: &mut impl Reader, facts: &mut PackageSourceFacts) -> Result<(), QueryReason> {
    let installed = reader.read("/usr/bin/rpm", &["-q", "--qf", "%{NAME}\t%{EVR}\t%{ARCH}\n", "--", name])?;
    if installed.code != 0 { return Err(QueryReason::QueryFailed); }
    let records: Vec<_> = installed.stdout.lines().collect();
    if records.len() != 1 { return Err(QueryReason::AmbiguousCandidate); }
    let fields: Vec<_> = records[0].split('\t').collect();
    if fields.len() != 3 || fields[0] != name || !fields.iter().all(|f| field(f)) {
        return Err(QueryReason::InvalidResponse);
    }
    facts.database_version = Some(fields[1].into());
    facts.architecture = Some(fields[2].into());
    let candidates = match reader.read("/usr/bin/dnf5", &["--cacheonly", "--quiet", "check-upgrade", "--json", name]) {
        Err(QueryReason::AdapterUnavailable) => {
            let raw = success(reader, "/usr/bin/dnf", &["--cacheonly", "--quiet", "repoquery", "--upgrades", "--latest-limit=1", "--queryformat", "%{name}\t%{evr}\t%{arch}\t%{reponame}\n", "--", name])?;
            parse_dnf4(&raw)?
        },
        Err(reason) => return Err(reason),
        Ok(output) => {
            if output.code != 0 && output.code != 100 { return Err(QueryReason::QueryFailed); }
            let sections: BTreeMap<String, Vec<DnfCandidate>> = serde_json::from_str(&output.stdout).map_err(|_| QueryReason::InvalidResponse)?;
            let candidates: Vec<_> = sections.into_values().flatten().collect();
            if output.code == 0 && !candidates.is_empty() { return Err(QueryReason::InvalidResponse); }
            candidates
        },
    };
    project(name, fields[2], candidates, facts)
}

fn parse_dnf4(raw: &str) -> Result<Vec<DnfCandidate>, QueryReason> {
    raw.lines().map(|line| {
        let fields: Vec<_> = line.split('\t').collect();
        if fields.len() != 4 { return Err(QueryReason::InvalidResponse); }
        Ok(DnfCandidate { name: fields[0].into(), evr: fields[1].into(), arch: fields[2].into(), repository: fields[3].into() })
    }).collect()
}

fn project(name: &str, architecture: &str, candidates: Vec<DnfCandidate>, facts: &mut PackageSourceFacts) -> Result<(), QueryReason> {
    let mut selected = None;
    let mut wrong_architecture = false;
    for candidate in candidates {
        if candidate.name != name { continue; }
        if ![&candidate.evr, &candidate.arch, &candidate.repository].iter().all(|f| field(f)) {
            return Err(QueryReason::InvalidResponse);
        }
        if candidate.arch != architecture && candidate.arch != "noarch" { wrong_architecture = true; continue; }
        if selected.is_some() { return Err(QueryReason::AmbiguousCandidate); }
        selected = Some(PackageCandidate { version: candidate.evr, architecture: candidate.arch,
            repositories: vec![Repository { id: candidate.repository, suite: None, trusted: None }], newer_than_installed: true });
    }
    if selected.is_none() && wrong_architecture { return Err(QueryReason::WrongArchitecture); }
    select(facts, selected, Some(QueryReason::PolicyUnobserved));
    Ok(())
}
