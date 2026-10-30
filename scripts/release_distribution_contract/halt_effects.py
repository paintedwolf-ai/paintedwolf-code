"""Retain the complete halt preparation barrier and each operation owner's selected actions."""
import argparse
import json
import os
from pathlib import Path

from .halt_plan import validate
from .document import snapshot
from .document_schema import require

FILES = {'selection': 'halt-distribution.json', 'native': 'linux-withdrawal.json', 'ledger': 'halt-ledger.json',
         'feeds': 'prepared-halt/prepared.json', 'nix': 'nix-withdrawal.json', 'distribution': 'withdrawal.json'}


def admit(directory, outcomes):
    require(isinstance(outcomes, dict) and set(outcomes) == set(FILES), 'halt barrier requires every preparation outcome')
    evidence, values = {}, {}
    for owner, relative in FILES.items():
        try:
            require(outcomes[owner] == 'success', 'operation owner did not complete preparation')
            path = directory / relative
            captured, raw = snapshot(path, retain=True, limit=16 << 20)
            values[owner] = json.loads(raw)
            evidence[owner] = {'sha256': captured['sha256'], 'actions': []}
        except (OSError, ValueError) as error:
            refusal = {'action': 'refuse', 'code': 'preparation_incomplete', 'reason': str(error)}
            diagnostic = directory / ('halt-' + owner + '-refusal.log')
            if diagnostic.is_file():
                try:
                    _, raw = snapshot(diagnostic, retain=True, limit=65536)
                    refusal['diagnostic'] = raw.decode('utf-8', errors='replace')
                except (OSError, ValueError):
                    refusal['diagnostic'] = 'preparation diagnostic could not be captured within its bound'
            evidence[owner] = {'actions': [refusal]}
    selected = None
    if 'selection' in values:
        try:
            selected = validate(values['selection'])
            versions = set(selected['bad_versions'])
            channels = {row['channel']: row['bad'] for row in selected['channels']}
            evidence['selection']['actions'] = [{'channel': row['channel'], 'action': 'selected', 'version': row['bad']}
                                                for row in selected['channels']]
        except (KeyError, TypeError, ValueError) as error:
            selected = None
            evidence['selection']['actions'] = [{'action': 'refuse', 'code': 'invalid_selection', 'reason': str(error)}]
    for owner in ('native', 'ledger', 'feeds', 'nix', 'distribution'):
        if owner not in values:
            continue
        if selected is None:
            evidence[owner]['actions'] = [{'action': 'refuse', 'code': 'invalid_selection', 'reason': 'shared selection did not prepare'}]
            continue
        try:
            value = values[owner]
            if owner == 'native':
                rows = value
                require(isinstance(rows, list) and all(row['channel'] in channels for row in rows),
                        'native preparation differs from selected channels')
                actions = [{key: row[key] for key in ('action', 'family', 'channel', 'architecture')} for row in rows]
            elif owner == 'ledger':
                require({row['version'] for row in value} == versions, 'negative ledger omits selected versions')
                actions = value
            elif owner == 'feeds':
                require(value['distribution'] == selected and value['ledger'] == values.get('ledger'),
                        'feed preparation differs from shared plan or negative ledger')
                actions = value['actions']
            elif owner == 'nix':
                require(set(value['bad_versions']) == versions, 'Nix preparation differs from selected versions')
                actions = [{key: row[key] for key in ('version', 'system', 'action')} for row in value['rows']]
            else:
                require(set(value['bad_versions']) == versions and {row['channel'] for row in value['channels']} == set(channels),
                        'package and website preparation differs from selected channels')
                actions = [{'effect': effect, 'channel': row['channel'], 'action': row[key]}
                           for row in value['channels'] for effect, key in (('tap', 'tap_action'), ('website', 'website_action'))]
                actions += [{'effect': 'mirror', 'version': row['version'], 'action': row['action']} for row in value['mirrors']]
            require(all(row['action'] in {'withdraw', 'keep'} for row in actions), 'operation owner returned an unsupported action')
            evidence[owner]['actions'] = actions
        except (KeyError, TypeError, ValueError) as error:
            evidence[owner]['actions'] = [{'action': 'refuse', 'code': 'inconsistent_preparation', 'reason': str(error)}]
    complete = all(outcomes[owner] == 'success' for owner in FILES) and not any(
        row['action'] == 'refuse' for record in evidence.values() for row in record['actions'])
    return {'schema_version': 1, 'complete': complete, 'effects': evidence}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--directory', type=Path, required=True)
    args = parser.parse_args()
    result = admit(args.directory, json.loads(os.environ['HALT_PREPARATION_OUTCOMES']))
    (args.directory / 'halt-effects.json').write_text(json.dumps(result, indent=2) + '\n')
    if not result['complete']:
        parser.exit(1, 'halt preparation refused; no effect may apply; see halt-effects.json\n')


if __name__ == '__main__':
    main()
