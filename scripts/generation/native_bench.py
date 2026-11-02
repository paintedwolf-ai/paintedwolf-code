"""Measure the production runner over its bounded command protocol."""

import argparse
import json
import os
import resource
import select
import struct
import subprocess
import sys
import time
from pathlib import Path

MAX_FRAME = 8 << 20


class Client:
    def __init__(self, process):
        self.process = process
        self.sequence = 0

    def read(self, length, deadline):
        chunks = bytearray()
        while len(chunks) < length:
            remaining = deadline - time.monotonic()
            if remaining <= 0 or not select.select([self.process.stdout], [], [], remaining)[0]:
                raise TimeoutError("native runner response deadline exceeded")
            block = os.read(self.process.stdout.fileno(), length - len(chunks))
            if not block:
                raise RuntimeError("native runner closed its response stream")
            chunks.extend(block)
        return bytes(chunks)

    def call(self, method, **fields):
        self.sequence += 1
        payload = json.dumps({"version": 2, "id": self.sequence,
                              "command": {"method": method, **fields}}).encode()
        if not 0 < len(payload) <= MAX_FRAME:
            raise ValueError("benchmark request exceeds frame bound")
        deadline = time.monotonic() + 300
        pending = memoryview(struct.pack("<I", len(payload)) + payload)
        while pending:
            remaining = deadline - time.monotonic()
            if remaining <= 0 or not select.select([], [self.process.stdin], [], remaining)[1]:
                raise TimeoutError("native runner request deadline exceeded")
            written = os.write(self.process.stdin.fileno(), pending[:4096])
            if written == 0:
                raise RuntimeError("native runner closed its request stream")
            pending = pending[written:]
        length, = struct.unpack("<I", self.read(4, deadline))
        if not 0 < length <= MAX_FRAME:
            raise ValueError("native runner response exceeds frame bound")
        response = json.loads(self.read(length, deadline))
        if response.get("id") != self.sequence or response.get("status") != "ok":
            raise RuntimeError(f"native runner rejected {method}: {response}")
        return response["result"]


def trial(client, specification, request_id, checkpoint):
    prompt = specification["prompt_tokens"]
    started = time.perf_counter()
    begun = client.call("begin", request={
        "request_id": request_id, "prompt_tokens": prompt,
        "output_contract": {"allowed_tools": specification.get("allowed_tools", [])},
        "max_new_tokens": specification["generation_tokens"],
        "temperature": 0, "top_p": 1, "top_k": 0, "seed": 0,
        "checkpoint_tokens": checkpoint,
    })
    first_token = None
    tokens, steps = [], []
    while True:
        step_start = time.perf_counter()
        result = client.call("advance", request_id=request_id)
        finished = time.perf_counter()
        progress = result["progress"]
        steps.append({"seconds": finished - step_start, **progress})
        if progress["generated_tokens"] and first_token is None:
            first_token = finished
        if progress["token"] is not None:
            tokens.append(progress["token"])
        if progress["stop_reason"] is not None:
            break
    generated = progress["generated_tokens"]
    prefill_seconds = first_token - started if first_token else None
    decode_seconds = finished - first_token if first_token else None
    return {
        "request_id": request_id, "reused_prefix_tokens": begun["reused_prefix_tokens"],
        "prompt_tokens": len(prompt), "generated_tokens": generated,
        "stop_reason": progress["stop_reason"], "tokens": tokens,
        "prefill_seconds": prefill_seconds, "decode_seconds": decode_seconds,
        "prefill_tokens_per_second": (len(prompt) - begun["reused_prefix_tokens"]) / prefill_seconds if prefill_seconds else None,
        "decode_tokens_per_second": (generated - 1) / decode_seconds if generated > 1 and decode_seconds else None,
        "steps": steps,
    }


def measure(args):
    specification = json.loads(args.input.read_text())
    if not 0 <= args.checkpoint_tokens < len(specification["prompt_tokens"]):
        raise ValueError("checkpoint must precede the final prompt token")
    context = len(specification["prompt_tokens"]) + specification["generation_tokens"]
    command = [str(args.binary), "--model", str(args.model), "--artifact-id", args.artifact_identity]
    if args.metallib:
        command += ["--metallib", str(args.metallib)]
    if args.device_pci:
        command += ["--device-pci", args.device_pci]
    process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, bufsize=0)
    client = Client(process)
    try:
        hello = client.call("hello")
        plan = client.call("inspect", context_tokens=context, prefill_chunk=specification["prefill_chunk"])["plan"]
        started = time.perf_counter()
        client.call("load", context_tokens=context, prefill_chunk=specification["prefill_chunk"],
                    device_budget_bytes=plan["device_reservation_bytes"],
                    host_budget_bytes=plan["host_reservation_bytes"])
        print(json.dumps({"hello": hello, "plan": plan, "load_seconds": time.perf_counter() - started}), flush=True)
        for index in range(specification["trials"] + 1):
            result = trial(client, specification, index + 1, args.checkpoint_tokens)
            print(json.dumps({"warmup": index == 0, **result}), flush=True)
        client.call("unload")
        client.call("shutdown")
        if process.wait(timeout=30) != 0:
            raise RuntimeError("native runner exited unsuccessfully")
        usage = resource.getrusage(resource.RUSAGE_CHILDREN)
        print(json.dumps({"process_usage": {
            "peak_rss_bytes": usage.ru_maxrss * (1 if sys.platform == "darwin" else 1024),
            "major_page_faults": usage.ru_majflt, "minor_page_faults": usage.ru_minflt,
            "block_inputs": usage.ru_inblock, "block_outputs": usage.ru_oublock,
            "user_seconds": usage.ru_utime, "system_seconds": usage.ru_stime,
            "scope": "child process accounting; RSS does not measure discrete GPU allocation",
        }}), flush=True)
    finally:
        if process.poll() is None:
            process.kill()
        process.wait()
        process.stdin.close()
        process.stdout.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("binary", "model", "input"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--artifact-identity", required=True)
    parser.add_argument("--metallib", type=Path)
    parser.add_argument("--device-pci")
    parser.add_argument("--checkpoint-tokens", type=int, default=0)
    measure(parser.parse_args())
