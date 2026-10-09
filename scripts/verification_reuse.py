"""Passing Go package results reused for an identical build and identical observed inputs.

A result is keyed by the package, its build identity, the test-selection arguments, the Go runtime
settings the binary reads directly, and the declared verification environment. The build identity hashes
what Go's build ID covers (compiled and embedded file contents across the dependency closure, external
module versions, tags, race mode, toolchain) except the checkout path, which differs between snapshot
slots. Each recorded pass lists the files and environment variables the test log observed; a replay
requires all of them unchanged.

A test process that forks can read inputs the log never sees, so it is not recorded; detecting forks
needs kernel process events, available here only on macOS. Paths under the per-run scratch
directories are the tests' own output.
"""

import gzip
import hashlib
import json
import os
from pathlib import Path
import stat
import sys
import time

from verification_state import read_state, write_json

FORMAT = 1
MAX_VARIANTS = 4
MAX_OUTPUT_BYTES = 64 << 20
STORE_BYTES = 2 << 30
STORE_AGE_SECONDS = 14 * 86400
PRUNE_INTERVAL_SECONDS = 3600
ORPHAN_SECONDS = 3600
SOURCE_FIELDS = ("GoFiles", "CgoFiles", "CFiles", "CXXFiles", "MFiles", "HFiles", "FFiles", "SFiles", "SwigFiles",
                 "SwigCXXFiles", "SysoFiles", "EmbedFiles", "TestGoFiles", "XTestGoFiles", "TestEmbedFiles",
                 "XTestEmbedFiles")
FLAG_FIELDS = ("CgoCFLAGS", "CgoCPPFLAGS", "CgoCXXFLAGS", "CgoFFLAGS", "CgoLDFLAGS", "CgoPkgConfig")
BUILD_ENV = ("GOVERSION", "GOOS", "GOARCH", "GOEXPERIMENT", "CGO_ENABLED", "CC", "CXX", "CGO_CFLAGS",
             "CGO_CPPFLAGS", "CGO_CXXFLAGS", "CGO_FFLAGS", "CGO_LDFLAGS", "GOAMD64", "GOARM64", "GOARM", "GO386")
# go test flags that take a separate value, so the value is not read as a package pattern.
VALUE_FLAGS = {"-p", "-parallel", "-timeout", "-tags", "-run", "-skip", "-count", "-cpu", "-exec", "-shuffle"}
DURATION_SAMPLES = 8
DURATION_KEYS = 64
# Arguments that decide which tests run and how they report. Anything else, such as profiles, fuzzing,
# benchmarks, or coverage output, has effects a replay cannot reproduce, so it is never reused.
SELECTION_FLAGS = {"-test.v", "-test.run", "-test.skip", "-test.short", "-test.count", "-test.cpu",
                   "-test.paniconexit0", "-test.failfast", "-test.list"}
# Admission-shaped settings: how much of the host a run had, not what it tested.
RESOURCE_FLAGS = {"-test.timeout", "-test.parallel"}
RESOURCE_ENV = {"PW_TEST_WORKERS", "PW_TEST_TIMEOUT_SCALE", "GOMAXPROCS", "GO_TEST_P", "GO_TEST_PARALLEL",
                "PW_VITEST_MAX_WORKERS", "PW_FUZZ_WORKERS", "CARGO_BUILD_JOBS", "RUST_TEST_THREADS"}
# Settings the Go runtime reads without os.Getenv, so the test log cannot observe them.
RUNTIME_ENV = ("GODEBUG", "GOTRACEBACK", "GOGC", "GOMEMLIMIT", "GORACE", "TZ")


def fork_observable():
    return sys.platform == "darwin"


def default_store():
    if not fork_observable():
        return None
    base = os.environ.get("XDG_CACHE_HOME") or str(Path.home() / "Library" / "Caches")
    return Path(base) / "paintedwolf" / "verification-results"


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def environment_identity(environment):
    return digest({key: value for key, value in environment.items() if key not in RESOURCE_ENV})


def build_arguments(arguments):
    """Package patterns and the flags that change what `go test` compiles."""
    patterns, flags, index = [], [], 0
    while index < len(arguments):
        argument = arguments[index]
        name, separator, value = argument.partition("=")
        if not argument.startswith("-"):
            patterns.append(argument)
        elif name in VALUE_FLAGS:
            if not separator:
                index += 1
                value = arguments[index] if index < len(arguments) else ""
            if name == "-tags":
                flags.append("-tags=" + value)
        elif name == "-race":
            flags.append("-race")
        index += 1
    return patterns, flags


