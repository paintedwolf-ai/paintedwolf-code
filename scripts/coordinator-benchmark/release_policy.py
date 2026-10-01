"""Evaluate an explicitly selected target against an optional benchmark cadence."""
import argparse
import json
import pathlib
import sys

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))
from release_semver import parse


def decision(version, cadence='selected'):
    parsed = parse(version)
    if cadence not in {'selected', 'major-minor'}:
        raise ValueError('unknown benchmark cadence')
    if cadence == 'selected':
        reason = 'selected_target'
    elif parsed.prerelease:
        reason = 'prerelease'
    elif parsed.patch:
        reason = 'patch_release'
    else:
        reason = 'major_or_minor_release'
    return {'version': version, 'cadence': cadence,
            'eligible': reason in {'selected_target', 'major_or_minor_release'}, 'reason': reason}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', help='SemVer without a tag prefix; defaults to repository VERSION')
    parser.add_argument('--cadence', choices=['selected', 'major-minor'], default='selected')
    args = parser.parse_args()
    version = args.version or (pathlib.Path(__file__).resolve().parents[2] / 'VERSION').read_text().strip()
    try:
        print(json.dumps(decision(version, args.cadence)))
    except ValueError as exc:
        parser.error(str(exc))
