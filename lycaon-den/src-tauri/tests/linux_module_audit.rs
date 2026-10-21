//! The protected loader boundary admits exactly the native modules its root policy
//! selects. Each case runs a real program under the dynamic loader inside a user
//! namespace whose filesystem root the test owns, so its files appear root-owned.
#![cfg(target_os = "linux")]

use std::ffi::CString;
use std::fs;
use std::os::unix::fs::{symlink, PermissionsExt};
use std::os::unix::process::CommandExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Output};

const NONCE: [u8; 32] = *b"0123456789abcdef0123456789abcdef";

fn sources() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../packaging/linux/module-audit")
}

fn run(program: &str, args: &[&str]) -> String {
    let output = Command::new(program).args(args).output().unwrap_or_else(|error| panic!("{program}: {error}"));
    assert!(output.status.success(), "{program} {args:?}: {}", String::from_utf8_lossy(&output.stderr));
    String::from_utf8(output.stdout).expect("utf-8 tool output")
}

fn freestanding(source: &Path, output: &Path, extra: &[&str]) {
    let mut args = vec!["-shared", "-fPIC", "-nostdlib", "-ffreestanding", "-fno-builtin",
        "-fno-stack-protector", "-O2", "-Wall", "-Wextra", "-Werror"];
    args.extend_from_slice(extra);
    args.extend_from_slice(&[source.to_str().unwrap(), "-o", output.to_str().unwrap()]);
    run("cc", &args);
}

/// The guard's witness address, as the final ELF's dynamic symbol records it.
fn witness_offset(guard: &Path) -> u64 {
    let symbols = run("nm", &["--dynamic", "--defined-only", guard.to_str().unwrap()]);
    let line = symbols.lines().find(|line| line.ends_with(" pw_module_guard_witness_v2")).expect("guard witness symbol");
    u64::from_str_radix(line.split_whitespace().next().unwrap(), 16).unwrap()
}

/// Policy v2: magic, size, root count, nonce, guard offset, guard path, then roots.
fn policy(roots: &[&str], guard: &str, offset: u64) -> Vec<u8> {
    let mut body = guard.as_bytes().to_vec();
    for root in roots {
        body.extend_from_slice(&(root.len() as u16).to_le_bytes());
        body.extend_from_slice(root.as_bytes());
    }
    let mut raw = b"PWMOD02\0".to_vec();
    raw.extend_from_slice(&((64 + body.len()) as u32).to_le_bytes());
    raw.extend_from_slice(&(roots.len() as u32).to_le_bytes());
    raw.extend_from_slice(&NONCE);
    raw.extend_from_slice(&offset.to_le_bytes());
    raw.extend_from_slice(&(guard.len() as u16).to_le_bytes());
    raw.extend_from_slice(&[0; 6]);
    raw.extend_from_slice(&body);
    raw
}

/// The file the dynamic loader resolves for `name` when it loads `program`.
fn resolved(program: &Path, name: &str) -> PathBuf {
    let listing = run("ldd", &[program.to_str().unwrap()]);
    let path = listing
        .lines()
        .find_map(|line| {
            let line = line.trim();
            if line.starts_with(name) {
                line.split("=> ").nth(1).and_then(|rest| rest.split(" (").next()).map(str::to_owned)
            } else if line.starts_with('/') && Path::new(line.split(' ').next().unwrap()).file_name()?.to_str()? == name {
                line.split(' ').next().map(str::to_owned)
            } else {
                None
            }
        })
        .unwrap_or_else(|| panic!("{name} in {listing}"));
    fs::canonicalize(path).unwrap()
}

fn interpreter(program: &Path) -> String {
    let headers = run("readelf", &["--program-headers", program.to_str().unwrap()]);
    let start = headers.find("[Requesting program interpreter: ").expect("program interpreter") + 33;
    headers[start..].split(']').next().unwrap().to_owned()
}

struct Fixture {
    _directory: tempdir::Directory,
    tree: PathBuf,
    offset: u64,
}