def own_identity(record, memo):
    """What one package in a test binary's closure contributes, or None when it cannot be established."""
    if record.get("Error") or record.get("DepsErrors") or record.get("Incomplete"):
        return None
    if record.get("Standard"):
        return "standard"
    module = record.get("Module") or {}
    replacement = module.get("Replace") or {}
    versioned = replacement if replacement else module
    if module and not module.get("Main") and versioned.get("Version"):
        return f"module {versioned['Path']}@{versioned['Version']}"
    files = []
    for field in SOURCE_FIELDS:
        for name in record.get(field, []):
            path = os.path.join(record.get("Dir", ""), name)
            try:
                files.append([field, "<generated>" if os.path.isabs(name) else name, file_digest(path, memo)])
            except OSError:
                return None
    flags = {field: record.get(field) for field in FLAG_FIELDS if record.get(field)}
    return digest({"files": files, "flags": flags, "go": module.get("GoVersion")})


def package_identities(records, toolchain):
    """Build identity per tested package, from `go list -deps -test -json` records."""
    records = {record["ImportPath"]: record for record in records if isinstance(record.get("ImportPath"), str)}
    memo, owned, identities = {}, {}, {}
    for path, record in records.items():
        if not path.endswith(".test") or record.get("Name") != "main":
            continue
        closure = [path, *record.get("Deps", [])]
        parts = []
        for dependency in closure:
            if dependency not in owned:
                found = records.get(dependency)
                owned[dependency] = own_identity(found, memo) if found else None
            if owned[dependency] is None:
                break
            parts.append([dependency, owned[dependency]])
        else:
            identities[path.removesuffix(".test")] = digest({"toolchain": toolchain, "packages": parts})
    return identities


def write_identities(output, arguments):
    """Run from the module root with the effective `go test` arguments."""
    import subprocess
    patterns, flags = build_arguments(arguments)
    environment = json.loads(subprocess.check_output(["go", "env", "-json", *BUILD_ENV], text=True))
    listed = subprocess.run(["go", "list", "-deps", "-test", "-json", *flags, *patterns],
                            capture_output=True, text=True, check=True).stdout
    decoder, records, text = json.JSONDecoder(), [], listed
    while text.strip():
        text = text.lstrip()
        value, end = decoder.raw_decode(text)
        records.append(value)
        text = text[end:]
    identities = package_identities(records, {"environment": environment, "flags": sorted(flags)})
    Path(output).write_text(json.dumps(identities))


def selection_arguments(arguments):
    """The arguments that belong in the key, or None when the invocation has effects beyond its output."""
    selected = []
    for argument in arguments:
        name = argument.partition("=")[0]
        if not argument.startswith("-test."):
            return None
        if name in RESOURCE_FLAGS:
            continue
        if name not in SELECTION_FLAGS:
            return None
        selected.append(argument)
    return selected


class Context:
    """Where this run's source snapshot and private scratch directories live."""

    def __init__(self, source, scratch, module):
        self.source = sorted({source, os.path.realpath(source)}) if source else []
        self.scratch = sorted({scratch, os.path.realpath(scratch)}) if scratch else []
        self.primary = source
        self.module = module

    @classmethod
    def from_environment(cls, environment):
        return cls(environment.get("PW_TEST_SOURCE_ROOT", ""), environment.get("PW_TEST_SCRATCH_ROOT", ""),
                   environment.get("PW_TEST_MODULE_ROOT", ""))

    def package(self, directory):
        """Import path of the package whose test binary runs in `directory`."""
        module_root = Path(self.module)
        name = next((line.split()[1] for line in (module_root / "go.mod").read_text().splitlines()
                     if line.startswith("module ")), None)
        relative = Path(os.path.realpath(directory)).relative_to(os.path.realpath(module_root)).as_posix()
        return name if relative == "." else f"{name}/{relative}"

    def within(self, path, roots):
        return next((root for root in roots if path == root or path.startswith(root + os.sep)), None)

    def key(self, path):
        """A location independent of this run's roots; None for the run's own scratch output."""
        if self.within(path, self.scratch):
            return None
        root = self.within(path, self.source)
        return "<source>" + path[len(root):] if root else path

    def path(self, key):
        return self.primary + key[len("<source>"):] if key.startswith("<source>") else key

    def value(self, text):
        for placeholder, roots in (("<scratch>", self.scratch), ("<source>", self.source)):
            for root in sorted(roots, key=len, reverse=True):
                text = text.replace(root, placeholder)
        return text


