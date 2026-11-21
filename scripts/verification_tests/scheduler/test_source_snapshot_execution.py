"""Source snapshots: slots, caches, collection locks, leases and fixture write-back."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parents[2]
SNAPSHOT_SCRIPTS = ["test-source-snapshot.sh", "checkout-cache-dir.sh", "sha256.sh", "test-run-isolation.sh",
                    "test-run-lease.py", "digest-run-lock.sh", "artifact-paths.sh", "artifact_paths.py"]


def git(repo, *args):
    return subprocess.run(["git", "-C", str(repo), "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
                           *args], check=True, capture_output=True, text=True).stdout.strip()


@unittest.skipIf(os.name == "nt", "snapshot leases require POSIX file locks")
class SourceSnapshotTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="source-snapshot-")
        self.addCleanup(temporary.cleanup)
        self.work = Path(temporary.name).resolve()
        self.repo = self.work / "repo"
        (self.repo / "scripts" / "verification_lifecycle").mkdir(parents=True)
        for name in SNAPSHOT_SCRIPTS:
            shutil.copy2(SCRIPTS / name, self.repo / "scripts" / name)
        shutil.copy2(SCRIPTS / "verification_lifecycle" / "run_lease.py",
                     self.repo / "scripts" / "verification_lifecycle" / "run_lease.py")
        (self.repo / "value.go").write_text("package cachefixture\n")
        git(self.repo, "init", "-q")
        self.commit_all("fixture")
        self.env = {key: value for key, value in os.environ.items()
                    if not key.startswith(("PW_", "UPDATE_", "OPENGREP_"))}
        self.env.update(PW_TEST_SNAPSHOT_ROOT=str(self.work / "snapshots"),
                        PW_ARTIFACT_ROOT=str(self.work / "artifacts"), PW_TEST_ARTIFACT_ROOT=str(self.work / "artifacts"),
                        PW_BIN_DIR=str(self.work / "tools"), PW_BUILD_DIR=str(self.work / "build"),
                        PW_LOCK_ROOT=str(self.work / "locks"), TMPDIR=str(self.work / "tmp"))
        (self.work / "tmp").mkdir()

    def commit_all(self, message):
        git(self.repo, "add", "-A")
        git(self.repo, "commit", "-qm", message)
        self.commit = git(self.repo, "rev-parse", "HEAD")

    def snapshot(self, *command, env=None, **options):
        argv = ["bash", str(self.repo / "scripts" / "test-source-snapshot.sh"), "run-commit", self.commit, "--", *command]
        return subprocess.run(argv, env={**self.env, **(env or {})}, capture_output=True, text=True, **options)

    def started(self, *command):
        argv = ["bash", str(self.repo / "scripts" / "test-source-snapshot.sh"), "run-commit", self.commit, "--", *command]
        process = subprocess.Popen(argv, env=self.env, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True)
        self.addCleanup(lambda: process.poll() is None and (process.kill(), process.wait()))
        return process

    def fifo(self, name):
        path = self.work / name
        os.mkfifo(path)
        return path

    def test_caches_survive_runs_while_build_outputs_do_not(self):
        parent_cache, compiled = self.work / "parent-cache", self.work / "compiled-cache"
        parent_cache.mkdir()
        (parent_cache / "keep").touch()
        compiled.mkdir()
        (compiled / "object").touch()
        env = {"HOME": str(self.work / "home"), "XDG_CACHE_HOME": str(self.work / "os-cache"),
               "PW_TEST_CHECKOUT_CACHE": str(parent_cache), "GOCACHE": str(compiled)}
        probe = r'''set -euo pipefail
cache="$(bash scripts/checkout-cache-dir.sh)"
[[ "$cache" != "$1/parent-cache" && "$cache" != "$PWD"/* ]]
[[ "$(HOME="$PWD" bash scripts/checkout-cache-dir.sh)" == "$cache" ]]
[[ "$(bash scripts/checkout-cache-dir.sh "$PWD")" == "$cache" ]]
[[ "$GOCACHE" == "$1/compiled-cache" && -f "$GOCACHE/object" ]]
[[ ! -e previous-run ]]
touch previous-run
[[ "$PW_BIN_DIR" == "$1/tools" && "$OPENGREP_STAGE_DIR" == "$1/build/opengrep-bundle" ]]
[[ "$PW_BUILD_DIR" != "$1/build" && "$PW_BUILD_DIR" != "$PWD"/* ]]
mkdir -p "$PW_BUILD_DIR" "$cache/golangci-cache-test"
touch "$PW_BUILD_DIR/lycaon-dev"
[[ ! -e "$cache/golangci-cache-test/diagnostic" ]] || touch "$1/cache-reused"
touch "$cache/golangci-cache-test/diagnostic"
printf "%s" "$cache" > "$1/cache-path"
printf "%s" "$PWD" > "$1/snapshot-path"
printf "%s" "$PW_BUILD_DIR" > "$1/build-path"
exit "$2"
'''
        snapshots = []
        for status in (0, 7):
            with self.subTest(status=status):
                result = self.snapshot("bash", "-c", probe, "cache-test", str(self.work), str(status), env=env)
                self.assertEqual(result.returncode, status, result.stderr)
                cache = Path((self.work / "cache-path").read_text())
                self.assertTrue((cache / "golangci-cache-test" / "diagnostic").is_file())
                self.assertFalse(Path((self.work / "build-path").read_text()).exists())
                self.assertFalse((self.work / "build" / "lycaon-dev").exists())
                self.assertTrue((parent_cache / "keep").is_file())
                self.assertTrue((compiled / "object").is_file())
                snapshots.append((self.work / "snapshot-path").read_text())
        self.assertTrue((self.work / "cache-reused").exists())
        self.assertEqual(snapshots[0], snapshots[1])
        diagnostics = subprocess.run(["bash", str(self.repo / "scripts" / "test-source-snapshot.sh"), "diagnostics-cache"],
                                     env={**self.env, **env}, capture_output=True, text=True, check=True)
        self.assertEqual(diagnostics.stdout.strip(), (self.work / "cache-path").read_text())

    def test_failed_isolation_creation_releases_the_collection_lock(self):
        root = self.work / "cache-root"
        with (self.repo / "scripts" / "test-run-isolation.sh").open("a") as isolation:
            isolation.write(r'''
test_run_create_isolation() {
  TEST_RUN_ROOT="$PW_CACHE_FAILURE_ROOT"
  mkdir -p "$TEST_RUN_ROOT"
  exec 9>"$TEST_RUN_ROOT/.collection.lock"
  python3 "$TEST_RUN_LEASE_HELPER" lock 9
  return 19
}
test_run_prune_abandoned() {
  python3 -c 'import fcntl, sys; fcntl.flock(open(sys.argv[1], "a"), fcntl.LOCK_EX | fcntl.LOCK_NB)' \
    "$TEST_RUN_ROOT/.collection.lock" > "$TEST_RUN_ROOT/probe" 2>&1
}
''')
        self.commit_all("failing isolation")
        result = self.snapshot("true", env={"PW_CACHE_FAILURE_ROOT": str(root)})
        self.assertEqual(result.returncode, 19, result.stderr)
        self.assertEqual((root / "probe").read_text(), "")

    def test_a_live_descendant_keeps_its_lease(self):
        self.fifo("ready"), self.fifo("release")
        script = r'''set -euo pipefail
source "$1/test-run-isolation.sh"
TEST_RUN_ROOT="$2/runs"
test_run_create_isolation snapshot-cache
printf '%s\n' "$TEST_RUN_DIR" > "$2/cache"
bash -c 'printf "ready\\n" > "$1/ready"; read -r line < "$1/release"' child "$2" &
child=$!
read -r line < "$2/ready"
exec 8>&-
test_run_prune_abandoned
[[ -d "$TEST_RUN_DIR" ]]
printf 'release\n' > "$2/release"
wait "$child"
test_run_prune_abandoned
[[ ! -e "$TEST_RUN_DIR" ]]
'''
        result = subprocess.run(["bash", "-c", script, "lease-test", str(SCRIPTS), str(self.work)],
                                env=self.env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(Path((self.work / "cache").read_text().strip()).exists())

    def test_concurrent_runs_take_separate_slots_and_reuse_them(self):
        started, release = self.fifo("started"), self.fifo("release")
        first = self.started("bash", "-c", 'printf "%s" "$PWD" > "$1/started"; read -r line < "$1/release"',
                             "slot-test", str(self.work))
        with started.open() as announcement:
            first_path = announcement.read()
        self.assertTrue((Path(first_path) / "value.go").is_file())
        second = self.snapshot("pwd")
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertNotEqual(second.stdout.strip(), first_path)
        with release.open("w") as releasing:
            releasing.write("release\n")
        _, errors = first.communicate()
        self.assertEqual(first.returncode, 0, errors)
        reused = self.snapshot("pwd")
        self.assertEqual(reused.returncode, 0, reused.stderr)
        self.assertEqual(reused.stdout.strip(), first_path)

    def test_fixture_refresh_copies_back_only_its_own_paths(self):
        golden = Path("lycaon/internal/report/testdata/golden")
        baseline = Path("lycaon/internal/oar/testdata/corpus_baseline.json")
        (self.repo / golden).mkdir(parents=True)
        (self.repo / baseline).parent.mkdir(parents=True)
        for name in "abce":
            (self.repo / golden / f"{name}.pdf").write_text(f"old-{name}")
        (self.repo / baseline).write_text("old-baseline")
        self.commit_all("goldens")

        def read(name):
            path = self.repo / golden / f"{name}.pdf"
            return path.read_text() if path.exists() else None

        # Rewrites a and b, adds d and e, deletes c; a concurrent edit changes b and removes e in the checkout.
        refresh = f'''set -euo pipefail
printf new-a > {golden}/a.pdf
printf new-b > {golden}/b.pdf
printf new-d > {golden}/d.pdf
printf new-e > {golden}/e.pdf
rm {golden}/c.pdf
printf new-baseline > {baseline}
printf peer-b > "$1/{golden}/b.pdf"
rm -f "$1/{golden}/e.pdf"
'''
        plain = self.snapshot("bash", "-c", refresh, "refresh", str(self.repo))
        self.assertEqual(plain.returncode, 0, plain.stderr)
        self.assertEqual((read("a"), read("d"), (self.repo / baseline).read_text()), ("old-a", None, "old-baseline"))
        (self.repo / golden / "b.pdf").write_text("old-b")

        golden_refresh = self.snapshot("bash", "-c", refresh, "refresh", str(self.repo), env={"UPDATE_REPORT_GOLDEN": "1"})
        self.assertEqual(golden_refresh.returncode, 0, golden_refresh.stderr)
        self.assertEqual({name: read(name) for name in "abcde"},
                         {"a": "new-a", "b": "peer-b", "c": "old-c", "d": "new-d", "e": None})
        self.assertEqual((self.repo / baseline).read_text(), "old-baseline")

        baseline_refresh = self.snapshot("bash", "-c", refresh, "refresh", str(self.repo), env={"UPDATE_CORPUS_BASELINE": "1"})
        self.assertEqual(baseline_refresh.returncode, 0, baseline_refresh.stderr)
        self.assertEqual((self.repo / baseline).read_text(), "new-baseline")


if __name__ == "__main__":
    unittest.main()
