"""The final inline block for a request that did not pass: bounded, and built from runner results.

Status, target, and exit code come from receipts and job results. Patterns here only choose which
lines of a log to show; nothing reads them back as an outcome.
"""

from collections import deque
import json
from pathlib import Path
import re
import sys

MARKER = "=== Failure digest ==="
MAX_LINES = 40
MAX_BYTES = 4096
# A command result shows at least this much of a stream's end: the smallest model budget, split between
# two streams, then between the window's head and tail.
STREAM_TAIL_BYTES = 3072
# The process-finished kick keeps this much of a merged, labelled tail. Stdout and stderr cross separate
# pipes there, so a line written just before the digest can land after it.
MERGED_TAIL_BYTES = 4096
STREAM_LABEL_BYTES = 18
MIN_BYTES = 1024
LINE_BYTES = 240
NAME_BYTES = 120
EXCERPT_FLOOR_BYTES = 24
# A shown target gets its header, its failed tests or one excerpt line, and one more diagnostic.
TARGET_FLOOR_LINES = 3
MAX_TEST_NAMES = 3
TEST_OUTPUT_LINES = 64
LOG_SCAN_BYTES = 1 << 28
LOG_EARLY_LINES = 128
LOG_NEWEST_LINES = 64
JOB_TAIL_BYTES = 1 << 13

LINKER_NOISE = b"ld: warning: ignoring duplicate libraries: '-lobjc'"
CONTROL = re.compile(r"\x1b\[[0-9;?]*[ -/]*[@-~]|[\x00-\x08\x0b-\x1f\x7f]")
DIAGNOSTIC = re.compile(r"\b(?:FAIL(?:ED)?|ERROR|[Ee]rror|panic|[Ff]atal|[Ee]xception|Traceback|want|got|expected)\b"
                        r"|\S+\.\w+:\d+|[✗✘]")
FRAMING = re.compile(r"\s*(?:=== (?:RUN|PAUSE|CONT|NAME)\b|\[?PASS\]?\b|ok\s|task: |test execution: |exit status \d+$)")


class PreviewFilter:
    """Drops exact standalone linker warnings from a byte stream without holding back other output."""

    def __init__(self):
        self.partial = b""
        self.inside_line = False

    def feed(self, data):
        *complete, rest = (self.partial + data).split(b"\n")
        self.partial = b""
        kept = []
        for line in complete:
            if self.inside_line or line.removesuffix(b"\r") != LINKER_NOISE:
                kept.append(line + b"\n")
            self.inside_line = False
        if not self.inside_line and (LINKER_NOISE.startswith(rest) or rest.removesuffix(b"\r") == LINKER_NOISE):
            self.partial = rest
        else:
            kept.append(rest)
            self.inside_line = True
        return b"".join(kept)

    def finish(self):
        rest, self.partial = self.partial, b""
        return b"" if rest.removesuffix(b"\r") == LINKER_NOISE else rest


def size(lines):
    return sum(len(line.encode("utf-8", "replace")) + 1 for line in lines)


def clean(line):
    return CONTROL.sub("", line.encode("utf-8", "replace").decode("utf-8")).rstrip()


def clamp(text, limit):
    encoded = text.encode("utf-8", "replace")
    if len(encoded) <= limit:
        return text
    return encoded[:max(0, limit - 3)].decode("utf-8", "ignore") + "..."