def observed_inputs(log_text, directory, context):
    """Inputs from a Go test log, resolving relative names against the directory each was opened from."""
    lines = log_text.splitlines()
    if not lines or lines[0] != "# test log":
        return None
    inputs, current = set(), directory
    for line in lines[1:]:
        operation, _, argument = line.partition(" ")
        if operation == "getenv":
            inputs.add(("getenv", argument))
        elif operation in {"open", "stat", "chdir"}:
            path = os.path.normpath(os.path.join(current, argument))
            if operation == "chdir":
                current = path
                continue
            key = context.key(path)
            if key is not None:
                inputs.add((operation, key))
        else:
            return None
    return sorted(inputs)


def file_digest(path, memo):
    info = os.stat(path)
    token = (info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns)
    if token not in memo:
        hasher = hashlib.sha256()
        with open(path, "rb") as stream:
            for block in iter(lambda: stream.read(1 << 20), b""):
                hasher.update(block)
        memo[token] = hasher.hexdigest()
    return memo[token]


def entry_kind(entry):
    if entry.is_symlink():
        return "l"
    if entry.is_dir(follow_symlinks=False):
        return "d"
    return "f" if entry.is_file(follow_symlinks=False) else "o"


def describe(operation, key, context, environment, memo):
    if operation == "getenv":
        if key in RESOURCE_ENV:
            return "resource"
        value = environment.get(key)
        return "unset" if value is None else "value:" + context.value(value)
    path = context.path(key)
    outside = not key.startswith("<source>")
    try:
        if operation == "stat":
            # A directory's size and time track its listing, which a test observes by opening it.
            info = os.lstat(path)
            parts = [stat.S_IFMT(info.st_mode), info.st_mode & 0o777]
            if stat.S_ISREG(info.st_mode):
                parts.append(info.st_size)
                if outside:
                    parts.append(info.st_mtime_ns)
            if stat.S_ISLNK(info.st_mode):
                target = os.stat(path)
                parts.extend([os.readlink(path), stat.S_IFMT(target.st_mode)])
                if stat.S_ISREG(target.st_mode):
                    parts.append(target.st_size)
            return "stat:" + json.dumps(parts)
        info = os.stat(path)
        if stat.S_ISDIR(info.st_mode):
            with os.scandir(path) as listing:
                return "dir:" + digest(sorted((entry.name, entry_kind(entry)) for entry in listing))
        if stat.S_ISREG(info.st_mode):
            return "file:" + file_digest(path, memo)
        return f"type:{stat.S_IFMT(info.st_mode)}"
    except FileNotFoundError:
        return "missing"
    except OSError as error:
        return f"error:{error.errno}"


def fingerprint(inputs, context, environment, memo=None):
    memo = {} if memo is None else memo
    return digest([[operation, key, describe(operation, key, context, environment, memo)]
                   for operation, key in inputs])


def build_identity(package, environment):
    try:
        return json.loads(Path(environment["PW_TEST_PACKAGE_IDENTITIES"]).read_text()).get(package)
    except (KeyError, OSError, ValueError):
        return None


def result_key(arguments, package, environment):
    selected = selection_arguments(arguments)
    identity = build_identity(package, environment)
    if selected is None or not isinstance(identity, str):
        return None
    return digest({"format": FORMAT, "package": package, "build": identity, "arguments": selected,
                   "resource_policy": environment.get("PW_PACKAGE_RESOURCE_IDENTITY", ""),
                   "runtime": {name: environment.get(name) for name in RUNTIME_ENV},
                   "environment": environment.get("PW_TEST_ENVIRONMENT_IDENTITY", "")})


def entry_path(store, key):
    return store / "entries" / key[:2] / (key + ".json")


def output_path(store, name):
    return store / "outputs" / name[:2] / (name + ".gz")


