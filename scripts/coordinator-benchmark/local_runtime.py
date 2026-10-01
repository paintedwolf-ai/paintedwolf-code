"""Capture local model identity without publishing private server addresses."""
import hashlib
import json
import math
import re
import urllib.request
import yaml
from snapshot import identity

SAMPLERS = {'temperature', 'top_k', 'top_p', 'min_p', 'repeat_penalty', 'presence_penalty',
            'frequency_penalty', 'num_ctx', 'num_predict', 'seed', 'repeat_last_n', 'draft_num_predict'}


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
