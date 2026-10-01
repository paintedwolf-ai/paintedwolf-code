"""Record only non-secret, result-affecting application settings."""
import argparse
import hashlib
import json
import pathlib
import platform
import yaml
from local_runtime import ollama_model
from snapshot import file_hash, RUNTIME_METADATA
from progress import save

MODEL_CONTROLS = ('reasoning_effort', 'max_tokens', 'temperature', 'context_length', 'think_style', 'thinking_always_on')


def digest(path):
    if path.is_symlink() or not path.is_dir():
        raise ValueError('catalog identity requires a regular directory: ' + str(path))
    h = hashlib.sha256()
    for p in sorted(path.rglob('*')):
        if p.name in RUNTIME_METADATA:
            continue
        if p.is_symlink():
            raise ValueError('catalog identity requires regular files: ' + str(p.relative_to(path)))
        if p.is_file():
            h.update(str(p.relative_to(path)).encode() + b'\0' + p.read_bytes() + b'\0')
        elif not p.is_dir():
            raise ValueError('catalog identity requires regular files: ' + str(p.relative_to(path)))
    return h.hexdigest()


def verify_capture_files(run, configuration):
    engine = run / 'engine'
    if engine.is_symlink() or not engine.is_file() or file_hash(engine) != configuration['engine_sha256']:
        raise ValueError('captured engine differs from the admitted application')
    if digest(run / 'module/config') != configuration['catalog_sha256']:
        raise ValueError('captured catalog differs from the admitted application')


def hosted_runtime(endpoint, model):
    catalog = json.loads(pathlib.Path(__file__).with_name('hosted-models.json').read_text())
    if catalog['version'] != 1:
        raise ValueError('unsupported hosted model disclosure catalog')
    matches = [entry for entry in catalog['models'] if entry['endpoint'] == endpoint.rstrip('/') and entry['model'] == model]
    if len(matches) > 1:
        raise ValueError('duplicate hosted model disclosure')
    return ({key:matches[0][key] for key in ['precision','weights_revision','source','observed_at']} if matches else
            {'precision':None,'weights_revision':None,'source':None,'observed_at':None})


def describe(config, engine, catalog, time_limit="0s", application=None):
    providers = yaml.safe_load((config / 'providers.local.yaml').read_text())['providers']
    policy = yaml.safe_load((config / 'model-policy.yaml').read_text())
    def model(ref):
        provider = next(p for p in providers if p['id'] == ref['provider_id'])
        entry = next((m for m in provider.get('models', []) if m['id'] == ref['model']), {})
        local = provider['kind'] == 'ollama'
        return {'provider': ref['provider_id'], 'provider_kind': provider['kind'],
                'endpoint': None if local else provider['base_url'], 'model': ref['model'],
                'configured_retry': {key: provider.get(key) for key in ['http_retry_profile', 'http_retry']},
                **({'local_runtime': ollama_model(provider['base_url'], ref['model'])} if local else
                   {'hosted_runtime': hosted_runtime(provider['base_url'], ref['model'])}),
                'configured_settings': {key: entry.get(key) for key in MODEL_CONTROLS}}
    result = {'request_control_policy': 'application', 'engine_sha256': file_hash(engine),
              'catalog_sha256': digest(catalog),
              'coordinator': model(policy['coordinator']), 'lite': model(policy['lite']),
              'workers': [model(ref) for ref in policy['agent_pool']['models']],
              'worker_selection': policy['agent_pool']['selection'], 'episode_time_limit': time_limit,
              'application_profile': 'development-harness',
              'command_path_policy': 'explicit-launcher-path',
              'platform': {'os': platform.system().lower(), 'architecture': platform.machine()}}
    if application is not None:
        result['application'] = application
    result['configuration_sha256'] = hashlib.sha256(json.dumps(result, sort_keys=True).encode()).hexdigest()
    return result


def validate_expected(result, plan):
    selected = result['coordinator']
    expected = [m['configuration'] for m in plan['models']
                if m['provider']==selected['provider'] and m['model']==selected['model']]
    if not expected or any(config != result for config in expected):
        fields = sorted({key for config in expected for key in config.keys() | result.keys()
                         if config.get(key) != result.get(key)}) if expected else ['coordinator']
        raise ValueError('captured configuration differs from the frozen plan; candidate execution refused: '
                         + ', '.join(fields))


def capture(run, config=None, time_limit='0s', application=None, expected_plan=None):
    try:
        result = describe(config or run / 'config', run / 'engine', run / 'module/config', time_limit, application)
        save(run / 'configuration.json', result)
        if expected_plan is not None:
            validate_expected(result, expected_plan)
    except (ValueError, OSError) as exc:
        save(run / 'setup-failure.json', {'operation': 'configuration_admission', 'failure': {
            'kind': 'harness', 'code': 'configuration_mismatch', 'retryable': False}, 'diagnostic': str(exc)})
        raise
    return result


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('run', type=pathlib.Path)
    p.add_argument('--time-limit', required=True)
    p.add_argument('--application', type=pathlib.Path)
    p.add_argument('--expected-plan', type=pathlib.Path)
    args = p.parse_args()
    capture(args.run, time_limit=args.time_limit,
            application=json.loads(args.application.read_text()) if args.application else None,
            expected_plan=json.loads(args.expected_plan.read_text()) if args.expected_plan else None)
