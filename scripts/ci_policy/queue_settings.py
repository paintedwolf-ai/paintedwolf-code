"""Review/apply only merge-queue parameters, preserving every other ruleset field."""
import argparse
import json
from pathlib import Path
from .github import api, repository


def proposed(current):
    expected = json.loads(Path(__file__).with_name('queue-settings.json').read_text())
    body = {key: current[key] for key in ('name', 'target', 'enforcement', 'conditions', 'bypass_actors', 'rules')}
    body = json.loads(json.dumps(body))
    queues = [rule for rule in body['rules'] if rule['type'] == 'merge_queue']
    if len(queues) != 1:
        raise ValueError('expected exactly one merge queue rule')
    queues[0]['parameters'] = expected
    return body


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('ruleset', type=int)
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    path = f'{repository()}/rulesets/{args.ruleset}'
    current = api(path)
    body = proposed(current)
    print(json.dumps(body, indent=2))
    if args.apply:
        # Refuse a concurrent edit rather than overwrite another administrator.
        if api(path) != current:
            raise ValueError('ruleset changed while preparing update; review again')
        updated = api(path, 'PUT', body)
        if {key: updated[key] for key in body} != body:
            raise ValueError('ruleset response did not preserve the reviewed configuration')


if __name__ == '__main__':
    main()
