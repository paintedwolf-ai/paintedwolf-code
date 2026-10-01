use super::*;
use super::{daemon::*, diagnostics::*, layout::*, logging::*, process::*, startup::*};
use crate::test_support::TempDir;
use std::{
    fs,
    io::BufReader,
    process::Command,
    time::{Duration, Instant},
};

#[test]
fn engine_stderr_rotates_without_a_process_restart() {
    let directory = crate::test_support::TempDir::new("continuous-engine-log");
    let path = directory.join("engine.log");
    let input = vec![b'x'; ENGINE_LOG_MAX_BYTES as usize * 3 + 17];
    drain_engine_stderr(std::io::Cursor::new(input), Some(path.clone()));
    assert!(fs::metadata(&path).expect("current log").len() <= ENGINE_LOG_MAX_BYTES);
    assert!(
        fs::metadata(path.with_extension("log.1"))
            .expect("previous log")
            .len()
            <= ENGINE_LOG_MAX_BYTES
    );
    assert_eq!(fs::read(path).expect("latest output"), vec![b'x'; 17]);
}

#[test]
fn store_locked_exit_code_matches_engine() {
    let src = std::fs::read_to_string(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../lycaon/internal/hostlock/hostlock.go"),
    )
    .expect("read engine hostlock.go");
    let needle = "const ExitCodeStoreInstanceLocked = ";
    let at = src
        .find(needle)
        .expect("engine no longer declares ExitCodeStoreInstanceLocked");
    let value: i32 = src[at + needle.len()..]
        .lines()
        .next()
        .expect("constant line")
        .trim()
        .parse()
        .expect("constant is an integer literal");
    assert_eq!(
        value, STORE_INSTANCE_LOCKED_EXIT,
        "engine exit code and shell mirror disagree",
    );
}

#[test]
fn engine_graceful_stop_exceeds_the_engine_shutdown_budget() {
    let src = std::fs::read_to_string(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../../lycaon/internal/app/serve.go"),
    )
    .expect("read engine serve.go");
    let needle = "const ShutdownBudgetSeconds = ";
    let at = src
        .find(needle)
        .expect("engine no longer declares ShutdownBudgetSeconds");
    let budget: u64 = src[at + needle.len()..]
        .lines()
        .next()
        .expect("constant line")
        .trim()
        .parse()
        .expect("constant is an integer literal");

    assert!(
        GRACEFUL_STOP_TIMEOUT > Duration::from_secs(budget),
        "shell waits {:?} but the engine's ordered shutdown may take {budget}s; \
         a kill inside that window loses the clean-shutdown marker",
        GRACEFUL_STOP_TIMEOUT,
    );
}

#[test]
fn sidecar_state_defaults() {
    let state = SidecarState::new();
    assert!(state.process.lock().unwrap().is_none());
    assert_eq!(*state.port.lock().unwrap(), 0);
    assert!(state.api_token.lock().unwrap().is_empty());
    assert!(!state.starting.load(Ordering::Acquire));
    assert!(!state.cancel_start.load(Ordering::Acquire));
}

#[test]
fn bundled_layout_uses_platform_resources_and_sidecar_name() {
    let temp = TempDir::new("bundled-layout");
    for (target_os, sidecar_name) in [("macos", "pw"), ("linux", "pw"), ("windows", "pw.exe")] {
        let root = temp.join(target_os);
        let executable = root.join("bin").join(if target_os == "windows" {
            "painted-wolf-code.exe"
        } else {
            "painted-wolf-code"
        });
        let sidecar = if target_os == "macos" {
            root.join("Helpers/Painted Wolf Code engine.app/Contents/MacOS/pw")
        } else {
            root.join("bin").join(sidecar_name)
        };
        std::fs::create_dir_all(sidecar.parent().unwrap()).unwrap();
        let resources = root.join("native-resources");
        let marker = resources
            .join(BUNDLED_ENGINE_RESOURCES_DIR)
            .join(BUNDLED_ENGINE_MARKER);
        std::fs::create_dir_all(executable.parent().expect("executable parent"))
            .expect("create executable parent");
        std::fs::create_dir_all(marker.parent().expect("marker parent"))
            .expect("create engine marker parent");
        std::fs::write(&executable, b"app").expect("write app");
        std::fs::write(&sidecar, b"sidecar").expect("write sidecar");
        std::fs::write(&marker, b"schema").expect("write marker");

        let layout = bundled_engine_layout_from(&executable, &resources, target_os)
            .expect("resolve bundled layout");
        assert_eq!(layout.binary, sidecar);
        assert_eq!(
            layout.engine_root,
            resources.join(BUNDLED_ENGINE_RESOURCES_DIR)
        );
    }
}

