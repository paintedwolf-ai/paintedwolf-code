//! Read shared host work facts before the explicit user exit transaction preserves resources.
use serde::{Deserialize, Serialize};
use std::path::Path;
use std::time::Duration;
use tauri::{AppHandle, Emitter, Manager, WebviewWindow};

#[cfg(target_os = "linux")]
#[path = "confirmation/linux.rs"]
mod dialog;
#[cfg(not(target_os = "linux"))]
#[path = "confirmation/native.rs"]
mod dialog;

pub(super) const TITLE: &str = "Quit Painted Wolf Code?";
pub(super) const QUIT: &str = "Quit";
pub(super) const CANCEL: &str = "Cancel";
pub(super) const KEEP_WORKING: &str = "Keep working";
pub(super) const DONT_ASK_AGAIN: &str = "Don't ask again";
pub(super) const OPEN_RUNNING: &str = "Open Running";
const QUIT_PREFERENCES_FILE: &str = "quit-preferences.json";

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum Choice {
    Quit { dont_ask_again: bool },
    OpenRunning,
    KeepWorking,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize)]
pub(super) struct BackgroundCommandFact {
    #[serde(default)]
    pub process_id: String,
    #[serde(default)]
    pub project_id: String,
    #[serde(default)]
    pub project_name: String,
    #[serde(default)]
    pub session_id: String,
    #[serde(default)]
    pub session_title: String,
    #[serde(default)]
    pub command: String,
    #[serde(default)]
    pub ports: Vec<String>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize)]