def atomic_write(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    temporary.write_bytes(data)
    temporary.replace(path)


def lookup(store, key, context, environment):
    """The recorded output of a matching pass, and where it was recorded, or None."""
    path = entry_path(store, key)
    record = read_state(path)
    memo = {}
    for variant in record.get("variants", []):
        try:
            inputs = [tuple(item) for item in variant["inputs"]]
            if fingerprint(inputs, context, environment, memo) != variant["fingerprint"]:
                continue
            blob = output_path(store, variant["output"])
            output = gzip.decompress(blob.read_bytes())
        except (KeyError, TypeError, ValueError, OSError, EOFError):
            continue
        now = time.time()
        for touched in (path, blob):
            try:
                os.utime(touched, (now, now))
            except OSError:
                pass
        return output, variant.get("provenance", {})
    return None


def record(store, key, package, inputs, context, environment, output, elapsed, provenance):
    if len(output) > MAX_OUTPUT_BYTES:
        return False
    name = hashlib.sha256(output).hexdigest()
    blob = output_path(store, name)
    if not blob.exists():
        atomic_write(blob, gzip.compress(output))
    variant = {"inputs": [list(item) for item in inputs], "fingerprint": fingerprint(inputs, context, environment),
               "output": name, "elapsed": round(elapsed, 3), "recorded_at": time.time(), "provenance": provenance}
    path = entry_path(store, key)
    existing = [v for v in read_state(path).get("variants", []) if v.get("fingerprint") != variant["fingerprint"]]
    body = {"format": FORMAT, "package": package, "variants": [variant, *existing][:MAX_VARIANTS]}
    atomic_write(path, json.dumps(body, separators=(",", ":")).encode())
    return True


def prune(store, now=None):
    """Least recently used entries go first once the store exceeds its budget; unused ones age out."""
    now = time.time() if now is None else now
    store.mkdir(parents=True, exist_ok=True)
    stamp = store / "pruned-at"
    try:
        if now - stamp.stat().st_mtime < PRUNE_INTERVAL_SECONDS:
            return
    except FileNotFoundError:
        pass
    if os.name != "nt":
        import fcntl
        with (store / ".prune.lock").open("a") as lock:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                return
            collect(store, now)
            stamp.touch()


def collect(store, now):
    entries = []
    for path in (store / "entries").glob("*/*.json"):
        try:
            info = path.stat()
        except FileNotFoundError:
            continue
        entries.append((info.st_mtime, path, info.st_size))
    entries.sort()
    outputs = {}
    for path in (store / "outputs").glob("*/*.gz"):
        try:
            info = path.stat()
        except FileNotFoundError:
            continue
        outputs[path.name[:-3]] = (path, info.st_size, info.st_mtime)
    total = sum(size for _, _, size in entries) + sum(size for _, size, _ in outputs.values())
    kept = []
    for modified, path, size in entries:
        if now - modified > STORE_AGE_SECONDS or total > STORE_BYTES:
            path.unlink(missing_ok=True)
            total -= size
        else:
            kept.append(path)
    referenced = set()
    for path in kept:
        referenced.update(v.get("output") for v in read_state(path).get("variants", []))
    for name, (path, size, modified) in outputs.items():
        # A writer stores its output before its entry, so a young unreferenced output may still be claimed.
        if name not in referenced and now - modified > ORPHAN_SECONDS:
            path.unlink(missing_ok=True)


def emit(events, value):
    line = (json.dumps(value, separators=(",", ":")) + "\n").encode()
    descriptor = os.open(events, os.O_WRONLY | os.O_APPEND | os.O_CREAT, 0o600)
    try:
        os.write(descriptor, line)
    finally:
        os.close(descriptor)


def read_events(path):
    """The last outcome each package's test binary reported in a stage."""
    outcomes = {}
    try:
        text = Path(path).read_text(errors="replace")
    except FileNotFoundError:
        return outcomes
    for line in text.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if isinstance(event, dict) and isinstance(event.get("package"), str):
            outcomes[event["package"]] = event
    return outcomes


def durations_path(queue):
    return queue.root / "package-durations.json"


def record_package_durations(queue, stage_key, outcomes):
    measured = {package: event["elapsed"] for package, event in outcomes.items()
                if not event.get("reused") and type(event.get("elapsed")) in (int, float) and event["elapsed"] >= 0}
    if not measured:
        return
    with queue.locked():
        state = read_state(durations_path(queue))
        stage = state.get(stage_key) if isinstance(state.get(stage_key), dict) else {}
        packages = stage.get("packages") if isinstance(stage.get("packages"), dict) else {}
        for package, seconds in measured.items():
            previous = [s for s in packages.get(package, []) if type(s) in (int, float)]
            packages[package] = [*previous, round(seconds, 3)][-DURATION_SAMPLES:]
        state[stage_key] = {"packages": packages, "updated_at": time.time()}
        if len(state) > DURATION_KEYS:
            ranked = sorted(state.items(), key=lambda item: item[1].get("updated_at", 0), reverse=True)
            state = dict(ranked[:DURATION_KEYS])
        write_json(durations_path(queue), state)


def package_durations(queue, stage_key):
    stage = read_state(durations_path(queue)).get(stage_key)
    packages = stage.get("packages", {}) if isinstance(stage, dict) else {}
    result = {}
    for package, values in packages.items():
        values = sorted(v for v in values if type(v) in (int, float)) if isinstance(values, list) else []
        if values:
            result[package] = values[len(values) // 2]
    return result


if __name__ == "__main__":
    if len(sys.argv) < 4 or sys.argv[1] != "identities" or sys.argv[3] != "--":
        raise SystemExit("usage: verification_reuse.py identities OUTPUT -- <go test arguments>")
    write_identities(sys.argv[2], sys.argv[4:])
