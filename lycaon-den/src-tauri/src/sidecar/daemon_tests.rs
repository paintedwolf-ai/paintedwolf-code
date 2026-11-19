use super::*;
use crate::test_support::{TempDir, NO_DEADLINE};
use std::fs;
use std::path::Path;
use std::process::{Command, Stdio};
use std::time::Duration;

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
        run_token_print_command(ok_cmd, NO_DEADLINE)
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
    let err = run_token_print_command(fail_cmd, NO_DEADLINE)
        .expect_err("non-zero exit must fail");
    assert!(
        err.contains("exit 7") && !err.contains(stderr_canary) && !err.contains(stdout_canary),
        "unexpected error message: {err}"
    );

    let mut empty_cmd = Command::new("sh");
    empty_cmd.args(["-c", "printf '   \\n'"]);
    assert!(run_token_print_command(empty_cmd, NO_DEADLINE).is_err());

    let mut slow_cmd = Command::new("sh");
    slow_cmd.args(["-c", "exec sleep 1000000"]);
    let timeout_err = run_token_print_command(slow_cmd, Duration::from_millis(50))
        .expect_err("hanging command must time out");
    assert!(
        timeout_err.contains("timed out"),
        "unexpected timeout error: {timeout_err}"
    );

    let mut fallback_ok = Command::new("sh");
    fallback_ok.args(["-c", "printf 'fallback-token-xyz\\n'"]);
    assert_eq!(
        run_token_print_command_bounded_fallback(fallback_ok, NO_DEADLINE)
            .expect("non-unix bounded fallback success"),
        "fallback-token-xyz"
    );
    let mut fallback_slow = Command::new("sh");
    fallback_slow.args(["-c", "exec sleep 1000000"]);
    assert!(
        run_token_print_command_bounded_fallback(fallback_slow, Duration::from_millis(50))
            .expect_err("non-unix bounded fallback timeout")
            .contains("timed out")
    );
}

#[cfg(unix)]
/// Returns once the process is gone; a process that never exits holds the test until the run's backstop names it.
fn wait_for_pid_exit(pid: i32) {
    while is_pid_alive(pid) {
        std::thread::sleep(Duration::from_millis(10));
    }
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

    // Descendants never exit on their own: only the helper's cleanup ends them.
    // Case 1: Helper spawns a background descendant inheriting stdout/stderr, prints token, and exits.
    let exit_bg_pid_file = dir.path().join("exit-bg.pid");
    let mut exit_cmd = Command::new("sh");
    exit_cmd.args([
        "-c",
        &format!(
            "sleep 1000000 & echo $! > '{}'; printf 'token-from-parent-with-bg-child\\n'",
            exit_bg_pid_file.display()
        ),
    ]);
    let token = run_token_print_command(exit_cmd, NO_DEADLINE)
        .expect("helper exit with inherited pipe must succeed without blocking");
    assert_eq!(token, "token-from-parent-with-bg-child");
    let exit_bg_pid: i32 = fs::read_to_string(&exit_bg_pid_file)
        .expect("read exit bg pid")
        .trim()
        .parse()
        .expect("parse exit bg pid");
    wait_for_pid_exit(exit_bg_pid);
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
            "sleep 1000000 & echo $! > '{}'; sleep 1000000",
            timeout_bg_pid_file.display()
        ),
    ]);
    let err = run_token_print_command(timeout_cmd, Duration::from_millis(120))
        .expect_err("hanging helper with inherited pipe must time out");
    assert!(err.contains("timed out"), "unexpected error: {err}");
    let timeout_bg_pid: i32 = fs::read_to_string(&timeout_bg_pid_file)
        .expect("read timeout bg pid")
        .trim()
        .parse()
        .expect("parse timeout bg pid");
    wait_for_pid_exit(timeout_bg_pid);
    assert!(
        is_pid_alive(bystander_pid),
        "bystander process {bystander_pid} must remain alive after helper timeout"
    );

    let _ = bystander.kill();
    let _ = bystander.wait();
}

