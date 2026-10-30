"""Select one monotonic channel plan and re-admit its retained observations before effects."""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import re

from release_semver import compare, parse
from .document_schema import exact, require

FIELDS = {'schema_version', 'source_commit', 'version', 'channel', 'signing_generation', 'channels', 'controls'}


def decision(channel, version, controls):
    require(all(row is None or isinstance(row, dict) and isinstance(row.get('version'), str)
                for row in controls.values()), 'channel plan requires structured retained feed controls')
    current = controls[channel]
    require(current is None or compare(parse(version), parse(current['version'])) >= 0,
            'channel plan cannot regress its primary feed')
    selected = [channel]
    previous = controls.get('preview')
    if channel == 'stable' and (previous is None or compare(parse(previous['version']), parse(version)) <= 0):
        selected.append('preview')
    return selected


def select(channel, version, generation, source, *, read=None):
    from release_distribution import read_storage
    read = read or read_storage
    require(isinstance(version, str) and channel in {'stable', 'preview'}
            and parse(version).channel == channel and isinstance(source, str) and re.fullmatch('[a-f0-9]{40}', source)
            and type(generation) is int and generation > 0, 'channel plan requires exact release identities')
    retained = read(f'release-metadata/{version}/channel-plan.json')
    if retained is not None:
        validate(retained, source, version, channel, generation)
        check(retained, read=read)
        return retained
    controls = {name: read(f'updates/{name}/key-{generation}/latest.json')
                for name in (['stable', 'preview'] if channel == 'stable' else ['preview'])}
    channels = decision(channel, version, controls)
    return {'schema_version': 1, 'source_commit': source, 'version': version, 'channel': channel,
            'signing_generation': generation, 'channels': channels, 'controls': controls}


def validate(plan, source, version, channel, generation):
    exact(plan, FIELDS)
    require(type(plan['schema_version']) is int and plan['schema_version'] == 1
            and plan['source_commit'] == source and plan['version'] == version and plan['channel'] == channel
            and type(plan['signing_generation']) is int and plan['signing_generation'] == generation and type(generation) is int and generation > 0
            and isinstance(source, str) and re.fullmatch('[a-f0-9]{40}', source)
            and parse(version).channel == channel, 'channel plan differs from the selected release')
    require(isinstance(plan['controls'], dict)
            and set(plan['controls']) == ({'stable', 'preview'} if channel == 'stable' else {'preview'}),
            'channel plan lacks its exact retained observations')
    require(plan['channels'] == decision(channel, version, plan['controls']),
            'channel plan differs from its retained monotonic decision')
    return plan


def check(plan, *, read=None):
    from release_distribution import read_storage
    read = read or read_storage
    validate(plan, plan['source_commit'], plan['version'], plan['channel'], plan['signing_generation'])
    from .ledger import require_publishable
    require_publishable(plan['version'], read=read)
    for channel, observed in plan['controls'].items():
        actual = read(f"updates/{channel}/key-{plan['signing_generation']}/latest.json")
        if actual != observed:
            immutable = read(f"updates/releases/{plan['version']}.json")
            same = lambda row: {key: value for key, value in row.items() if key != 'pub_date'}
            require(channel in plan['channels'] and isinstance(actual, dict) and isinstance(immutable, dict)
                    and actual.get('version') == plan['version'] and immutable.get('version') == plan['version']
                    and same(actual) == same(immutable), 'channel controls changed after release selection')
    return True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('operation', choices=('select', 'check'))
    parser.add_argument('--file', type=Path, required=True)
    parser.add_argument('--source')
    parser.add_argument('--version')
    parser.add_argument('--channel', choices=('stable', 'preview'))
    parser.add_argument('--generation', type=int)
    args = parser.parse_args()
    if args.operation == 'select':
        args.file.write_text(json.dumps(select(args.channel, args.version, args.generation, args.source), indent=2) + '\n')
    else:
        check(json.loads(args.file.read_bytes()))


if __name__ == '__main__':
    main()
