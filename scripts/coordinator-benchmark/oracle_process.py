"""Bound a grader subprocess's output without letting candidate output fill host memory."""
import json
import re
import selectors
import subprocess
import time


def capture(command, timeout=30, output_limit=1024*1024):
    process=subprocess.Popen(command,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    output={'stdout':bytearray(),'stderr':bytearray()}
    deadline=time.monotonic()+timeout
    selector=selectors.DefaultSelector()
    try:
        selector.register(process.stdout,selectors.EVENT_READ,'stdout')
        selector.register(process.stderr,selectors.EVENT_READ,'stderr')
        while selector.get_map():
            remaining=deadline-time.monotonic()
            if remaining<=0: raise subprocess.TimeoutExpired(command,timeout)
            for key,_ in selector.select(remaining):
                chunk=key.fileobj.read1(8192)
                if not chunk:
                    selector.unregister(key.fileobj);continue
                output[key.data].extend(chunk)
                if sum(len(body) for body in output.values())>output_limit:
                    return None
        remaining=deadline-time.monotonic()
        if remaining<=0: raise subprocess.TimeoutExpired(command,timeout)
        code=process.wait(timeout=remaining)
        return subprocess.CompletedProcess(command,code,bytes(output['stdout']),bytes(output['stderr']))
    finally:
        selector.close()
        if process.poll() is None: process.kill()
        process.wait()
        process.stdout.close();process.stderr.close()


def remove_container(container):
    if not re.fullmatch('[a-f0-9]{64}', container):
        raise ValueError('invalid oracle container ownership receipt')
    removed = subprocess.run(['docker', 'rm', '--force', container], capture_output=True, timeout=30)
    if removed.returncode == 0:
        return True
    listed = subprocess.run(['docker', 'container', 'ls', '--all', '--no-trunc',
                             '--filter', 'id=' + container, '--format', '{{json .ID}}'],
                            capture_output=True, timeout=30)
    if listed.returncode != 0:
        return False
    identities = [json.loads(line) for line in listed.stdout.splitlines()]
    return container not in identities