pub(super) struct WorkFacts {
    pub active_work_count: u64,
    #[serde(default)]
    pub background_processes: Vec<BackgroundCommandFact>,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub(super) struct QuitPreferences {
    #[serde(default)]
    pub suppress_background_quit_warning: bool,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(super) struct ConfirmationPrompt {
    pub title: &'static str,
    pub description: String,
    pub confirm_label: &'static str,
    pub cancel_label: &'static str,
    pub has_background_commands: bool,
    pub allow_dont_ask_again: bool,
    pub open_running_label: Option<&'static str>,
    pub running_project_id: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(super) struct ConfirmationResolution {
    pub approved: bool,
    pub open_running_project_id: Option<String>,
}

fn decode(raw: &[u8]) -> Result<WorkFacts, ()> {
    serde_json::from_slice::<WorkFacts>(raw).map_err(|_| ())
}

pub(super) async fn active_work(app: &AppHandle) -> Result<WorkFacts, ()> {
    let state = app.state::<crate::SidecarState>();
    let info = state.cached_info().ok_or(())?;
    let facts = fetch(&info).await?;
    let current = state.cached_info().ok_or(())?;
    if (current.port, current.generation, &current.api_token)
        != (info.port, info.generation, &info.api_token)
    {
        return Err(());
    }
    Ok(facts)
}

async fn fetch(info: &crate::sidecar::SidecarInfo) -> Result<WorkFacts, ()> {
    let client = crate::http_transport::client_builder()
        .no_proxy()
        .redirect(reqwest::redirect::Policy::none())
        .timeout(Duration::from_secs(3))
        .build()
        .map_err(|_| ())?;
    let mut response = client
        .get(format!("http://127.0.0.1:{}/v1/settings/power", info.port))
        .bearer_auth(&info.api_token)
        .send()
        .await
        .map_err(|_| ())?;
    if !response.status().is_success() {
        return Err(());
    }
    let mut raw = Vec::new();
    while let Some(chunk) = response.chunk().await.map_err(|_| ())? {
        if raw.len() + chunk.len() > 4096 {
            return Err(());
        }
        raw.extend_from_slice(&chunk);
    }
    decode(&raw)
}

fn format_command_line(command: &str, ports: &[String]) -> String {
    let trimmed = command.trim();
    let label = if trimmed.is_empty() {
        "Background command"
    } else {
        trimmed
    };
    let clean_ports: Vec<&str> = ports
        .iter()
        .map(|s| s.trim())
        .filter(|s| !s.is_empty())
        .collect();
    if clean_ports.is_empty() {
        format!("• {label}")
    } else {
        format!("• {label} ({})", clean_ports.join(", "))
    }
}

fn format_background_commands(procs: &[BackgroundCommandFact]) -> String {
    let mut project_order: Vec<String> = Vec::new();
    for proc in procs {
        if !project_order.contains(&proc.project_id) {
            project_order.push(proc.project_id.clone());
        }
    }
    if project_order.len() <= 1 {
        return procs
            .iter()
            .map(|p| format_command_line(&p.command, &p.ports))
            .collect::<Vec<_>>()
            .join("\n");
    }
    let mut sections = Vec::with_capacity(project_order.len());
    for project_id in &project_order {
        let group: Vec<&BackgroundCommandFact> = procs
            .iter()
            .filter(|p| &p.project_id == project_id)
            .collect();
        let heading = group
            .first()
            .and_then(|p| {
                let name = p.project_name.trim();
                if !name.is_empty() {
                    Some(name)
                } else {
                    let id = p.project_id.trim();
                    (!id.is_empty()).then_some(id)
                }
            })
            .unwrap_or("Project");
        let lines = group
            .iter()
            .map(|p| format_command_line(&p.command, &p.ports))
            .collect::<Vec<_>>()
            .join("\n");
        sections.push(format!("{heading}:\n{lines}"));
    }
    sections.join("\n\n")
}

pub(super) fn plan_confirmation(
    facts: Result<&WorkFacts, ()>,
    suppress_background_warning: bool,
) -> Option<ConfirmationPrompt> {
    let Ok(facts) = facts else {
        return Some(ConfirmationPrompt {
            title: TITLE,
            description: "Painted Wolf Code could not check for active work. Quitting may stop work. You can keep working instead.".into(),
            confirm_label: QUIT,
            cancel_label: KEEP_WORKING,
            has_background_commands: false,
            allow_dont_ask_again: false,
            open_running_label: None,
            running_project_id: None,
        });
    };
    let effective_bg = if suppress_background_warning {
        &[][..]
    } else {
        facts.background_processes.as_slice()
    };
    if effective_bg.is_empty() {
        if facts.active_work_count == 0 {
            return None;
        }
        return Some(ConfirmationPrompt {
            title: TITLE,
            description: "Work is still active. Quitting will stop it. You can keep working instead.".into(),
            confirm_label: QUIT,
            cancel_label: KEEP_WORKING,
            has_background_commands: false,
            allow_dont_ask_again: false,
            open_running_label: None,
            running_project_id: None,
        });
    }
    let count = effective_bg.len();
    let count_phrase = if count == 1 {
        "1 background command is running and will stop when Painted Wolf Code quits.".to_string()
    } else {
        format!(
            "{count} background commands are running and will stop when Painted Wolf Code quits."
        )
    };
    let intro = if facts.active_work_count > 0 {
        format!("Work is still active, and {count_phrase}")
    } else {
        count_phrase
    };
    let command_list = format_background_commands(effective_bg);
    let description = format!(
        "{intro}\n\n{command_list}\n\nOpen the Running pane to inspect or stop background commands."
    );
    let running_project_id = effective_bg
        .first()
        .map(|p| p.project_id.trim().to_string())
        .filter(|id| !id.is_empty());
    Some(ConfirmationPrompt {
        title: TITLE,
        description,
        confirm_label: QUIT,
        cancel_label: CANCEL,
        has_background_commands: true,
        allow_dont_ask_again: true,
        open_running_label: Some(OPEN_RUNNING),
        running_project_id,
    })
}

pub(super) fn load_quit_preferences(dir: &Path) -> QuitPreferences {
    let Ok(raw) = std::fs::read(dir.join(QUIT_PREFERENCES_FILE)) else {
        return QuitPreferences::default();
    };
    serde_json::from_slice::<QuitPreferences>(&raw).unwrap_or_default()
}

pub(super) fn save_quit_preferences(dir: &Path, prefs: &QuitPreferences) -> Result<(), String> {
    crate::config_dir::ensure_private_dir(dir).map_err(|e| e.to_string())?;
    let path = dir.join(QUIT_PREFERENCES_FILE);
    let tmp = dir.join(format!("quit-preferences.tmp-{}", uuid::Uuid::new_v4()));
    let raw = serde_json::to_vec_pretty(prefs).map_err(|e| e.to_string())?;
    if let Err(err) = std::fs::write(&tmp, &raw)
        .and_then(|()| crate::atomic_file::replace(&tmp, &path, true))
    {
        let _ = std::fs::remove_file(&tmp);
        return Err(err.to_string());
    }
    Ok(())
}

pub(super) fn resolve_choice(
    prompt: &ConfirmationPrompt,
    choice: Choice,
    prefs_dir: Option<&Path>,
) -> ConfirmationResolution {
    match choice {
        Choice::Quit { dont_ask_again } => {
            if dont_ask_again && prompt.allow_dont_ask_again {
                if let Some(dir) = prefs_dir {
                    let _ = save_quit_preferences(
                        dir,
                        &QuitPreferences {
                            suppress_background_quit_warning: true,
                        },
                    );
                }
            }
            ConfirmationResolution {
                approved: true,
                open_running_project_id: None,
            }
        }
        Choice::OpenRunning => ConfirmationResolution {
            approved: false,
            open_running_project_id: Some(
                prompt.running_project_id.clone().unwrap_or_default(),
            ),
        },
        Choice::KeepWorking => ConfirmationResolution {
            approved: false,
            open_running_project_id: None,
        },
    }
}

fn parent_label(windows: &[(String, bool)]) -> Option<&str> {
    let focused: Vec<_> = windows.iter().filter(|(_, focused)| *focused).collect();
    match focused.as_slice() {
        [window] => Some(window.0.as_str()),
        [] => windows
            .iter()
            .find(|(label, _)| label == "main")
            .map(|(label, _)| label.as_str()),
        _ => None,
    }
}

pub(super) fn parent_window(app: &AppHandle) -> Option<WebviewWindow> {
    let windows = app.webview_windows();
    let facts: Vec<_> = windows
        .iter()
        .map(|(label, window)| (label.clone(), window.is_focused().unwrap_or(false)))
        .collect();
    parent_label(&facts).and_then(|label| windows.get(label).cloned())
}

pub(super) async fn approve(parent: Option<WebviewWindow>, facts: Result<WorkFacts, ()>) -> bool {
    let prefs_dir = crate::config_dir::host_config_dir().ok();
    let suppress_bg = prefs_dir
        .as_deref()
        .map(load_quit_preferences)
        .unwrap_or_default()
        .suppress_background_quit_warning;
    let Some(prompt) = plan_confirmation(facts.as_ref().map_err(|_| ()), suppress_bg) else {
        return true;
    };
    let Some(parent) = parent else {
        return false;
    };
    if parent.show().is_err() {
        return false;
    }
    match dialog::show(&parent, &prompt).await {
        Ok(choice) => {
            let resolution = resolve_choice(&prompt, choice, prefs_dir.as_deref());
            if let Some(project_id) = resolution.open_running_project_id {
                let _ = parent.emit("app-exit-open-running", project_id);
            }
            resolution.approved
        }
        Err(error) => {
            eprintln!("Application exit confirmation could not open: {error}");
            false
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_support::TempDir;

    #[tokio::test]
    async fn work_observation_authenticates_and_refuses_failed_redirected_or_unbounded_responses() {
        use std::io::{Read, Write};
        for (status, body, expected) in [
            (
                "200 OK",
                r#"{"active_work_count":3}"#.to_string(),
                Ok(WorkFacts {
                    active_work_count: 3,
                    background_processes: Vec::new(),
                }),
            ),
            ("200 OK", "{}".to_string(), Err(())),
            ("401 Unauthorized", r#"{"active_work_count":0}"#.to_string(), Err(())),
            ("302 Found", r#"{"active_work_count":0}"#.to_string(), Err(())),
            ("200 OK", " ".repeat(4097), Err(())),
        ] {
            let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
            let port = listener.local_addr().unwrap().port();
            let server = std::thread::spawn(move || {
                let (mut connection, _) = listener.accept().unwrap();
                connection
                    .set_read_timeout(Some(Duration::from_secs(3)))
                    .unwrap();
                let mut request = Vec::new();
                while !request.ends_with(b"\r\n\r\n") {
                    let mut byte = [0];
                    connection.read_exact(&mut byte).unwrap();
                    request.push(byte[0]);
                    assert!(request.len() < 8192);
                }
                let request = String::from_utf8(request).unwrap().to_ascii_lowercase();
                assert!(request.starts_with("get /v1/settings/power http/1.1\r\n"));
                assert!(request.contains("\r\nauthorization: bearer fixture-token\r\n"));
                let response = format!(
                    "HTTP/1.1 {status}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                    body.len()
                );
                let _ = connection.write_all(response.as_bytes());
            });
            let info = crate::sidecar::SidecarInfo {
                port,
                api_token: "fixture-token".into(),
                generation: 1,
            };
            assert_eq!(fetch(&info).await, expected);
            server.join().unwrap();
        }
    }

    #[test]
    fn parent_is_the_initiating_owned_window_or_the_retained_main() {
        let windows = vec![("main".into(), false), ("peer".into(), true)];
        assert_eq!(parent_label(&windows), Some("peer"));
        assert_eq!(
            parent_label(&[("main".into(), false), ("peer".into(), false)]),
            Some("main")
        );
        assert_eq!(
            parent_label(&[("main".into(), true), ("peer".into(), true)]),
            None
        );
        assert_eq!(parent_label(&[("peer".into(), false)]), None);
        assert_eq!(parent_label(&[]), None);
    }

    #[test]
    fn only_explicit_idle_facts_bypass_the_choice() {
        let idle = WorkFacts {
            active_work_count: 0,
            background_processes: Vec::new(),
        };
        let active_fg = WorkFacts {
            active_work_count: 1,
            background_processes: Vec::new(),
        };
        assert_eq!(plan_confirmation(Ok(&idle), false), None);
        let fg_prompt = plan_confirmation(Ok(&active_fg), false).expect("active work prompts");
        let err_prompt = plan_confirmation(Err(()), false).expect("failed query prompts");
        assert!(!fg_prompt.has_background_commands);
        assert!(!fg_prompt.allow_dont_ask_again);
        assert_eq!(fg_prompt.cancel_label, KEEP_WORKING);
        assert!(!err_prompt.has_background_commands);
        assert!(!err_prompt.allow_dont_ask_again);
        assert_ne!(fg_prompt.description, err_prompt.description);
    }

    #[test]
    fn active_work_is_typed_and_independent_of_inhibitor_support_or_preference() {
        for raw in [
            br#"{"active_work_count":2,"supported":false,"keep_awake_while_working":false}"#
                .as_slice(),
            br#"{"active_work_count":2,"supported":true,"inhibiting":true}"#.as_slice(),
        ] {
            assert_eq!(
                decode(raw),
                Ok(WorkFacts {
                    active_work_count: 2,
                    background_processes: Vec::new(),
                })
            );
        }
        assert_eq!(
            decode(br#"{"active_work_count":0}"#),
            Ok(WorkFacts {
                active_work_count: 0,
                background_processes: Vec::new(),
            })
        );
        for raw in [
            b"{}".as_slice(),
            br#"{"active_work_count":-1}"#.as_slice(),
            br#"{"active_work_count":"0"}"#.as_slice(),
            br#"{"active_work_count":null}"#.as_slice(),
            b"bad".as_slice(),
        ] {
            assert_eq!(decode(raw), Err(()));
        }
    }

    #[test]
    fn background_commands_in_one_or_multiple_projects_are_listed_and_grouped() {
        let single_project = WorkFacts {
            active_work_count: 0,
            background_processes: vec![
                BackgroundCommandFact {
                    process_id: "bg_1".into(),
                    project_id: "proj-alpha".into(),
                    project_name: "Alpha".into(),
                    session_id: "s-1".into(),
                    session_title: "Chat 1".into(),
                    command: "python3 -m http.server 8000".into(),
                    ports: vec!["127.0.0.1:8000".into()],
                },
                BackgroundCommandFact {
                    process_id: "bg_2".into(),
                    project_id: "proj-alpha".into(),
                    project_name: "Alpha".into(),
                    session_id: "s-2".into(),
                    session_title: "Chat 2".into(),
                    command: "npm run watch".into(),
                    ports: Vec::new(),
                },
            ],
        };
        let prompt = plan_confirmation(Ok(&single_project), false).expect("background prompt");
        assert!(prompt.has_background_commands);
        assert!(prompt.allow_dont_ask_again);
        assert_eq!(prompt.confirm_label, QUIT);
        assert_eq!(prompt.cancel_label, CANCEL);
        assert_eq!(prompt.open_running_label, Some(OPEN_RUNNING));
        assert_eq!(prompt.running_project_id.as_deref(), Some("proj-alpha"));
        assert!(prompt.description.contains(
            "2 background commands are running and will stop when Painted Wolf Code quits."
        ));
        assert!(prompt
            .description
            .contains("• python3 -m http.server 8000 (127.0.0.1:8000)"));
        assert!(prompt.description.contains("• npm run watch"));
        assert!(!prompt.description.contains("Alpha:"));

        let multi_project = WorkFacts {
            active_work_count: 0,
            background_processes: vec![
                BackgroundCommandFact {
                    process_id: "bg_1".into(),
                    project_id: "proj-alpha".into(),
                    project_name: "Alpha".into(),
                    session_id: "s-1".into(),
                    session_title: "Chat 1".into(),
                    command: "python3 -m http.server 8000".into(),
                    ports: vec!["127.0.0.1:8000".into()],
                },
                BackgroundCommandFact {
                    process_id: "bg_2".into(),
                    project_id: "proj-beta".into(),
                    project_name: "Beta".into(),
                    session_id: "s-2".into(),
                    session_title: "Chat 2".into(),
                    command: "npm run dev".into(),
                    ports: vec!["127.0.0.1:5173".into()],
                },
            ],
        };
        let grouped = plan_confirmation(Ok(&multi_project), false).expect("grouped prompt");
        assert!(grouped
            .description
            .contains("Alpha:\n• python3 -m http.server 8000 (127.0.0.1:8000)"));
        assert!(grouped
            .description
            .contains("Beta:\n• npm run dev (127.0.0.1:5173)"));
    }

    #[test]
    fn dont_ask_again_preference_persists_and_never_suppresses_foreground_work_or_failed_queries() {
        let dir = TempDir::new("quit-preferences-test");
        assert!(!load_quit_preferences(&dir).suppress_background_quit_warning);

        let bg_only = WorkFacts {
            active_work_count: 0,
            background_processes: vec![BackgroundCommandFact {
                process_id: "bg_1".into(),
                project_id: "proj-alpha".into(),
                project_name: "Alpha".into(),
                session_id: "s-1".into(),
                session_title: "Chat 1".into(),
                command: "python3 -m http.server 8000".into(),
                ports: vec!["127.0.0.1:8000".into()],
            }],
        };
        let prompt = plan_confirmation(Ok(&bg_only), false).expect("asks first time");
        let res = resolve_choice(
            &prompt,
            Choice::Quit {
                dont_ask_again: true,
            },
            Some(&dir),
        );
        assert!(res.approved);
        assert!(load_quit_preferences(&dir).suppress_background_quit_warning);

        // Once suppressed, background-only quit is silent.
        assert_eq!(plan_confirmation(Ok(&bg_only), true), None);

        // Foreground active work alongside background commands still prompts even when background warning is suppressed!
        let combined = WorkFacts {
            active_work_count: 1,
            background_processes: bg_only.background_processes.clone(),
        };
        let unsuppressed_combined =
            plan_confirmation(Ok(&combined), false).expect("combined prompt");
        assert!(unsuppressed_combined.description.contains(
            "Work is still active, and 1 background command is running and will stop when Painted Wolf Code quits."
        ));
        let suppressed_combined =
            plan_confirmation(Ok(&combined), true).expect("active work still prompts");
        assert!(!suppressed_combined.has_background_commands);
        assert!(!suppressed_combined.allow_dont_ask_again);
        assert_eq!(
            suppressed_combined.description,
            "Work is still active. Quitting will stop it. You can keep working instead."
        );

        // Failed inventory query is never suppressed by the background-only preference.
        let failed = plan_confirmation(Err(()), true).expect("failed query still prompts");
        assert!(!failed.has_background_commands);
        assert!(!failed.allow_dont_ask_again);

        // Cancel and OpenRunning do not approve exit or persist the preference.
        let cancel_dir = TempDir::new("quit-preferences-cancel");
        let cancel_res = resolve_choice(&prompt, Choice::KeepWorking, Some(&cancel_dir));
        assert!(!cancel_res.approved);
        assert!(cancel_res.open_running_project_id.is_none());
        assert!(!load_quit_preferences(&cancel_dir).suppress_background_quit_warning);

        let open_res = resolve_choice(&prompt, Choice::OpenRunning, Some(&cancel_dir));
        assert!(!open_res.approved);
        assert_eq!(open_res.open_running_project_id.as_deref(), Some("proj-alpha"));
    }
}