mod tempdir {
    use std::path::{Path, PathBuf};

    pub struct Directory(PathBuf);

    impl Directory {
        pub fn new() -> Self {
            let base = std::env::temp_dir().join(format!("paintedwolf-module-audit-{}", std::process::id()));
            let path = base.join(format!("{:x}", std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_nanos()));
            std::fs::create_dir_all(&path).unwrap();
            Self(path)
        }

        pub fn path(&self) -> &Path {
            &self.0
        }
    }

    impl Drop for Directory {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.0);
        }
    }
}

impl Fixture {
    /// A root tree holding the loader, libc, the auditor, the guard and a probe whose
    /// constructor and main each write one byte.
    fn new() -> Self {
        let directory = tempdir::Directory::new();
        let build = directory.path().join("build");
        let tree = directory.path().join("tree");
        for path in [&build, &tree] {
            fs::create_dir(path).unwrap();
            fs::set_permissions(path, fs::Permissions::from_mode(0o755)).unwrap();
        }
        let source = sources();
        let auditor = build.join("pw-module-audit.so");
        freestanding(&source.join("audit.c"), &auditor, &[]);
        let guard = build.join("pw-module-guard.so");
        freestanding(&source.join("guard.c"), &guard,
            &["-Wl,-z,initfirst,-z,relro,-z,now,-Bsymbolic,-soname,pw-module-guard.so"]);
        let constructor = build.join("ctor.c");
        fs::write(&constructor, "#include <unistd.h>\nstatic void __attribute__((constructor)) c(void){write(1,\"C\",1);}\n").unwrap();
        run("cc", &["-shared", "-fPIC", "-Wl,-soname,module-ctor.so", constructor.to_str().unwrap(),
            "-o", build.join("module-ctor.so").to_str().unwrap()]);
        let probe = build.join("probe.c");
        fs::write(&probe, "#include <dlfcn.h>\n#include <unistd.h>\nint main(int n,char**v){if(n==2&&!dlopen(v[1],RTLD_NOW))return 42;write(1,\"M\",1);return 0;}\n").unwrap();
        run("cc", &[probe.to_str().unwrap(), "-Wl,--no-as-needed", guard.to_str().unwrap(),
            build.join("module-ctor.so").to_str().unwrap(), "-Wl,-rpath,/usr/lib",
            "-o", build.join("probe").to_str().unwrap()]);
        let launcher = build.join("launcher.c");
        fs::write(&launcher, LAUNCHER).unwrap();
        run("cc", &["-static", "-O2", launcher.to_str().unwrap(), "-o", build.join("launcher").to_str().unwrap()]);

        let program = build.join("probe");
        let loader = interpreter(&program);
        let libc = resolved(&program, "libc.so.6");
        let place = |from: &Path, to: &str| {
            let target = tree.join(to.trim_start_matches('/'));
            fs::create_dir_all(target.parent().unwrap()).unwrap();
            fs::copy(from, &target).unwrap();
            // Independent of the host umask: selected modules are never group or world writable.
            fs::set_permissions(&target, fs::Permissions::from_mode(0o755)).unwrap();
        };
        place(&fs::canonicalize(&loader).unwrap(), &loader);
        place(&libc, "/usr/lib/libc.so.6");
        place(&guard, "/usr/lib/pw-module-guard.so");
        place(&build.join("module-ctor.so"), "/usr/lib/module-ctor.so");
        place(&build.join("module-ctor.so"), "/foreign/module-ctor.so");
        place(&auditor, "/audit/pw-module-audit.so");
        place(&program, "/bin/probe");
        place(&build.join("launcher"), "/bin/launcher");
        fs::create_dir(tree.join("proc")).unwrap();
        for directory in walk(&tree) {
            fs::set_permissions(&directory, fs::Permissions::from_mode(0o755)).unwrap();
        }
        let offset = witness_offset(&guard);
        Fixture { _directory: directory, tree, offset }
    }

