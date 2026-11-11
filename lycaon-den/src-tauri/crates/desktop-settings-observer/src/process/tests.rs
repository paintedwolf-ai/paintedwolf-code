use super::*;
use std::os::fd::FromRawFd;
#[test]
fn fixed_child_observes_only_safe_environment_and_no_inherited_fds() {
    if std::env::var_os("PW_OBSERVER_TEST_CHILD").is_none() {
        return;
    }
    if std::env::var("PW_OBSERVER_TEST_CHILD").unwrap() == "-1" {
        assert_eq!(unsafe { libc::prctl(libc::PR_SET_DUMPABLE, 0) }, 0);
        let mut observer = command(&std::env::current_exe().unwrap(), &environment())
            .args(["--exact", "process::tests::fixed_child_observes_only_safe_environment_and_no_inherited_fds"])
            .env("PW_OBSERVER_TEST_CHILD", "200")
            .spawn().unwrap();
        assert!(observer.wait().unwrap().success());
        assert_eq!(unsafe { libc::prctl(libc::PR_GET_DUMPABLE) }, 0);
        return;
    }
    assert_eq!(unsafe { libc::prctl(libc::PR_GET_DUMPABLE) }, 1);
    assert_eq!(
        unsafe { libc::prctl(libc::PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0) },
        1
    );
    for (name, _) in std::env::vars_os() {
        assert!(ENVIRONMENT.iter().any(|key| name == *key) || name == "PW_OBSERVER_TEST_CHILD");
    }
    let descriptor: i32 = std::env::var("PW_OBSERVER_TEST_CHILD")
        .unwrap()
        .parse()
        .unwrap();
    assert_eq!(unsafe { libc::fcntl(descriptor, libc::F_GETFD) }, -1);
}
#[test]
fn actual_exec_clears_environment_descriptors_and_reaps_owned_child() {
    use std::os::fd::AsRawFd;
    let secret = std::fs::File::open("/dev/null").unwrap();
    let descriptor = unsafe { libc::fcntl(secret.as_raw_fd(), libc::F_DUPFD, 200) };
    assert!(descriptor >= 200);
    let mut child = command(&std::env::current_exe().unwrap(), &environment())
        .args([
            "--exact",
            "process::tests::fixed_child_observes_only_safe_environment_and_no_inherited_fds",
            "--nocapture",
        ])
        .env("PW_OBSERVER_TEST_CHILD", descriptor.to_string())
        .spawn()
        .unwrap();
    unsafe {
        libc::close(descriptor);
    }
    let input = File::from(OwnedFd::from(child.stdin.take().unwrap()));
    let result = child.wait().unwrap();
    assert!(result.success());
    stop(input, &mut child);
    assert!(child.try_wait().unwrap().is_some());
}
#[test]
fn separate_exec_observes_without_relaxing_the_nondumpable_parent() {
    let mut hardened = command(&std::env::current_exe().unwrap(), &environment())
        .args([
            "--exact",
            "process::tests::fixed_child_observes_only_safe_environment_and_no_inherited_fds",
        ])
        .env("PW_OBSERVER_TEST_CHILD", "-1")
        .spawn()
        .unwrap();
    assert!(hardened.wait().unwrap().success());
}
#[test]
fn a_started_observer_hardens_itself_whoever_launched_it() {
    const MARKER: &str = "PW_OBSERVER_HARDEN_CHILD";
    if let Some(descriptor) = std::env::var_os(MARKER) {
        let descriptor: i32 = descriptor.to_str().unwrap().parse().unwrap();
        assert!(unsafe { libc::fcntl(descriptor, libc::F_GETFD) } >= 0, "the launcher's descriptor arrived");
        crate::harden().unwrap();
        assert_eq!(unsafe { libc::fcntl(descriptor, libc::F_GETFD) }, -1);
        assert_eq!(unsafe { libc::prctl(libc::PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0) }, 1);
        println!("\nhardened");
        return;
    }
    // A launcher that, like the broker, sets no no-new-privs and leaves a descriptor open.
    let image = std::fs::File::open("/dev/null").unwrap();
    let descriptor = unsafe { libc::fcntl(image.as_raw_fd(), libc::F_DUPFD, 200) };
    assert!(descriptor >= 200);
    let output = Command::new(std::env::current_exe().unwrap())
        .args(["--exact", "process::tests::a_started_observer_hardens_itself_whoever_launched_it", "--nocapture"])
        .env(MARKER, descriptor.to_string())
        .output()
        .unwrap();
    unsafe { libc::close(descriptor) };
    let stdout = String::from_utf8_lossy(&output.stdout);
    assert!(output.status.success() && stdout.lines().any(|line| line == "hardened"),
        "{}\n{stdout}\n{}", output.status, String::from_utf8_lossy(&output.stderr));
}

struct Scripted {
    frames: &'static [u8],
    started: std::sync::Mutex<u32>,
}

struct Ended(Arc<AtomicBool>);

impl Process for Ended {
    fn exited(&mut self) -> io::Result<bool> {
        Ok(self.0.load(Ordering::Acquire))
    }
    fn end(&mut self) {
        self.0.store(true, Ordering::Release);
    }
}

impl Launch for Scripted {
    fn start(&self, _: &File, environment: &[(&'static str, OsString)]) -> io::Result<Started> {
        assert!(environment.iter().all(|(key, _)| ENVIRONMENT.contains(key)));
        *self.started.lock().unwrap() += 1;
        let (output, mut writer) = pipe();
        let (_, input) = pipe();
        use std::io::Write;
        writer.write_all(self.frames).unwrap();
        Ok(Started { input, output, process: Box::new(Ended(Arc::new(AtomicBool::new(false)))) })
    }
}

fn pipe() -> (File, File) {
    let mut fds = [0; 2];
    assert_eq!(unsafe { libc::pipe2(fds.as_mut_ptr(), libc::O_CLOEXEC) }, 0);
    unsafe { (File::from_raw_fd(fds[0]), File::from_raw_fd(fds[1])) }
}

#[test]
fn stop_ends_a_launched_observer_that_does_not_exit_when_asked() {
    let launcher = Scripted { frames: b"", started: std::sync::Mutex::new(0) };
    let image = File::open("/dev/null").unwrap();
    let Started { input, output, mut process } = launcher.start(&image, &environment()).unwrap();
    drop(output);
    stop(input, process.as_mut());
    assert!(process.exited().unwrap());
    assert_eq!(*launcher.started.lock().unwrap(), 1);
}

#[test]
fn environment_never_inherits_parent_credentials() {
    assert!(!ENVIRONMENT.iter().any(|name| name.contains("TOKEN")
        || name.contains("KEY")
        || name.contains("PASSWORD")));
    assert_eq!(ENVIRONMENT.len(), 6);
}
#[test]
fn foreign_or_missing_image_cannot_select_a_command() {
    assert!(image::Image::open(Path::new("/nonexistent/bin/painted-wolf-code")).is_err());
    assert!(image::Image::open(Path::new("/bin/sh")).is_err());
}
