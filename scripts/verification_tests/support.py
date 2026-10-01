"""Shared batch-admission fixture: a disposable repository, fake task runner, and queue."""

import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
import unittest.mock


SCRIPT = Path(__file__).resolve().parents[1].joinpath("test-execution.py")
spec = importlib.util.spec_from_file_location("batch_admission", SCRIPT)
execution = importlib.util.module_from_spec(spec)
spec.loader.exec_module(execution)


@unittest.skipIf(os.name == "nt", "batch leases require inherited POSIX file locks")
class BatchFixture(unittest.TestCase):
    state_timeout = 15

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="verification-fixture-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.scripts = self.repo / "scripts"
        self.scripts.mkdir()
        for path in SCRIPT.parent.glob("verification_*.py"):
            shutil.copy2(path, self.scripts / path.name)
        for name in ["test-execution.py", "test-source-snapshot.sh", "test-run-isolation.sh", "test-run-lease.py",
                     "digest-run-lock.sh", "repo-snapshot-lock.sh", "snapshot-publish.sh",
                     "artifact-paths.sh", "artifact_paths.py"]:
            shutil.copy2(SCRIPT.with_name(name), self.scripts / name)
        self.catalog = {"resources": {}, "private": [], "ci": {},
            "environment": {"names": ["PATH", "HOME", "TMPDIR", "USER", "LANG"],
                            "prefixes": ["LC_", "GO", "PW_", "FIXTURE_", "XDG_"]},
            "selection": {"test:digest": "go", "den:harness:test": "playwright"},
            "groups": {"check-fast": ["build", "lint", "unit"]}, "go": {
            "test:digest": {"packages": ["./..."], "options": []}},
            "tasks": {name: [name] for name in ["build", "lint", "unit", "den:harness:test"]}}
        self.save_catalog()
        (self.repo / "source.txt").write_text("initial")
        (self.repo / "lycaon").mkdir()
        (self.repo / "lycaon" / "fixture.txt").write_text("fixture\n")
        for package in ("good", "bad", "shared"):
            (self.repo / "lycaon" / package).mkdir()
            (self.repo / "lycaon" / package / "fixture_test.go").write_text("package fixture\n")
        (self.repo / "lycaon-den" / "e2e").mkdir(parents=True)
        (self.repo / "lycaon-den" / "e2e" / "fixture.spec.ts").write_text("")
        (self.repo / ".gitignore").write_text(".task/\n.bin/\n")
        (self.repo / "Taskfile.yml").write_text("fixture")
        self.calls = self.root / "calls.jsonl"
        self.hold = self.root / "hold"
        self.release = self.root / "release"
        self.tools = self.root / "tools"
        self.tools.mkdir()
        fake = '''#!/usr/bin/env python3
import json, os, pathlib, subprocess, sys, time
args=sys.argv[1:]
if '--dry' in args:
 plan=json.loads((pathlib.Path.cwd()/'scripts/verification-plan.json').read_text())
 end=args.index('--') if '--' in args else len(args)
 for name in args[args.index('--force')+1:end] or ['default']:
  if name not in {*plan['groups'], *plan['go'], *plan['tasks'], *plan['resources'], 'default', 'test:forwards', 'test:drops', 'test:exclusive'}:
   print('task: Available tasks for this project:', file=sys.stderr)
   print('task: Task "%s" does not exist' % name, file=sys.stderr); sys.exit(200)
  forwarded=args[end+1:] if name in {*plan['selection'], 'test:forwards'} else []
  print('task: [%s] fixture %s' % (name, ' '.join(forwarded)), file=sys.stderr)
 sys.exit(0)
name=[a for a in (args[:args.index('--')] if '--' in args else args) if not a.startswith('-')][-1]
root=pathlib.Path.cwd()
subprocess.run([sys.executable,str(root/'scripts/test-execution.py'),'holding'],pass_fds=(200,),check=True)
record={'name':name, 'source':(root/'source.txt').read_text(), 'cwd':str(root), 'workers':os.environ['PW_TEST_WORKERS'], 'pid':os.getpid()}
with open(os.environ['FIXTURE_CALLS'],'a') as f: f.write(json.dumps(record)+'\\n')
if pathlib.Path(os.environ['FIXTURE_HOLD']).exists():
 while not pathlib.Path(os.environ['FIXTURE_RELEASE']).exists(): time.sleep(.01)
assert (root/'source.txt').is_file(), 'source lease was lost'
subprocess.run([sys.executable,str(root/'scripts/test-execution.py'),'holding'],check=True)
print('fixture '+name, flush=True)
sys.exit(1 if name == os.environ.get('FIXTURE_FAIL') else 0)
'''
        (self.repo / "task").write_text(fake)
        (self.repo / "task").chmod(0o755)
        (self.tools / "go").write_text('''#!/usr/bin/env python3
import json, sys
if sys.argv[1] == 'env': print('/tmp/verification-fixture-gocache')
else:
 for arg in sys.argv[2:]:
  if arg.startswith('./'): print(json.dumps({'ImportPath':'fixture/'+arg[2:]}))
''')
        (self.tools / "go").chmod(0o755)
        (self.scripts / "go-test-digest.sh").write_text('''#!/usr/bin/env bash
exec python3 scripts/fake-digest.py "$@"
''')
        (self.scripts / "fake-digest.py").write_text('''import json, os, pathlib, sys, time
args=sys.argv[1:]; packages=[p for p in args[args.index('--')+1:] if not p.startswith('-')]
with open(os.environ['FIXTURE_CALLS'],'a') as f: f.write(json.dumps({'packages':packages})+'\\n')
events=os.environ.get('PW_TEST_STAGE_EVENTS')
if events and os.environ.get('FIXTURE_EARLY'):
 with open(events,'a') as f:
  for p in packages:
   if p.endswith('/bad'): f.write(json.dumps({'package':p,'exit_code':1,'tests':['TestBad']})+'\\n')
if pathlib.Path(os.environ['FIXTURE_HOLD']).exists():
 while not pathlib.Path(os.environ['FIXTURE_RELEASE']).exists(): time.sleep(.01)
results={p:('fail' if p.endswith('/bad') else 'pass') for p in packages}
if os.environ.get('FIXTURE_MISSING'): results.pop(packages[-1], None)
code=1 if 'fail' in results.values() else 0
pathlib.Path(os.environ['PW_TEST_STAGE_RESULT']).write_text(json.dumps({'completed':True,'packages':results,'process_exit_code':code,'digest_exit_code':code}))
sys.exit(code)
''')
        self.git("init", "-q")
        self.git("add", ".")
        self.git("-c", "user.name=Verification fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "Fixture")
        self.queue = execution.Queue(self.root / "queue")
        self.artifacts = self.root / "artifacts"
        self.artifacts.mkdir(parents=True, exist_ok=True)
        self.env = {**os.environ, "PW_TEST_EXECUTION_ROOT": str(self.queue.root), "PW_TEST_WORKERS": "1",
                    "PW_ARTIFACT_ROOT": str(self.artifacts),
                    "PW_LOCK_ROOT": str(self.root / "locks"),
                    "GOCACHE": str(self.root / "go-cache"), "XDG_CACHE_HOME": str(self.root / "user-cache"),
                    "PW_TEST_SNAPSHOT_ROOT": str(self.root / "snapshots"), "FIXTURE_CALLS": str(self.calls),
                    "FIXTURE_HOLD": str(self.hold), "FIXTURE_RELEASE": str(self.release),
                    "PATH": str(self.tools) + os.pathsep + os.environ["PATH"]}
        for key in list(self.env):
            if key.startswith("PW_SOURCE_") or key in {"PW_TEST_EXECUTION_TICKET", "PW_TEST_ARTIFACT_ROOT",
                                                       "PW_TEST_STAGE_RESULT", "PW_TEST_STAGE_RAW"}:
                self.env.pop(key)
        env_patcher = unittest.mock.patch.dict(os.environ, {
            "PW_ARTIFACT_ROOT": str(self.artifacts),
            "PW_TEST_ARTIFACT_ROOT": str(self.artifacts),
            "PW_LOCK_ROOT": str(self.root / "locks"),
        })
        env_patcher.start()
        self.addCleanup(env_patcher.stop)
        self.processes = []
        self.addCleanup(self.cleanup_processes)
        self.queue.pause("fixture")

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.repo), *args], text=True, stderr=subprocess.STDOUT)

    def save_catalog(self):
        (self.scripts / "verification-plan.json").write_text(json.dumps(self.catalog))

    def start(self, *names, env=None):
        process = subprocess.Popen([sys.executable, str(self.scripts / "test-execution.py"), "task", "--",
                                    str(self.repo / "task"), str(self.repo), *names], cwd=self.repo,
                                   env=env or self.env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        self.processes.append(process)
        return process

    def await_condition(self, predicate):
        deadline = time.monotonic() + self.state_timeout
        while time.monotonic() < deadline:
            if predicate():
                return
            time.sleep(.02)
        self.fail("batch fixture did not reach the expected state: " + json.dumps(self.queue.status()))

    def queued(self, count):
        self.await_condition(lambda: len([e for e in self.queue.status()["runs"] if e.get("kind") == "request"]) == count)

    def collect(self, process, code=0):
        out, err = process.communicate(timeout=self.state_timeout + 5)
        self.assertEqual(process.returncode, code, out + err)
        return out + err

    def records(self):
        return [json.loads(line) for line in self.calls.read_text().splitlines()] if self.calls.exists() else []

    def receipts(self):
        return [json.loads(path.read_text()) for path in (self.artifacts / "verification").glob('*/*.json')
                if '"request":' in path.read_text()]

    def cleanup_processes(self):
        self.release.touch()
        for process in self.processes:
            if process.poll() is None:
                process.terminate()
            try:
                process.communicate(timeout=15)
            except subprocess.TimeoutExpired:
                process.kill()
                process.communicate()
        deadline = time.monotonic() + 15
        while self.queue.status()["runs"] and time.monotonic() < deadline:
            time.sleep(.05)