    /// The directory holding the program interpreter, part of the selected system.
    fn loader_root(&self) -> String {
        let loader = interpreter(&self.tree.join("bin/probe"));
        Path::new(&loader).parent().unwrap().to_str().unwrap().to_owned()
    }

    fn write_policy(&self, roots: &[&str]) {
        let raw = policy(roots, "/usr/lib/pw-module-guard.so", self.offset);
        fs::write(self.tree.join("policy"), raw).unwrap();
    }

    /// Runs the launcher as namespace root inside the tree, with the host's procfs
    /// bound in so descriptors resolve to their physical paths.
    fn launch(&self, args: &[&str]) -> Output {
        let tree = CString::new(self.tree.to_str().unwrap()).unwrap();
        let proc = CString::new(self.tree.join("proc").to_str().unwrap()).unwrap();
        let (uid, gid) = unsafe { (libc::getuid(), libc::getgid()) };
        let maps = [
            (CString::new("/proc/self/setgroups").unwrap(), b"deny".to_vec()),
            (CString::new("/proc/self/uid_map").unwrap(), format!("0 {uid} 1").into_bytes()),
            (CString::new("/proc/self/gid_map").unwrap(), format!("0 {gid} 1").into_bytes()),
        ];
        let mut command = Command::new("/bin/launcher");
        command.args(args).env_clear();
        unsafe {
            command.pre_exec(move || {
                let fail = || Err(std::io::Error::last_os_error());
                if libc::unshare(libc::CLONE_NEWUSER | libc::CLONE_NEWNS) != 0 {
                    return fail();
                }
                for (path, value) in &maps {
                    let fd = libc::open(path.as_ptr(), libc::O_WRONLY | libc::O_CLOEXEC);
                    if fd < 0 || libc::write(fd, value.as_ptr().cast(), value.len()) != value.len() as isize {
                        return fail();
                    }
                    libc::close(fd);
                }
                if libc::mount(std::ptr::null(), c"/".as_ptr(), std::ptr::null(), libc::MS_REC | libc::MS_PRIVATE, std::ptr::null()) != 0
                    || libc::mount(c"/proc".as_ptr(), proc.as_ptr(), std::ptr::null(), libc::MS_BIND | libc::MS_REC, std::ptr::null()) != 0
                    || libc::chroot(tree.as_ptr()) != 0
                    || libc::chdir(c"/".as_ptr()) != 0
                {
                    return fail();
                }
                Ok(())
            });
        }
        command.output().expect("launch the namespace fixture")
    }

    fn selected(&self, roots: &[&str]) {
        let loader = self.loader_root();
        let mut selected = vec![loader.clone()];
        selected.extend(roots.iter().map(|root| root.to_string()).filter(|root| *root != loader));
        self.write_policy(&selected.iter().map(String::as_str).collect::<Vec<_>>());
    }
}

fn walk(root: &Path) -> Vec<PathBuf> {
    let mut found = vec![root.to_path_buf()];
    for entry in fs::read_dir(root).unwrap() {
        let path = entry.unwrap().path();
        if path.is_dir() {
            found.extend(walk(&path));
        }
    }
    found
}

/// Creates the root policy descriptor and audit handshake the protected launch
/// provides, then executes the probe through the auditor.
const LAUNCHER: &str = r#"
#define _GNU_SOURCE
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>
int main(int argc, char **argv) {
  /* usage: launcher sealed|unsealed audit|none <program> [args...] */
  if (argc < 4) return 2;
  int policy = memfd_create("policy", MFD_ALLOW_SEALING | MFD_CLOEXEC);
  int source = open("/policy", O_RDONLY);
  char bytes[65536]; ssize_t size = read(source, bytes, sizeof bytes);
  if (policy < 0 || source < 0 || size <= 0 || write(policy, bytes, size) != size) return 3;
  if (fchmod(policy, 0444) != 0) return 4;
  if (!strcmp(argv[1], "sealed") && fcntl(policy, F_ADD_SEALS, F_SEAL_SEAL | F_SEAL_SHRINK | F_SEAL_GROW | F_SEAL_WRITE) != 0) return 5;
  int pipes[2];
  if (pipe2(pipes, O_NONBLOCK) != 0 || dup2(policy, 7) != 7 || dup2(pipes[0], 9) != 9 || dup2(pipes[1], 10) != 10) return 6;
  char *environment[] = { "LD_AUDIT=/audit/pw-module-audit.so", NULL };
  char *none[] = { NULL };
  execve(argv[3], argv + 3, strcmp(argv[2], "audit") ? none : environment);
  return 7;
}
"#;

