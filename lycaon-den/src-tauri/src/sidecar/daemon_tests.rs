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

    let mut fallback_ok = Command::new("sh");
    fallback_ok.args(["-c", "printf 'fallback-token-xyz\\n'"]);
    assert_eq!(
        run_token_print_command_bounded_fallback(fallback_ok, Duration::from_secs(2))
            .expect("non-unix bounded fallback success"),
        "fallback-token-xyz"
    );
    let mut fallback_slow = Command::new("sh");
    fallback_slow.args(["-c", "exec sleep 10"]);
    assert!(
        run_token_print_command_bounded_fallback(fallback_slow, Duration::from_millis(50))
            .expect_err("non-unix bounded fallback timeout")
            .contains("timed out")
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
            "sleep 30 & echo $! > '{}'; while [ ! -s '{}' ]; do :; done; sleep 30",
            setup_pid_file.display(),
            setup_pid_file.display()
        ),
    ]);
    let setup_err = run_token_print_command_with(
        setup_cmd,
        Duration::from_secs(2),
        |_stdout| {
            let deadline = Instant::now() + Duration::from_secs(2);
            while Instant::now() < deadline {
                if fs::read_to_string(&setup_pid_file)
                    .map(|s| !s.trim().is_empty())
                    .unwrap_or(false)
                {
                    break;
                }
                std::thread::sleep(Duration::from_millis(5));
            }
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
    assert!(
        wait_until_pid_exits(setup_bg_pid, Duration::from_secs(1)),
        "pipe-inheriting grandchild {setup_bg_pid} survived nonblocking setup error"
    );

    // 2. Real stdout read failure (EBADF via dup2) cleans up helper + grandchild.
    let read_pid_file = dir.path().join("read-fail-bg.pid");
    let mut read_cmd = Command::new("sh");
    read_cmd.args([
        "-c",
        &format!(
            "sleep 30 & echo $! > '{}'; sleep 30",
            read_pid_file.display()
        ),
    ]);
    let read_err = run_token_print_command_with(
        read_cmd,
        Duration::from_secs(2),
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
    assert!(
        wait_until_pid_exits(read_bg_pid, Duration::from_secs(1)),
        "pipe-inheriting grandchild {read_bg_pid} survived stdout read error"
    );
}

#[cfg(unix)]
fn set_fd_nonblocking_and_wait_for_pid(
    pid_file: &Path,
) -> impl FnOnce(&std::process::ChildStdout) -> Result<(), String> + '_ {
    use std::os::fd::AsRawFd;
    move |stdout| {
        set_fd_nonblocking(stdout.as_raw_fd())?;
        let deadline = Instant::now() + Duration::from_secs(2);
        while Instant::now() < deadline {
            if fs::read_to_string(pid_file)
                .map(|s| !s.trim().is_empty())
                .unwrap_or(false)
            {
                break;
            }
            std::thread::sleep(Duration::from_millis(5));
        }
        Ok(())
    }
}
