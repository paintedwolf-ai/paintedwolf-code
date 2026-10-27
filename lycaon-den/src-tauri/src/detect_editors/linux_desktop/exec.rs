//! Desktop Exec argument semantics, including Flatpak forwarding regions.

use std::path::Path;

#[derive(Debug, Clone, PartialEq, Eq)]
pub(super) enum Argument {
    Literal(String),
    File { uri: bool },
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct DesktopCommand {
    pub(crate) program: String,
    pub(super) arguments: Vec<Argument>,
}

pub(super) fn parse(exec: &str) -> Option<DesktopCommand> {
    let mut arguments = Vec::new();
    let (mut current, mut quoted, mut started, mut field) = (String::new(), false, false, None);
    let mut chars = exec.chars().peekable();
    while let Some(ch) = chars.next() {
        match ch {
            '"' => {
                quoted = !quoted;
                started = true;
            }
            '\\' if quoted => {
                current.push(chars.next()?);
                started = true;
            }
            '%' if !quoted => match chars.next()? {
                '%' => {
                    current.push('%');
                    started = true;
                }
                code @ ('f' | 'F' | 'u' | 'U') => {
                    if started || chars.peek().is_some_and(|ch| !ch.is_whitespace()) {
                        return None;
                    }
                    field = Some(matches!(code, 'u' | 'U'));
                    started = true;
                }
                _ => {}
            },
            ' ' | '\t' if !quoted => {
                push(&mut arguments, &mut current, &mut started, &mut field);
            }
            other => {
                current.push(other);
                started = true;
            }
        }
    }
    if quoted {
        return None;
    }
    push(&mut arguments, &mut current, &mut started, &mut field);
    if arguments.is_empty() {
        return None;
    }
    let Argument::Literal(program) = arguments.remove(0) else {
        return None;
    };
    if program.is_empty() {
        return None;
    }
    Some(DesktopCommand { program, arguments })
}

fn push(
    arguments: &mut Vec<Argument>,
    current: &mut String,
    started: &mut bool,
    field: &mut Option<bool>,
) {
    if let Some(uri) = field.take() {
        arguments.push(Argument::File { uri });
    } else if *started && !current.is_empty() {
        arguments.push(Argument::Literal(std::mem::take(current)));
    }
    *started = false;
}

impl DesktopCommand {
    /// The Flatpak app id comes from the launcher's structured argv.
    pub(crate) fn flatpak_app(&self) -> Result<Option<String>, String> {
        if Path::new(&self.program)
            .file_name()
            .and_then(|name| name.to_str())
            != Some("flatpak")
        {
            return Ok(None);
        }
        let mut words = self.arguments.iter();
        if words.next() != Some(&Argument::Literal("run".into())) {
            return Err("The editor's Flatpak command is not a run operation.".into());
        }
        while let Some(argument) = words.next() {
            let Argument::Literal(word) = argument else {
                break;
            };
            if word.starts_with("--") {
                if !word.contains('=')
                    && matches!(
                        word.as_str(),
                        "--arch"
                            | "--branch"
                            | "--command"
                            | "--installation"
                            | "--env"
                            | "--filesystem"
                            | "--socket"
                            | "--device"
                            | "--share"
                            | "--talk-name"
                            | "--own-name"
                            | "--persist"
                            | "--cwd"
                    )
                {
                    if !matches!(words.next(), Some(Argument::Literal(_))) {
                        break;
                    }
                }
                continue;
            }
            if word.split('.').count() >= 3
                && word
                    .bytes()
                    .all(|ch| ch.is_ascii_alphanumeric() || b"._-".contains(&ch))
            {
                return Ok(Some(word.clone()));
            }
            break;
        }
        Err("The editor's Flatpak application id is unavailable.".into())
    }

    /// Catalog navigation is inserted where the desktop file expects files.
    /// Flatpak exports have already been granted for the exact original path;
    /// forwarding delimiters are consumed before formatting path:line arguments.
    pub(crate) fn arguments(&self, cli: &[String], exported: bool) -> Result<Vec<String>, String> {
        let mut result = Vec::new();
        let mut inserted = false;
        let mut forwarding = false;
        for argument in &self.arguments {
            match argument {
                Argument::Literal(marker) if marker == "@@" || marker == "@@u" => {
                    if !exported {
                        return Err("The editor requires document portal forwarding.".into());
                    }
                    if marker == "@@u" && forwarding {
                        return Err("The editor has invalid forwarding markers.".into());
                    }
                    forwarding = !forwarding;
                }
                Argument::File { .. } => {
                    if !inserted {
                        result.extend_from_slice(cli);
                        inserted = true;
                    }
                }
                Argument::Literal(value) => result.push(value.clone()),
            }
        }
        if forwarding {
            return Err("The editor has an unclosed forwarding region.".into());
        }
        if !inserted {
            result.extend_from_slice(cli);
        }
        Ok(result)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn fields_percent_literals_quoting_and_forwarding_keep_their_meanings() {
        let command = parse(r#"/usr/bin/flatpak run --command=code --file-forwarding com.visualstudio.code @@u %U @@"#).unwrap();
        assert_eq!(
            command.flatpak_app().unwrap(),
            Some("com.visualstudio.code".into())
        );
        assert!(command.arguments.contains(&Argument::File { uri: true }));
        let cli = vec!["-g".into(), "/run/user/1000/doc/abc/a é.txt:42".into()];
        assert_eq!(
            command.arguments(&cli, true).unwrap(),
            [
                "run",
                "--command=code",
                "--file-forwarding",
                "com.visualstudio.code",
                "-g",
                "/run/user/1000/doc/abc/a é.txt:42"
            ]
        );
        assert!(command.arguments(&cli, false).is_err());
        let literal =
            parse(r#""/opt/My Editor/editor" --name="a \"quoted\" name" 100%% %%F %f --after"#)
                .unwrap();
        assert_eq!(literal.program, "/opt/My Editor/editor");
        assert_eq!(
            literal.arguments(&["a file".into()], false).unwrap(),
            [
                "--name=a \"quoted\" name",
                "100%",
                "%F",
                "a file",
                "--after"
            ]
        );
    }

    #[test]
    fn malformed_commands_and_forwarding_are_rejected() {
        for exec in ["", "   ", "\"unterminated", "code --arg=%F", "%F code"] {
            assert!(parse(exec).is_none(), "{exec}");
        }
        let command = parse("flatpak run com.example.Editor @@u %U").unwrap();
        assert!(command.arguments(&["/export/file".into()], true).is_err());
        assert!(parse("flatpak run --command code %F")
            .unwrap()
            .flatpak_app()
            .is_err());
    }
}
