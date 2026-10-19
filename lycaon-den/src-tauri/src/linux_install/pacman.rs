use super::{run, InstalledPackage, PackageFamily};
use std::path::Path;

pub(super) fn owner(executable: &Path) -> Option<InstalledPackage> {
    let output = run("/usr/bin/pacman", &["-Qoq", "--", &executable.to_string_lossy()])?;
    let name = package_name(&output)?;
    let version = run("/usr/bin/pacman", &["-Q", "--", name])?;
    parse_version(name, &version)
}

fn package_name(output: &str) -> Option<&str> {
    let fields: Vec<_> = output.split_whitespace().collect();
    let [name] = fields.as_slice() else {
        return None;
    };
    (!name.starts_with('-')
        && name
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"@._+-".contains(&b)))
    .then_some(*name)
}

fn parse_version(name: &str, output: &str) -> Option<InstalledPackage> {
    let fields: Vec<_> = output.split_whitespace().collect();
    let [actual, version] = fields.as_slice() else {
        return None;
    };
    (*actual == name)
        .then(|| InstalledPackage::new(PackageFamily::Pacman, name.into(), (*version).into()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ownership_is_a_single_machine_query_name_not_localized_prose() {
        for name in ["painted-wolf-code", "painted-wolf-code-bin"] {
            assert_eq!(package_name(&format!("{name}\n")), Some(name));
            let package = parse_version(name, &format!("{name} 1:1.0.2pre.rc6-1\n")).unwrap();
            assert_eq!(package.family, PackageFamily::Pacman);
            assert_eq!(package.version, "1:1.0.2pre.rc6-1");
            assert_eq!(package.update_command, "sudo pacman -Syu");
        }
        for output in [
            "",
            "one\ntwo",
            "error: no package owns file",
            "-option",
            "bad/name",
        ] {
            assert_eq!(package_name(output), None);
        }
        assert!(parse_version("wanted", "other 1.0.2").is_none());
        assert!(parse_version("wanted", "wanted").is_none());
        assert!(parse_version("wanted", "wanted 1.0.2\nother 1.0.2").is_none());
    }
}