def clamp_middle(text, limit):
    """A package's end identifies it and its start locates it, so both survive."""
    encoded = text.encode("utf-8", "replace")
    if len(encoded) <= limit:
        return text
    keep = max(0, limit - 3)
    return (encoded[:keep // 2].decode("utf-8", "ignore") + "..."
            + encoded[len(encoded) - (keep - keep // 2):].decode("utf-8", "ignore"))


def representative(lines, limit):
    """Lines that look diagnostic with one neighbor each side, oldest first; otherwise the newest lines."""
    if limit <= 0:
        return []
    matches = [index for index, line in enumerate(lines) if DIAGNOSTIC.search(line)]
    if not matches:
        return lines[-limit:]
    chosen = set()
    for index in matches:
        window = {i for i in (index - 1, index, index + 1) if 0 <= i < len(lines)}
        if len(chosen | window) <= limit:
            chosen |= window
        elif len(chosen) < limit:
            chosen.add(index)
    return [lines[index] for index in sorted(chosen)]


def log_lines(path):
    """A log's earliest diagnostics with their neighbors and its newest lines, however much passed between.

    Missing evidence is simply no excerpt.
    """
    early, newest = {}, deque(maxlen=LOG_NEWEST_LINES)
    previous, follow, index = None, False, 0
    try:
        with Path(path).open("rb") as stream:
            stream.seek(0, 2)
            stream.seek(max(0, stream.tell() - LOG_SCAN_BYTES))
            if stream.tell():
                stream.readline()
            for raw in stream:
                line = clean(raw.decode("utf-8", "replace"))
                if not line or FRAMING.match(line) or line.encode() == LINKER_NOISE:
                    continue
                matched = DIAGNOSTIC.search(line) and len(early) < LOG_EARLY_LINES
                if matched and previous is not None:
                    early[index - 1] = previous
                if matched or follow:
                    early[index] = line
                follow = bool(matched)
                newest.append((index, line))
                previous = line
                index += 1
    except (OSError, TypeError):
        return []
    return [line for _, line in sorted({**early, **dict(newest)}.items())]


def go_records(path, packages):
    """Failure output per package from a stage's `go test -json` events."""
    records = {package: {"failed": [], "tests": {}, "package": deque(maxlen=TEST_OUTPUT_LINES),
                         "build": deque(maxlen=TEST_OUTPUT_LINES)} for package in packages}
    if not records or not path:
        return records
    needles = re.compile("|".join(re.escape(package) for package in sorted(records)).encode())
    try:
        with Path(path).open("rb") as stream:
            for line in stream:
                if not needles.search(line):
                    continue
                try:
                    event = json.loads(line)
                except ValueError:
                    continue
                if isinstance(event, dict):
                    record_event(records, event)
    except OSError:
        pass
    return records


def record_event(records, event):
    action, test = event.get("Action"), event.get("Test")
    if action == "build-output" and isinstance(event.get("ImportPath"), str):
        record = records.get(event["ImportPath"].split(" [", 1)[0])
        if record is not None:
            record["build"].append(str(event.get("Output", "")))
        return
    record = records.get(event.get("Package"))
    if record is None:
        return
    if action == "fail" and isinstance(test, str):
        record["failed"].append(test)
    elif action == "output":
        output = str(event.get("Output", ""))
        if isinstance(test, str):
            record["tests"].setdefault(test, deque(maxlen=TEST_OUTPUT_LINES)).append(output)
        else:
            record["package"].append(output)


def shown(lines):
    cleaned = (clean(line) for line in lines)
    return [line for line in cleaned if line and not FRAMING.match(line) and line.encode() != LINKER_NOISE]


def go_diagnostics(record):
    """The failed test names, which are always shown, and the output to choose from."""
    failed = sorted(set(record["failed"]))
    # A parent fails because a child did; the child holds the assertion.
    leaves = [test for test in failed if not any(other.startswith(test + "/") for other in failed)]
    lead, lines = [], []
    if leaves:
        more = len(leaves) - MAX_TEST_NAMES
        lead.append("Tests: " + ", ".join(leaves[:MAX_TEST_NAMES]) + (f" (+{more} more)" if more > 0 else ""))
    for test in leaves:
        lines.extend(line for line in shown(record["tests"].get(test, ())) if not line.lstrip().startswith("--- FAIL"))
    lines.extend(shown(record["build"]))
    package = shown(record["package"])
    if leaves:
        panic = next((index for index, line in enumerate(package) if line.startswith("panic:")), None)
        package = package[panic:] if panic is not None else []
    lines.extend(package)
    return lead, lines


def targets(receipt, directory):
    """Every result in the request's own receipt that did not pass; failures lead, then evidence order."""
    found = []
    raws = {}
    for evidence in receipt.get("evidence", []):
        for package, result in sorted(evidence.get("results", {}).items()):
            if result.get("status") == "passed":
                continue
            stage = evidence.get("stage", "verification")
            found.append({"name": stage if package == "task" else f"{stage} {package}", "package": package,
                          "status": result.get("status", "unverified"), "exit_code": result.get("exit_code", 2),
                          "result": result})
            stage_id = result.get("stage_id")
            if package != "task" and stage_id:
                raws.setdefault(result.get("raw") or str(Path(directory) / f"{stage_id}-go.json"), []).append(package)
    records = {}
    for path, packages in raws.items():
        records[path] = go_records(path, packages)
    for target in found:
        target["lead"], target["lines"] = target_diagnostics(target, records, directory)
    return sorted(found, key=lambda target: target["status"] != "failed")


def target_diagnostics(target, records, directory):
    result = target["result"]
    if target["package"] == "task":
        return [], log_lines(result.get("log"))
    raw = result.get("raw") or str(Path(directory) / f"{result.get('stage_id')}-go.json")
    record = records.get(raw, {}).get(target["package"])
    lead, lines = go_diagnostics(record) if record else ([], [])
    # Unknown formats get a raw excerpt rather than nothing.
    return lead, lines or log_lines(result.get("output")) or log_lines(result.get("log"))


def target_header(target):
    return f"- {clamp_middle(target['name'], NAME_BYTES)}: {target['status']}, exit {target['exit_code']}"


def target_block(target, available_bytes, available_lines):
    block = [target_header(target)]
    left = available_bytes - size(block)
    lead = target["lead"]
    for line in [*lead, *representative(target["lines"], available_lines - 1 - len(lead))]:
        room = min(LINE_BYTES, left - 1)
        if room < EXCERPT_FLOOR_BYTES:
            break
        entry = clamp("  " + line, room)
        block.append(entry)
        left -= size([entry])
    return block


def compose(head, found, references, max_bytes):
    """Reference and overflow space is taken first; targets share the rest in order, so unused room rolls on."""
    references = [clamp_middle(reference, max_bytes // 4) for reference in references]
    total = len(found)
    overflow = f"Omitted: {total} of {total} targets. This digest does not list every cause."
    head = [clamp(line, LINE_BYTES) for line in head]
    while size([*head, *references, overflow]) > max_bytes and len(head) > 2:
        head.pop()
    left_bytes = max_bytes - size([*head, *references, overflow])
    left_lines = MAX_LINES - len(head) - len(references) - 1
    count, cost = 0, 0
    for target in found:
        cost += size([target_header(target)]) + (TARGET_FLOOR_LINES - 1) * EXCERPT_FLOOR_BYTES
        if cost > left_bytes or (count + 1) * TARGET_FLOOR_LINES > left_lines:
            break
        count += 1
    body = []
    for index, target in enumerate(found[:count]):
        remaining = count - index
        block = target_block(target, left_bytes // remaining, left_lines // remaining)
        body.extend(block)
        left_bytes -= size(block)
        left_lines -= len(block)
    tail = [overflow.replace(f"{total} of", f"{total - count} of")] if count < total else []
    return "\n".join([*head, *body, *tail, *references]) + "\n"


def budget(late=""):
    """What survives every delivered tail, given output that may land after the digest."""
    merged = MERGED_TAIL_BYTES - len(late.encode("utf-8", "replace")) - STREAM_LABEL_BYTES
    return max(MIN_BYTES, min(MAX_BYTES, STREAM_TAIL_BYTES, merged))


def status_head(names, status, code, tool_code=None, reason=None, not_verified=()):
    exit_text = f"exit {code}" if tool_code in (None, code) else f"exit {code} (tool exit {tool_code})"
    head = [MARKER, f"Request {', '.join(names) or 'verification'}: {status}, {exit_text}."]
    if reason:
        head.append(f"Reason: {clean(str(reason))}")
    if not_verified:
        listed = ", ".join(not_verified[:8])
        more = len(not_verified) - 8
        head.append(f"Not verified: {listed}" + (f" (+{more} more)" if more > 0 else ""))
    return head


def request_digest(receipt, path, directory, code, *, status=None, reason=None, late=""):
    found = targets(receipt, directory)
    settled = {target["name"].split(" ", 1)[0] for target in found}
    not_verified = [name for name in receipt.get("unverified_stages", []) if name not in settled]
    head = status_head(receipt.get("names", []), status or receipt.get("status", "unverified"), code,
                       receipt.get("exit_code"), reason or receipt.get("reason"), not_verified)
    log = next((target["result"]["log"] for target in found if target["result"].get("log")),
               str(Path(directory) / "batch.log"))
    return compose(head, found, [f"Receipt: {path}", f"Full log: {log}"], budget(late))


def invocation_digest(name, code, *, status, reason=None):
    """Work outside a shared batch has no receipt; its own output above this block is the log."""
    return compose(status_head([name], status, code, reason=reason), [],
                   ["Full log: this command's output above; no receipt is written outside a shared batch."], budget())


def job_digest(directory, result):
    """A waiter gets the block its job printed, or what the job result alone establishes."""
    log = Path(directory) / "output.log"
    try:
        with log.open("rb") as stream:
            stream.seek(0, 2)
            stream.seek(max(0, stream.tell() - JOB_TAIL_BYTES))
            text = stream.read().decode("utf-8", "replace")
    except OSError:
        text = ""
    start = 0 if text.startswith(MARKER) else text.rfind("\n" + MARKER) + 1
    if text.startswith(MARKER) or start:
        return text[start:]
    status = "unverified" if result.get("state") != "completed" or result["exit_code"] != 1 else "failed"
    head = status_head(["background job " + Path(directory).name], status, result["exit_code"],
                       result.get("task_exit_code"), result.get("reason"))
    return compose(head, [], [f"Full log: {log}"], budget())


def deliver(text, stream=None):
    """The caller's result stands even if nobody is left to read this."""
    stream = stream if stream is not None else sys.stdout
    try:
        stream.buffer.write(text.encode("utf-8", "replace"))
        stream.buffer.flush()
    except OSError:
        pass
