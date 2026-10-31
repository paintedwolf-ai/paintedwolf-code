"""Capture local model identity and clean up owned launcher process groups."""
import hashlib
import json
import math
import os
import re
import signal
import subprocess
import time
import urllib.request
import yaml
from snapshot import identity

SAMPLERS = {'temperature', 'top_k', 'top_p', 'min_p', 'repeat_penalty', 'presence_penalty',
            'frequency_penalty', 'num_ctx', 'num_predict', 'seed', 'repeat_last_n', 'draft_num_predict'}


def live_members(pgid):
    if pgid <= 1:
        raise ValueError('cleanup requires an explicit process group')
    rows = subprocess.check_output(['ps', '-axo', 'pgid=,stat='], text=True)
    live = False
    for row in rows.splitlines():
        group, state = row.split()
        if int(group) == pgid and state[0] != 'Z':
            live = True
    return live


def signal_group(pgid, sig):
    if pgid <= 1:
        raise ValueError('cleanup requires an explicit process group')
    try:
        os.killpg(pgid, sig)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        # Darwin may reject signals to a group containing only zombies.
        if live_members(pgid):
            raise
        return False


def stop_process(process, grace_seconds=30, kill_wait_seconds=30):
    if not signal_group(process.pid, signal.SIGTERM):
        return process.wait()
    deadline = time.monotonic() + grace_seconds
    while live_members(process.pid):
        process.poll()
        if time.monotonic() >= deadline:
            signal_group(process.pid, signal.SIGKILL)
            deadline = time.monotonic() + kill_wait_seconds
            while live_members(process.pid):
                process.poll()
                if time.monotonic() >= deadline:
                    raise TimeoutError('owned process group remains live after forced cleanup')
                time.sleep(0.05)
            break
        time.sleep(0.05)
    return process.wait()


def ollama_model(endpoint, model):
    base = endpoint.rstrip('/').removesuffix('/v1')
    def request(path, body=None):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(base + path, data=data, headers={'Content-Type': 'application/json'})
        with urllib.request.urlopen(req, timeout=15) as response:
            return json.load(response)
    def digest():
        entry = next(m for m in request('/api/tags')['models'] if m['name'] == model)
        value = entry['digest']
        if not re.fullmatch('[a-f0-9]{64}', value):
            raise ValueError('Ollama returned an invalid model digest')
        return value
    before = digest()
    show = request('/api/show', {'model': model})
    version = request('/api/version')['version']
    if digest() != before:
        raise ValueError('Ollama model changed while capturing its configuration')
    defaults = {}
    for line in show.get('parameters', '').splitlines():
        parts = line.split(maxsplit=1)
        if len(parts) == 2 and parts[0] in SAMPLERS:
            value = json.loads(parts[1])
            if type(value) not in (int, float) or not math.isfinite(value):
                raise ValueError('Ollama returned an invalid sampler setting')
            defaults[parts[0]] = value
    result = {'model_digest': before, 'server_version': version, 'sampler_defaults': defaults,
              **{key: show['details'][key] for key in ['format', 'family', 'parameter_size', 'quantization_level']}}
    for key in ['parameters', 'template', 'system']:
        result[key + '_sha256'] = hashlib.sha256(show.get(key, '').encode()).hexdigest()
    return result


def local_models(configuration):
    models = [configuration['coordinator'], configuration['lite'], *configuration['workers']]
    return {m['provider'] + '/' + m['model']: m for m in models if 'local_runtime' in m}


def runtime_identity(configuration):
    models = local_models(configuration)
    return identity({key: m['local_runtime'] for key, m in models.items()}) if models else None


def verify_runtime(configuration, directory):
    models = local_models(configuration)
    if not models:
        return None
    providers = {p['id']: p for p in yaml.safe_load((directory / 'providers.local.yaml').read_text())['providers']}
    for model in models.values():
        actual = ollama_model(providers[model['provider']]['base_url'], model['model'])
        if actual != model['local_runtime']:
            raise ValueError('local model or server configuration differs from the frozen plan')
    return runtime_identity(configuration)
