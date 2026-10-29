"""Private executor completion facts, separate from descendant-writable result files."""

import hashlib
import json
import os
from pathlib import Path

from verification_state import read_optional, write_json


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def sealed_receipt(directory, receipt):
    seal = read_optional(Path(directory) / (receipt["request"] + ".seal")) or {}
    return (seal.get("source_commit") == receipt.get("source_commit")
            and seal.get("digest") == digest(receipt))


class Publisher:
    def __init__(self, source):
        descriptor = os.environ.pop("PW_TEST_COMPLETION_FD", None)
        self.file = os.fdopen(int(descriptor), "w") if descriptor else None
        if self.file is not None:
            # Stage subprocesses receive only their declared lease descriptors.
            os.set_inheritable(self.file.fileno(), False)
        self.source = source

    def send(self, kind, **facts):
        if self.file is not None:
            self.file.write(json.dumps({"kind": kind, "source_commit": self.source, **facts}) + "\n")
            self.file.flush()

    def receipt(self, value):
        self.send("request_complete", request=value["request"], digest=digest(value))

    def finish(self, code):
        self.send("executor_complete", exit_code=code)
        if self.file is not None:
            self.file.close()


class Witness:
    """Only the supervisor reads this pipe; request files alone cannot establish completion."""

    def __init__(self, directory=None):
        self.directory = Path(directory) if directory is not None else None
        self.reader, self.writer = os.pipe()
        os.set_blocking(self.reader, False)
        self.pending = b""
        self.requests = {}
        self.final = None
        self.source = None

    def launched(self, source):
        self.source = source
        os.close(self.writer)
        self.writer = None

    def drain(self):
        while True:
            try:
                data = os.read(self.reader, 65536)
            except BlockingIOError:
                break
            if not data:
                break
            self.pending += data
            while b"\n" in self.pending:
                line, self.pending = self.pending.split(b"\n", 1)
                value = json.loads(line)
                if value.get("source_commit") != self.source:
                    raise ValueError("Executor completion source identity changed")
                if value.get("kind") == "request_complete":
                    self.requests[value["request"]] = value["digest"]
                    if self.directory is not None:
                        write_json(self.directory / (value["request"] + ".seal"),
                                   {"source_commit": self.source, "digest": value["digest"]})
                elif value.get("kind") == "executor_complete":
                    self.final = value
            if len(self.pending) > 65536:
                raise ValueError("Executor completion record exceeded its bound")

    def sealed(self, receipt):
        return (receipt.get("source_commit") == self.source
                and self.requests.get(receipt.get("request")) == digest(receipt))

    def completed(self, code):
        return self.final is not None and self.final.get("exit_code") == code and code in {0, 1}

    def close(self):
        os.close(self.reader)
        if self.writer is not None:
            os.close(self.writer)