#[test]
fn bundled_layout_requires_resource_marker() {
    let temp = TempDir::new("bundled-layout-missing-marker");
    let executable = temp.join("bin/painted-wolf-code");
    let sidecar = temp.join("Helpers/Painted Wolf Code engine.app/Contents/MacOS/pw");
    std::fs::create_dir_all(sidecar.parent().unwrap()).unwrap();
    let resources = temp.join("resources");
    std::fs::create_dir_all(executable.parent().expect("executable parent"))
        .expect("create executable parent");
    std::fs::create_dir_all(&resources).expect("create resources");
    std::fs::write(&executable, b"app").expect("write app");
    std::fs::write(&sidecar, b"sidecar").expect("write sidecar");

    assert!(bundled_engine_layout_from(&executable, &resources, "macos").is_none());
}

#[test]
fn reveal_signature_is_url_safe_and_protocol_bound() {
    use ed25519_dalek::{Signature, Verifier};

    let state = SidecarState::new();
    let payload = "eyJjaGFsbGVuZ2VfaWQiOiJmaXh0dXJlIn0";
    let authenticator = "macos_user_presence";
    let encoded = state.sign_reveal(payload, authenticator);
    let bytes = URL_SAFE_NO_PAD
        .decode(encoded)
        .expect("decode reveal signature");
    let signature = Signature::from_slice(&bytes).expect("parse reveal signature");
    let message =
        format!("painted-wolf-managed-secret-reveal-v1\n{payload}\nauthenticator={authenticator}");

    state
        .reveal_signing_key
        .verifying_key()
        .verify(message.as_bytes(), &signature)
        .expect("signature covers the host protocol message");
    assert!(state
        .reveal_signing_key
        .verifying_key()
        .verify(
            format!(
                "painted-wolf-managed-secret-reveal-v1\n{payload}\nauthenticator=windows_user_presence"
            )
            .as_bytes(),
            &signature,
        )
        .is_err());
}

