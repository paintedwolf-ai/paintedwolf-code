"""Validate every GitHub Actions workflow with the pinned analyzer."""
from pathlib import Path
import subprocess
import sys

from analysis_tools import ensure
from toolchain import python_tools

ACTIONLINT_VERSION="v1.7.12"
ROOT = Path(__file__).resolve().parents[2]
REQUIREMENTS = Path(__file__).with_name("requirements.txt")
# actionlint #657 lacks GitHub's concurrency.queue grammar; validate it structurally.
QUEUE_DIAGNOSTIC = (r'^unexpected key "queue" for "concurrency" section\. '
                    r'expected one of "cancel-in-progress", "group"$')


def prepare():
    ensure("actionlint")
    python_tools.provision(REQUIREMENTS)


def queues(document):
    if not isinstance(document, dict):
        return
    scopes = [("workflow", document)]
    jobs = document.get("jobs")
    if isinstance(jobs, dict):
        scopes.extend((f"job {name}", job) for name, job in jobs.items() if isinstance(job, dict))
    for name, scope in scopes:
        concurrency = scope.get("concurrency")
        if not isinstance(concurrency, dict) or "queue" not in concurrency:
            continue
        queue = concurrency["queue"]
        if not isinstance(queue, str) or queue not in {"single", "max"}:
            raise ValueError(f"{name}: concurrency.queue must be single or max")
        if queue == "max" and concurrency.get("cancel-in-progress") is True:
            raise ValueError(f"{name}: concurrency.queue max cannot combine with cancel-in-progress true")


def validate(root=ROOT, binary=None):
    workflows = sorted(path for path in (root / ".github/workflows").iterdir()
                       if path.is_file() and path.suffix in {".yml", ".yaml"})
    if not workflows:
        raise ValueError("no GitHub Actions workflows found")
    sys.path.insert(0, str(python_tools.prepared(REQUIREMENTS)))
    import yaml
    for path in workflows:
        try:
            queues(yaml.safe_load(path.read_text()))
        except (ValueError, yaml.YAMLError) as error:
            print(f"{path}: {error}", file=sys.stderr)
            return 1
    binary = binary or ensure("actionlint")
    return subprocess.run([str(binary), "-shellcheck=", "-pyflakes=", "-ignore", QUEUE_DIAGNOSTIC,
                           *map(str, workflows)], cwd=root, check=False).returncode
