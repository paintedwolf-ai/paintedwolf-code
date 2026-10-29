"""Statement coverage of scripts Python under the declared Python suites.

Suites run under a disposable virtual environment whose startup hook starts
coverage.py in every interpreter it launches, so a child started as `python3`
from PATH or as `sys.executable` is measured even with a cleared environment
or `-I`. A fixture that copies a script into a disposable `scripts/` tree is
credited to the source it copied: a measured file counts for a changed source
when its path ends with that source's repository path.
"""

import os
from pathlib import Path
import shutil
import subprocess
import sys
import venv

from toolchain import python_tools

REQUIREMENTS = Path(__file__).resolve().with_name("requirements.txt")
# The same test-code rule the maintainability inventory applies.
TEST_DIRECTORIES = {"test", "tests", "e2e", "testutil", "testcorpus", "testsupport", "testfixture",
                    "verification_tests", "__tests__"}
STARTUP = """import os, sys
sys.path.extend({tools!r})
os.environ["COVERAGE_PROCESS_START"] = {rc!r}
# A parent's coverage.py hands an exec'd child its own configuration this way; this hook decides.
os.environ.pop("COVERAGE_PROCESS_CONFIG", None)
try:
    import coverage
    if coverage.process_startup() is not None:
        # The gate refuses a run in which no measured process recorded that coverage started.
        open(os.path.join({data!r}, "started.%d" % os.getpid()), "a").close()
except Exception:
    pass
"""


def sources(root, paths):
    """Changed handwritten scripts Python, without test code."""
    out = []
    for path in paths:
        parts = path.split("/")
        name = parts[-1]
        if (parts[0] != "scripts" or not name.endswith(".py") or name.startswith("test_") or name.endswith("_test.py")
                or TEST_DIRECTORIES.intersection(parts[:-1]) or not (Path(root) / path).is_file()):
            continue
        out.append(path)
    return out


def requirement_files(root, suites):
    return [REQUIREMENTS, *(Path(root) / suite["requirements"] for suite in suites.values() if suite.get("requirements"))]


def environment(work, tools, files):
    """An interpreter whose every process records `files` into work/data."""
    data = work / "data"
    data.mkdir(parents=True)
    rc = work / "coveragerc"
    include = "".join(f"\n    */{path}" for path in files)
    rc.write_text(f"[run]\ndata_file = {data / '.coverage'}\nparallel = True\nsigterm = True\n"
                  f"patch = _exit, execv, fork\ninclude ={include}\n")
    home = work / "python"
    venv.EnvBuilder(with_pip=False, symlinks=os.name != "nt", clear=True).create(home)
    python = home / ("Scripts/python.exe" if os.name == "nt" else "bin/python3")
    purelib = Path(subprocess.run([str(python), "-I", "-c", "import sysconfig; print(sysconfig.get_paths()['purelib'])"],
                                  capture_output=True, text=True, check=True).stdout.strip())
    startup = STARTUP.format(tools=[str(t) for t in tools], rc=str(rc), data=str(data))
    (purelib / "_pw_coverage_startup.py").write_text(startup)
    (purelib / "_pw_coverage_startup.pth").write_text("import _pw_coverage_startup\n")
    return python, data


class Measurement:
    """Statements of one source and which of them any measured process executed."""

    def __init__(self, parser, executed):
        self.parser = parser
        self.statements = parser.statements
        self.executed = parser.translate_lines(executed) & self.statements

    def changed(self, lines):
        """(changed lines, covered) for each statement a changed line belongs to."""
        grouped = {}
        for line in lines:
            for statement in self.parser.translate_lines({line}) & self.statements:
                grouped.setdefault(statement, set()).add(line)
        return [(grouped[statement], statement in self.executed) for statement in sorted(grouped)]


def executed_lines(data, files):
    """Lines each changed source ran in any measured process, wherever that process found its copy."""
    # Reading data files leaves a measured gate process alone; a second Coverage object would stop it saving.
    from coverage import CoverageData
    from coverage.exceptions import DataError
    executed = {path: set() for path in files}
    for file in sorted(data.glob(".coverage.*")):
        recorded = CoverageData(str(file))
        try:
            recorded.read()
        except DataError:
            continue  # A process killed while saving leaves a partial file.
        for measured in recorded.measured_files():
            normalized = measured.replace(os.sep, "/")
            for path in files:
                if normalized.endswith("/" + path):
                    executed[path].update(recorded.lines(measured) or ())
    return executed


def measure(root, files, suites, work):
    """{path: Measurement, or the error that kept it from being read} under every suite whose tree holds a changed
    source; None when a suite fails."""
    root = Path(root)
    tools = [python_tools.provision(requirements) for requirements in requirement_files(root, suites)]
    shutil.rmtree(work, ignore_errors=True)
    python, data = environment(work, tools, files)
    env = {**os.environ, "PATH": f"{python.parent}{os.pathsep}{os.environ.get('PATH', '')}"}
    for name, suite in sorted(suites.items()):
        start = suite["start"].rstrip("/")
        if not any(path.startswith(start + "/") for path in files):
            continue
        print(f"Changed coverage (Python): {name}: unittest discover -s {start} -p '{suite['pattern']}'", flush=True)
        if subprocess.call([str(python), "-m", "unittest", "discover", "-s", start, "-p", suite["pattern"]],
                           cwd=root, env=env) != 0:
            return None
    if not any(data.glob("started.*")):
        missing = RuntimeError("coverage.py did not start in any measured suite process")
        return {path: missing for path in files}
    sys.path.extend(str(t) for t in tools if str(t) not in sys.path)
    from coverage.exceptions import CoverageException
    from coverage.parser import PythonParser
    executed = executed_lines(data, files)
    out = {}
    for path in files:
        try:
            # No exclusions: changed code has no pragma that opts it out of measurement.
            parser = PythonParser(filename=str(root / path), exclude=None)
            parser.parse_source()
            out[path] = Measurement(parser, executed[path])
        except CoverageException as error:
            out[path] = error
    return out