#[test]
fn generate_api_token_is_url_safe_and_nonempty() {
    let token = generate_api_token();
    assert!(!token.is_empty());
    assert!(token
        .chars()
        .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_'));
}

#[test]
fn log_tail_survives_a_utf8_codepoint_split_at_the_byte_boundary() {
    let dir = TempDir::new("utf8-log-tail");
    let path = dir.path().join("engine.log");
    fs::write(&path, "🐺abc").expect("write engine log");

    let tail = read_log_tail(&path, 5).expect("read engine log tail");

    assert!(
        tail.ends_with("abc"),
        "tail lost valid log suffix: {tail:?}"
    );
    assert!(
        !tail.is_empty(),
        "split UTF-8 must not erase the diagnostic tail"
    );
}

#[test]
fn log_tail_returns_only_the_requested_suffix() {
    let dir = TempDir::new("bounded-log-tail");
    let path = dir.path().join("engine.log");
    fs::write(&path, "prefix\nlast").expect("write engine log");

    let tail = read_log_tail(&path, 4).expect("read engine log tail");

    assert_eq!(tail, "last");
}

#[test]
fn startup_protocol_accepts_ordered_records_from_the_spawned_child() {
    let record: StartupRecord = serde_json::from_str(
        r#"{"protocol":1,"sequence":2,"pid":4242,"kind":"ready","phase":"ready","elapsed_ms":15634,"port":64421}"#,
    )
    .expect("parse startup record");

    validate_startup_record(&record, 4242, 1).expect("validate startup record");
}

#[test]
fn startup_protocol_rejects_stale_pid_sequence_and_ready_without_port() {
    let record: StartupRecord = serde_json::from_str(
        r#"{"protocol":1,"sequence":4,"pid":4242,"kind":"ready","phase":"ready","elapsed_ms":10}"#,
    )
    .expect("parse startup record");

    assert!(validate_startup_record(&record, 9999, 3).is_err());
    assert!(validate_startup_record(&record, 4242, 2).is_err());
    assert!(validate_startup_record(&record, 4242, 3).is_err());
}

#[test]
fn startup_protocol_rejects_mixed_terminal_fields() {
    let ready_with_code: StartupRecord = serde_json::from_str(
        r#"{"protocol":1,"sequence":1,"pid":4242,"kind":"ready","phase":"ready","elapsed_ms":10,"port":64421,"code":"failed"}"#,
    )
    .expect("parse ready record");
    let failed_with_port: StartupRecord = serde_json::from_str(
        r#"{"protocol":1,"sequence":1,"pid":4242,"kind":"failed","phase":"providers","elapsed_ms":10,"port":64421,"code":"failed"}"#,
    )
    .expect("parse failed record");

    assert!(validate_startup_record(&ready_with_code, 4242, 0).is_err());
    assert!(validate_startup_record(&failed_with_port, 4242, 0).is_err());
}

#[test]
fn startup_protocol_reader_bounds_each_record() {
    let oversized = vec![b'x'; STARTUP_RECORD_MAX_BYTES + 1];
    let mut reader = BufReader::new(std::io::Cursor::new(oversized));
    let err = read_bounded_startup_line(&mut reader).expect_err("oversized startup record");
    assert!(err.contains("exceeded"), "unexpected error: {err}");
}

#[cfg(unix)]
#[test]
fn startup_lifecycle_waits_for_ready_without_a_deadline() {
    let state = SidecarState::new();
    let script = r#"
printf '{"protocol":1,"sequence":1,"pid":%s,"kind":"phase","phase":"providers","elapsed_ms":18000}\n' "$$"
printf '{"protocol":1,"sequence":2,"pid":%s,"kind":"ready","phase":"ready","elapsed_ms":18100,"port":43123}\n' "$$"
sleep 30
"#;
    let mut child = Command::new("sh")
        .args(["-c", script])
        .stdout(Stdio::piped())
        .spawn()
        .expect("spawn protocol fixture");
    let pid = child.id();
    let stdout = child.stdout.take().expect("fixture stdout");
    *state.process.lock().unwrap() = Some(child);
    state.starting.store(true, Ordering::Release);
    let emitted = Mutex::new(Vec::new());

    let port = await_child_ready(&state, pid, stdout, &|progress| {
        emitted.lock().unwrap().push(progress);
    })
    .expect("wait for protocol ready");

    assert_eq!(port, 43123);
    assert_eq!(
        emitted.lock().unwrap().last().map(|event| event.phase),
        Some(StartupPhase::Providers)
    );
    kill_child(&mut *state.process.lock().unwrap());
}

#[test]
fn cancel_marks_only_an_active_startup() {
    let state = SidecarState::new();
    assert!(!cancel_sidecar_start(&state));

    state.starting.store(true, Ordering::Release);
    assert!(cancel_sidecar_start(&state));
    assert!(state.cancel_start.load(Ordering::Acquire));
}

#[test]
fn attach_live_dev_daemon_when_present() {
    if std::env::var("LYCAON_TEST_LIVE_ATTACH").ok().as_deref() != Some("1") {
        return;
    }
    let info = attach_existing_daemon().expect("attach call");
    assert!(info.is_some(), "expected live daemon on 8787");
}

#[test]
fn attach_only_reports_missing_daemon_instead_of_spawning() {
    let dir = TempDir::new("attach");
    let err = attach_or_explain(dir.path()).expect_err("attach without daemon");
    let SidecarStartError::EngineNotStarted(message) = err else {
        panic!("attach-only must name its own code, not fall through to failed: {err:?}");
    };
    assert!(
        message.contains("no engine is running") && message.contains("./task den:sidecar"),
        "unexpected error: {message}"
    );
}

#[test]
fn full_debug_logging_sets_only_main_switch() {
    let mut cmd = Command::new("true");
    apply_full_debug_logging_env(&mut cmd);

    let envs: Vec<(String, Option<String>)> = cmd
        .get_envs()
        .map(|(key, value)| {
            (
                key.to_string_lossy().into_owned(),
                value.map(|value| value.to_string_lossy().into_owned()),
            )
        })
        .collect();
    assert_eq!(
        envs,
        vec![("LYCAON_DEBUG_ALL".to_string(), Some("1".to_string()))],
        "the engine expands the main switch, so the shell sets no per-area switch"
    );
}

#[test]
fn full_debug_logging_reads_app_state_flag() {
    let dir = TempDir::new("debug-flag");
    let cfg = dir.path();

    assert!(!full_debug_logging_enabled_in(cfg));

    crate::apply_app_state_patch_at(
        cfg,
        &[(
            "debug".to_string(),
            serde_json::json!({"verboseMode": true}),
        )]
        .into_iter()
        .collect(),
    )
    .expect("write app state");
    assert!(!full_debug_logging_enabled_in(cfg));

    crate::apply_app_state_patch_at(
        cfg,
        &[(
            ("debug").to_string(),
            serde_json::json!({"fullDebugLogging": true}),
        )]
        .into_iter()
        .collect(),
    )
    .expect("write app state");
    assert!(full_debug_logging_enabled_in(cfg));
}

#[test]
fn stop_sidecar_clears_cached_token() {
    let state = SidecarState::new();
    *state.port.lock().unwrap() = 8787;
    *state.api_token.lock().unwrap() = "secret".into();
    stop_sidecar(&state);
    assert_eq!(*state.port.lock().unwrap(), 0);
    assert!(state.api_token.lock().unwrap().is_empty());
}

#[test]
fn vault_password_frame_is_length_prefixed_and_exact() {
    let mut frame = Vec::new();
    write_vault_password(&mut frame, "correct horse battery staple".as_bytes())
        .expect("write password frame");
    assert_eq!(
        &frame[..4],
        &("correct horse battery staple".len() as u32).to_be_bytes()
    );
    assert_eq!(&frame[4..], b"correct horse battery staple");
}

#[test]
fn vault_password_frame_rejects_empty_and_oversized_values() {
    assert!(write_vault_password(Vec::new(), b"").is_err());
    assert!(write_vault_password(Vec::new(), &vec![b'x'; VAULT_PASSWORD_MAX_BYTES + 1]).is_err());
}

#[cfg(unix)]
#[test]
fn startup_diagnostics_child_takes_no_input_and_answers_on_stdout() {
    let dir = TempDir::new("startup-diagnostics-stdio");
    let mut child = startup_diagnostics_command(Path::new("/usr/bin/true"), dir.path())
        .spawn()
        .expect("spawn diagnostics stand-in");

    assert!(child.stdin.is_none(), "the child gets no input channel");
    assert!(child.stdout.is_some(), "the bundle arrives over stdout");
    assert!(child.stderr.is_none(), "engine chatter is discarded");

    child.wait().expect("wait for diagnostics stand-in");
}

#[test]
fn startup_diagnostics_stays_a_narrow_permitted_engine_command() {
    let dir = TempDir::new("startup-diagnostics");
    let cmd = startup_diagnostics_command(Path::new("/engine/pw"), dir.path());

    assert_eq!(cmd.get_program(), Path::new("/engine/pw").as_os_str());
    let args: Vec<String> = cmd
        .get_args()
        .map(|arg| arg.to_string_lossy().into_owned())
        .collect();
    assert_eq!(
        args,
        ["diagnostics", "startup-diagnostics"],
        "the shell runs the one diagnostics subcommand, never the engine itself"
    );
    let config_dir = cmd
        .get_envs()
        .find(|(key, _)| *key == std::ffi::OsStr::new("LYCAON_CONFIG_DIR"))
        .and_then(|(_, value)| value);
    assert_eq!(config_dir, Some(dir.path().as_os_str()));

    let manifest_dir = std::path::Path::new(env!("CARGO_MANIFEST_DIR"));
    let permissions = std::fs::read_to_string(manifest_dir.join("permissions/sidecar.toml"))
        .expect("read sidecar permissions");
    assert!(
        permissions.contains("allow-export-startup-diagnostics"),
        "startup diagnostics needs an explicit shell permission"
    );
    let capabilities = std::fs::read_to_string(manifest_dir.join("capabilities/default.json"))
        .expect("read default capability");
    assert!(
        capabilities.contains("allow-export-startup-diagnostics"),
        "main window must receive the startup diagnostics permission"
    );
    let app = std::fs::read_to_string(manifest_dir.join("src/lib.rs"))
        .expect("read Tauri command registration");
    assert!(
        app.contains("sidecar::commands::ipc_export_startup_diagnostics"),
        "Tauri app must register the startup diagnostics IPC command"
    );
}

#[cfg(unix)]
#[test]
fn kill_child_reaps_the_spawned_process() {
    let child = Command::new("sleep")
        .arg("30")
        .spawn()
        .expect("spawn test child");
    let pid = child.id() as i32;
    let mut slot = Some(child);

    let started = Instant::now();
    kill_child(&mut slot);

    assert!(slot.is_none(), "kill_child must consume the handle");
    assert!(
        started.elapsed() < GRACEFUL_STOP_TIMEOUT,
        "an unresponsive child must not wait past the shutdown budget"
    );
    assert!(
        !is_pid_alive(pid),
        "child must be gone and reaped, not left as a zombie or an orphan"
    );
}

#[cfg(unix)]
#[test]
fn graceful_stop_writes_the_control_frame() {
    let mut child = Command::new("sh")
        .args(["-c", "read line; test \"$line\" = shutdown"])
        .stdin(Stdio::piped())
        .spawn()
        .expect("spawn control fixture");

    assert!(request_graceful_stop(&mut child));
    assert!(child.wait().expect("wait for control fixture").success());
}