fn outcome(output: &Output) -> (Option<i32>, String) {
    (output.status.code(), String::from_utf8_lossy(&output.stdout).into_owned())
}

#[test]
fn selected_system_modules_load_and_initialize_under_the_auditor() {
    let fixture = Fixture::new();
    fixture.selected(&["/usr/lib"]);
    let output = fixture.launch(&["sealed", "audit", "/bin/probe"]);
    assert_eq!(outcome(&output), (Some(0), "CM".to_owned()), "{}", String::from_utf8_lossy(&output.stderr));
    let output = fixture.launch(&["sealed", "audit", "/bin/probe", "/usr/lib/module-ctor.so"]);
    assert_eq!(outcome(&output), (Some(0), "CM".to_owned()), "a selected late module was refused");
}

#[test]
fn modules_outside_the_selected_roots_refuse_before_their_code_runs() {
    let fixture = Fixture::new();
    fixture.selected(&["/usr/lib"]);
    let output = fixture.launch(&["sealed", "audit", "/bin/probe", "/foreign/module-ctor.so"]);
    assert_eq!(outcome(&output), (Some(125), "C".to_owned()), "a late foreign module ran");
    // Without /usr/lib the C library and the guard are foreign at startup.
    fixture.selected(&[]);
    let output = fixture.launch(&["sealed", "audit", "/bin/probe"]);
    assert_eq!(outcome(&output), (Some(125), String::new()), "an unselected startup module ran");
}

#[test]
fn writable_directories_and_links_refuse_even_inside_a_selected_root() {
    let fixture = Fixture::new();
    fixture.selected(&["/usr/lib"]);
    let writable = fixture.tree.join("usr/lib/writable");
    fs::create_dir(&writable).unwrap();
    fs::copy(fixture.tree.join("usr/lib/module-ctor.so"), writable.join("module-ctor.so")).unwrap();
    fs::set_permissions(&writable, fs::Permissions::from_mode(0o777)).unwrap();
    let output = fixture.launch(&["sealed", "audit", "/bin/probe", "/usr/lib/writable/module-ctor.so"]);
    assert_eq!(outcome(&output), (Some(125), "C".to_owned()), "a module in a writable directory loaded");
    let shared = fixture.tree.join("usr/lib/group-writable.so");
    fs::copy(fixture.tree.join("usr/lib/module-ctor.so"), &shared).unwrap();
    fs::set_permissions(&shared, fs::Permissions::from_mode(0o775)).unwrap();
    let output = fixture.launch(&["sealed", "audit", "/bin/probe", "/usr/lib/group-writable.so"]);
    assert_eq!(outcome(&output), (Some(125), "C".to_owned()), "a group-writable module loaded");
    symlink("/foreign/module-ctor.so", fixture.tree.join("usr/lib/linked.so")).unwrap();
    let output = fixture.launch(&["sealed", "audit", "/bin/probe", "/usr/lib/linked.so"]);
    assert_eq!(outcome(&output), (Some(125), "C".to_owned()), "a link into an unselected root loaded");
}

#[test]
fn the_guard_refuses_without_the_auditor_or_its_sealed_policy() {
    let fixture = Fixture::new();
    fixture.selected(&["/usr/lib"]);
    for (mode, audit) in [("sealed", "none"), ("unsealed", "audit")] {
        let output = fixture.launch(&[mode, audit, "/bin/probe"]);
        assert_eq!(outcome(&output), (Some(125), String::new()), "{mode} {audit} reached a constructor");
    }
}
