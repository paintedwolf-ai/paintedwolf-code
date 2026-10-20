"""Bounded single-inflight Bialy exchanges through the retained final executable."""
from __future__ import annotations

import json
import os
import select
import time

from .process import launch, LIMIT

LINE_LIMIT = 256 << 10


def exchange(binding: dict, arguments: list[str], requests: list[dict]) -> dict:
    if not 1 <= len(requests) <= 22:
        raise ValueError('installed Bialy exchange exceeds its fixed request bound')
    stdout, stderr, samples, responses = bytearray(), bytearray(), [], []
    with launch(binding, 'bialy', arguments, bialy_input=True) as (process, identity):
        pipes = {process.stdout.fileno(): stdout, process.stderr.fileno(): stderr}
        for pipe in [process.stdin, process.stdout, process.stderr]:
            os.set_blocking(pipe.fileno(), False)
        try:
            for index, request in enumerate(requests, 1):
                if not isinstance(request, dict) or 'id' in request:
                    raise ValueError('installed Bialy request must receive its owned protocol identity')
                encoded = json.dumps(dict(request, id=index), allow_nan=False).encode() + b'\n'
                if len(encoded) > LINE_LIMIT:
                    raise ValueError('installed Bialy request exceeds its line bound')
                started = time.monotonic()
                deadline = started + (120 if index == 1 else 15)
                pending, response = memoryview(encoded), None
                offset = len(stdout)
                while response is None:
                    remaining = deadline - time.monotonic()
                    if remaining <= 0:
                        raise ValueError('installed Bialy response exceeded its bounded deadline')
                    readable, writable, _ = select.select(list(pipes), [process.stdin.fileno()] if pending else [], [], remaining)
                    if pending and process.stdout.fileno() in readable:
                        raise ValueError('installed Bialy emitted output before its complete request')
                    if writable:
                        try:
                            pending = pending[os.write(process.stdin.fileno(), pending):]
                        except BrokenPipeError as error:
                            raise ValueError('installed Bialy exited before accepting its request') from error
                    for descriptor in readable:
                        block = os.read(descriptor, 64 << 10)
                        if not block:
                            pipes.pop(descriptor)
                            if descriptor == process.stdout.fileno():
                                raise ValueError('installed Bialy exited before its matching response')
                            continue
                        output = pipes[descriptor]
                        if len(output) + len(block) > LIMIT:
                            raise ValueError('installed Bialy evidence exceeds its aggregate bound')
                        output.extend(block)
                        if descriptor != process.stdout.fileno():
                            continue
                        line = bytes(stdout[offset:])
                        if len(line) > LINE_LIMIT:
                            raise ValueError('installed Bialy response exceeds its line bound')
                        if b'\n' in line:
                            if pending or line.count(b'\n') != 1 or not line.endswith(b'\n'):
                                raise ValueError('installed Bialy emitted unsolicited protocol output')
                            response = json.loads(line)
                            if (not isinstance(response, dict) or type(response.get('id')) is not int
                                    or response['id'] != index or 'error' in response):
                                raise ValueError('installed Bialy response differs from its owned request')
                samples.append((time.monotonic() - started) * 1000)
                responses.append(response)
            process.stdin.close()
            deadline = time.monotonic() + 5
            while pipes:
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise ValueError('installed Bialy retained evidence pipes after protocol completion')
                readable, _, _ = select.select(list(pipes), [], [], remaining)
                for descriptor in readable:
                    block = os.read(descriptor, 64 << 10)
                    if not block:
                        pipes.pop(descriptor)
                    elif descriptor == process.stdout.fileno():
                        raise ValueError('installed Bialy emitted output without an owned request')
                    elif len(stderr) + len(block) > LIMIT:
                        raise ValueError('installed Bialy evidence exceeds its aggregate bound')
                    else:
                        stderr.extend(block)
            process.wait(timeout=max(0.001, deadline - time.monotonic()))
            if process.returncode != 0:
                raise ValueError('installed Bialy exited unsuccessfully after its responses')
        finally:
            for pipe in [process.stdin, process.stdout, process.stderr]:
                pipe.close()
    return {'launch_identity': identity, 'roundtrip_ms': samples, 'responses': responses,
            'stdout': bytes(stdout), 'stderr': bytes(stderr)}