#[cfg(unix)]
#[test]
fn external_daemon_token_print_nonblocking_and_read_errors_clean_up_process_group() {
    use std::os::fd::AsRawFd;

    let bad_fd_err = set_fd_nonblocking(-1).expect_err("invalid fd must fail fcntl");
    assert!(
        bad_fd_err.contains("fcntl F_GETFL"),
        "unexpected error for invalid fd: {bad_fd_err}"
    );

    let dir = TempDir::new("token-print-err-cleanup");

    // 1. Nonblocking setup failure after spawning helper + pipe-inheriting grandchild.
    let setup_pid_file = dir.path().join("setup-fail-bg.pid");
    let mut setup_cmd = Command::new("sh");
    setup_cmd.args([
        "-c",
        &format!(
            "sleep 1000000 & echo $! > '{}'; while [ ! -s '{}' ]; do :; done; sleep 1000000",
            setup_pid_file.display(),
            setup_pid_file.display()
        ),
    ]);
    let setup_err = run_token_print_command_with(
        setup_cmd,
        NO_DEADLINE,
        |_stdout| {
            wait_for_pid_file(&setup_pid_file);
            Err("injected nonblocking setup failure".to_string())
        },
        drain_nonblocking_stdout,
    )
    .expect_err("nonblocking setup error must abort");
    assert!(setup_err.contains("injected nonblocking setup failure"));
    let setup_bg_pid: i32 = fs::read_to_string(&setup_pid_file)
        .expect("read setup bg pid")
        .trim()
        .parse()
        .expect("parse setup bg pid");
    wait_for_pid_exit(setup_bg_pid);

    // 2. Real stdout read failure (EBADF via dup2) cleans up helper + grandchild.
    let read_pid_file = dir.path().join("read-fail-bg.pid");
    let mut read_cmd = Command::new("sh");
    read_cmd.args([
        "-c",
        &format!(
            "sleep 1000000 & echo $! > '{}'; sleep 1000000",
            read_pid_file.display()
        ),
    ]);
    let read_err = run_token_print_command_with(
        read_cmd,
        NO_DEADLINE,
        set_fd_nonblocking_and_wait_for_pid(&read_pid_file),
        |stdout, buf, max_bytes| {
            // Replace stdout's file description with a write-only /dev/null fd so
            // read() returns EBADF without double-closing ChildStdout on Drop.
            let devnull = std::fs::OpenOptions::new()
                .write(true)
                .open("/dev/null")
                .expect("open /dev/null write-only");
            unsafe {
                let _ = libc::dup2(devnull.as_raw_fd(), stdout.as_raw_fd());
            }
            drain_nonblocking_stdout(stdout, buf, max_bytes)
        },
    )
    .expect_err("stdout read failure must abort");
    assert!(
        read_err.contains("read daemon token print stdout"),
        "unexpected read error: {read_err}"
    );
    let read_bg_pid: i32 = fs::read_to_string(&read_pid_file)
        .expect("read bg pid")
        .trim()
        .parse()
        .expect("parse bg pid");
    wait_for_pid_exit(read_bg_pid);
}

#[cfg(unix)]
fn set_fd_nonblocking_and_wait_for_pid(
    pid_file: &Path,
) -> impl FnOnce(&std::process::ChildStdout) -> Result<(), String> + '_ {
    use std::os::fd::AsRawFd;
    move |stdout| {
        set_fd_nonblocking(stdout.as_raw_fd())?;
        wait_for_pid_file(pid_file);
        Ok(())
    }
}

#[cfg(unix)]
fn wait_for_pid_file(pid_file: &Path) {
    while !fs::read_to_string(pid_file).is_ok_and(|s| !s.trim().is_empty()) {
        std::thread::sleep(Duration::from_millis(5));
    }
}
