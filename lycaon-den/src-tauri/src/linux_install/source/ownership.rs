//! A fresh database attribution of the original installed executable path.
use super::{command::{package_name, success, Reader}, PackageFamily, QueryReason};
use std::path::Path;

pub(super) fn read(family: PackageFamily, executable: &Path, reader: &mut impl Reader) -> Result<String, QueryReason> {
    let path = executable.to_str().filter(|_| executable.is_absolute()).ok_or(QueryReason::InvalidResponse)?;
    match family {
        PackageFamily::Deb => {
            let raw = success(reader, "/usr/bin/dpkg-query", &["-S", path])?;
            crate::linux_install::parse_dpkg_owner(&raw, executable).ok_or(QueryReason::InvalidResponse)
        },
        PackageFamily::Rpm => one_name(&success(reader, "/usr/bin/rpm", &["-qf", "--qf", "%{NAME}\n", "--", path])?),
        PackageFamily::Pacman => one_name(&success(reader, "/usr/bin/pacman", &["-Qoq", "--", path])?),
        PackageFamily::Nix => Err(QueryReason::UnsupportedOwner),
    }
}

fn one_name(raw: &str) -> Result<String, QueryReason> {
    let name = raw.trim();
    if !package_name(name) { return Err(QueryReason::InvalidResponse); }
    Ok(name.into())
}
