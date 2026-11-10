use super::*;
use crate::test_support::TempDir;
use std::fs;
use std::path::Path;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

#[test]
fn token_print_command_stays_a_narrow_profile_bound_engine_command() {
    let dir = TempDir::new("token-print-cmd");
    let cmd = token_print_command(Path::new("/engine/pw"), dir.path());

    assert_eq!(cmd.get_program(), Path::new("/engine/pw").as_os_str());
    let args: Vec<String> = cmd
        .get_args()
        .map(|arg| arg.to_string_lossy().into_owned())
        .collect();
    assert_eq!(args, ["token", "print"]);
    let envs: Vec<(String, Option<String>)> = cmd
        .get_envs()
        .map(|(k, v)| {
            (
                k.to_string_lossy().into_owned(),
                v.map(|v| v.to_string_lossy().into_owned()),
            )
        })
        .collect();
    assert!(envs.contains(&(
        "LYCAON_CONFIG_DIR".to_string(),
        Some(dir.path().to_string_lossy().into_owned())
    )));
    assert!(envs.contains(&("LYCAON_API_TOKEN".to_string(), None)));
}

#[test]
fn external_daemon_token_prefers_env_override_without_spawning() {
    let got = resolve_external_daemon_token_with(Some("  env-bearer-token  "), || {
        panic!("token print fallback must not run when LYCAON_API_TOKEN is set")
    })
    .expect("resolve env token");
    assert_eq!(got, "env-bearer-token");
}

#[cfg(unix)]
#[test]
fn external_daemon_token_print_handles_success_failure_timeout_and_never_leaks_stderr_secrets() {
    let dir = TempDir::new("token-print-exec");
    fs::write(
        dir.path().join("api.token"),
        "legacy-plaintext-must-be-ignored\n",
    )
    .expect("seed legacy api.token");

    let mut ok_cmd = Command::new("sh");
    ok_cmd.args(["-c", "printf 'vault-token-abc_123\\n'"]);
    let token = resolve_external_daemon_token_with(None, || {
        run_token_print_command(ok_cmd, Duration::from_secs(2))
    })
    .expect("read token from command");
    assert_eq!(token, "vault-token-abc_123");

    let stderr_canary = "SECRET-STDERR-CANARY-987654";
    let stdout_canary = "SECRET-STDOUT-CANARY-123456";
    let mut fail_cmd = Command::new("sh");
    fail_cmd.args([
        "-c",
        &format!("printf '{stdout_canary}\\n'; printf 'fatal: {stderr_canary}\\n' >&2; exit 7"),
    ]);
    let err = run_token_print_command(fail_cmd, Duration::from_secs(2))
        .expect_err("non-zero exit must fail");
    assert!(
        err.contains("exit 7") && !err.contains(stderr_canary) && !err.contains(stdout_canary),
        "unexpected error message: {err}"
    );

    let mut empty_cmd = Command::new("sh");
    empty_cmd.args(["-c", "printf '   \\n'"]);
    assert!(run_token_print_command(empty_cmd, Duration::from_secs(2)).is_err());

    let mut slow_cmd = Command::new("sh");
    slow_cmd.args(["-c", "exec sleep 10"]);
    let timeout_err = run_token_print_command(slow_cmd, Duration::from_millis(50))
        .expect_err("hanging command must time out");
    assert!(
        timeout_err.contains("timed out"),
        "unexpected timeout error: {timeout_err}"
    );
}

#[cfg(unix)]
fn wait_until_pid_exits(pid: i32, timeout: Duration) -> bool {
    let deadline = Instant::now() + timeout;
    while Instant::now() < deadline {
        if !is_pid_alive(pid) {
            return true;
        }
        std::thread::sleep(Duration::from_millis(10));
    }
    !is_pid_alive(pid)
}

#[cfg(unix)]
#[test]
fn external_daemon_token_print_pipe_inheriting_descendant_does_not_hang_and_preserves_bystanders() {
    let dir = TempDir::new("token-print-pipe-inherit");
    let mut bystander = Command::new("sleep")
        .arg("30")
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("spawn bystander");
    let bystander_pid = bystander.id() as i32;

    // Case 1: Helper spawns a background descendant inheriting stdout/stderr, prints token, and exits.
    let exit_bg_pid_file = dir.path().join("exit-bg.pid");
    let mut exit_cmd = Command::new("sh");
    exit_cmd.args([
        "-c",
        &format!(
            "sleep 30 & echo $! > '{}'; printf 'token-from-parent-with-bg-child\\n'",
            exit_bg_pid_file.display()
        ),
    ]);
    let started = Instant::now();
    let token = run_token_print_command(exit_cmd, Duration::from_secs(2))
        .expect("helper exit with inherited pipe must succeed without blocking");
    assert_eq!(token, "token-from-parent-with-bg-child");
    assert!(
        started.elapsed() < Duration::from_secs(1),
        "helper exit blocked on inherited pipe: {:?}",
        started.elapsed()
    );
    let exit_bg_pid: i32 = fs::read_to_string(&exit_bg_pid_file)
        .expect("read exit bg pid")
        .trim()
        .parse()
        .expect("parse exit bg pid");
    assert!(
        wait_until_pid_exits(exit_bg_pid, Duration::from_secs(1)),
        "pipe-inheriting background child {exit_bg_pid} was not cleaned up on exit"
    );
    assert!(
        is_pid_alive(bystander_pid),
        "bystander process {bystander_pid} must remain alive"
    );

    // Case 2: Helper spawns a background descendant inheriting stdout/stderr and hangs past timeout.
    let timeout_bg_pid_file = dir.path().join("timeout-bg.pid");
    let mut timeout_cmd = Command::new("sh");
    timeout_cmd.args([
        "-c",
        &format!(
            "sleep 30 & echo $! > '{}'; sleep 30",
            timeout_bg_pid_file.display()
        ),
    ]);
    let started_timeout = Instant::now();
    let err = run_token_print_command(timeout_cmd, Duration::from_millis(120))
        .expect_err("hanging helper with inherited pipe must time out");
    assert!(err.contains("timed out"), "unexpected error: {err}");
    assert!(
        started_timeout.elapsed() < Duration::from_secs(1),
        "helper timeout blocked on inherited pipe: {:?}",
        started_timeout.elapsed()
    );
    let timeout_bg_pid: i32 = fs::read_to_string(&timeout_bg_pid_file)
        .expect("read timeout bg pid")
        .trim()
        .parse()
        .expect("parse timeout bg pid");
    assert!(
        wait_until_pid_exits(timeout_bg_pid, Duration::from_secs(1)),
        "pipe-inheriting background child {timeout_bg_pid} was not cleaned up on timeout"
    );
    assert!(
        is_pid_alive(bystander_pid),
        "bystander process {bystander_pid} must remain alive after helper timeout"
    );

    let _ = bystander.kill();
    let _ = bystander.wait();
}
