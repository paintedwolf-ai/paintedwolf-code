use super::{command::{field, success, Reader}, query::select, *};

pub(super) fn observe(name: &str, reader: &mut impl Reader, facts: &mut PackageSourceFacts) -> Result<(), QueryReason> {
    let installed = reader.read("/usr/bin/pacman", &["-Q", "--", name])?;
    if installed.code != 0 { select(facts, None, Some(QueryReason::NotInstalled)); return Ok(()); }
    let fields: Vec<_> = installed.stdout.split_whitespace().collect();
    if fields.len() != 2 || fields[0] != name || !field(fields[1]) { return Err(QueryReason::InvalidResponse); }
    facts.database_version = Some(fields[1].into());
    let ignored = success(reader, "/usr/bin/pacman-conf", &["IgnorePkg"])?;
    let groups = success(reader, "/usr/bin/pacman-conf", &["IgnoreGroup"])?;
    let architecture = success(reader, "/usr/bin/pacman-conf", &["Architecture"])?;
    let architectures: Vec<_> = architecture.split_whitespace().collect();
    if architectures.is_empty() || !architectures.iter().all(|a| field(a)) { return Err(QueryReason::InvalidResponse); }
    facts.architecture = Some(architectures.join(","));
    let held = ignored.split_whitespace().any(|pattern| matches_ignore(pattern, name));
    facts.held = if held { Some(true) } else if groups.trim().is_empty() { Some(false) } else { None };
    // No refresh flag or target is supplied: this is the configured full-system
    // print-only upgrade decision, including IgnorePkg and repository priority.
    let raw = success(reader, "/usr/bin/pacman", &["-Su", "--print", "--print-format", "%n\t%v\t%a\t%r"])?;
    let mut candidate = None;
    for line in raw.lines() {
        let fields: Vec<_> = line.split('\t').collect();
        if fields.len() != 4 || !fields.iter().all(|f| field(f)) { return Err(QueryReason::InvalidResponse); }
        if fields[0] != name { continue; }
        if !architectures.contains(&fields[2]) && fields[2] != "any" { return Err(QueryReason::WrongArchitecture); }
        if candidate.is_some() { return Err(QueryReason::AmbiguousCandidate); }
        candidate = Some(PackageCandidate { version: fields[1].into(), architecture: fields[2].into(),
            repositories: vec![Repository { id: fields[3].into(), suite: None, trusted: None }], newer_than_installed: true });
    }
    let reason = if held { Some(QueryReason::Held) } else if facts.held.is_none() { Some(QueryReason::PolicyUnobserved) } else { None };
    select(facts, candidate, reason);
    Ok(())
}

fn matches_ignore(pattern: &str, name: &str) -> bool {
    #[cfg(unix)]
    {
        let (Ok(pattern), Ok(name)) = (std::ffi::CString::new(pattern), std::ffi::CString::new(name)) else { return false; };
        unsafe { libc::fnmatch(pattern.as_ptr(), name.as_ptr(), 0) == 0 }
    }
    #[cfg(not(unix))]
    { let _ = (pattern, name); false }
}
