use super::{command::{field, success, Reader}, query::select, *};
use serde::Deserialize;

const PROBE: &str = include_str!("apt_probe.py");

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Apt {
    schema: u32,
    adapter_available: bool,
    installed: Option<String>,
    architecture: Option<String>,
    held: Option<bool>,
    candidate: Option<Candidate>,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Candidate {
    version: String,
    architecture: String,
    newer: bool,
    priority: i64,
    origins: Vec<Origin>,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Origin { id: String, suite: String, trusted: bool }

pub(super) fn observe(name: &str, reader: &mut impl Reader, facts: &mut PackageSourceFacts) -> Result<(), QueryReason> {
    let raw = success(reader, "/usr/bin/python3", &["-I", "-c", PROBE, name])?;
    project(&raw, facts)
}

pub(super) fn project(raw: &str, facts: &mut PackageSourceFacts) -> Result<(), QueryReason> {
    let report: Apt = serde_json::from_str(raw).map_err(|_| QueryReason::InvalidResponse)?;
    if report.schema != 1 { return Err(QueryReason::InvalidResponse); }
    if !report.adapter_available { return Err(QueryReason::AdapterUnavailable); }
    let Some(installed) = report.installed else {
        if report.architecture.is_some() || report.held.is_some() || report.candidate.is_some() {
            return Err(QueryReason::InvalidResponse);
        }
        select(facts, None, Some(QueryReason::NotInstalled)); return Ok(());
    };
    let (Some(architecture), Some(held)) = (report.architecture, report.held) else {
        return Err(QueryReason::InvalidResponse);
    };
    if !field(&installed) || !field(&architecture) { return Err(QueryReason::InvalidResponse); }
    facts.database_version = Some(installed);
    facts.architecture = Some(architecture.clone());
    facts.held = Some(held);
    let Some(candidate) = report.candidate else {
        select(facts, None, Some(QueryReason::NoCachedCandidate)); return Ok(());
    };
    if !field(&candidate.version) || !field(&candidate.architecture) || candidate.origins.len() > 16 {
        return Err(QueryReason::InvalidResponse);
    }
    if candidate.architecture != architecture && candidate.architecture != "all" {
        return Err(QueryReason::WrongArchitecture);
    }
    facts.selection_priority = Some(candidate.priority);
    let mut repositories = Vec::new();
    for origin in candidate.origins {
        if !field(&origin.id) || !field(&origin.suite) { return Err(QueryReason::InvalidResponse); }
        repositories.push(Repository { id: origin.id, suite: Some(origin.suite), trusted: Some(origin.trusted) });
    }
    let reason = if held { Some(QueryReason::Held) }
        else if repositories.is_empty() { Some(QueryReason::NoCachedCandidate) }
        else if repositories.iter().any(|r| r.trusted != Some(true)) { Some(QueryReason::UntrustedSource) }
        else { None };
    select(facts, Some(PackageCandidate { version: candidate.version, architecture: candidate.architecture,
        repositories, newer_than_installed: candidate.newer }), reason);
    Ok(())
}
