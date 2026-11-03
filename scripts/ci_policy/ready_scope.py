"""Order ready feedback and partition identical short tests without dropping checks."""
from pathlib import PurePosixPath
import re
from types import SimpleNamespace

from verification_plan import catalog


def extract_targets(phases, names):
    selected, remaining = [], []
    for phase in phases:
        if '--' in phase:
            (selected if len(phase) > 1 and phase[1] in names else remaining).append(phase)
            continue
        take = [target for target in phase[1:] if target in names]
        keep = [target for target in phase[1:] if target not in names]
        if take:
            selected.append(['./task', *take])
        if keep:
            remaining.append(['./task', *keep])
    return selected, remaining


def coverage_partition(changed, metadata, exclude):
    from coverage_changes import go_touched
    touched = go_touched(changed, metadata, exclude)
    return {'./' + PurePosixPath(metadata[package]['dir']).relative_to('lycaon').as_posix()
            for package in touched if metadata[package]['tested']}


def commands(phases, scope, history=None, early=()):
    from ci_policy.impact import go_scope
    history = history or {}
    failed = set(history.get('failed_targets', [])) | set(history.get('native_failed', []))
    has_coverage = ('coverage:changes' in early
                    and any('coverage:changes' in phase[1:] for phase in phases))
    packages = go_scope(scope) if scope is not None else None
    metadata, covered = None, set()
    # Only this catalog recipe is identical to the short tests coverage actually runs.
    same_recipe = catalog()['go']['test:short']['options'] == []
    changed = []
    if scope is not None and has_coverage and same_recipe:
        from coverage_changes import go_changed_files
        changed = go_changed_files(SimpleNamespace(paths=scope.get('paths', [])))
    if scope is not None and (changed or history.get('failed_packages')):
        from coverage_changes import go_packages
        metadata = go_packages()
        if packages is None:
            packages = sorted('./' + PurePosixPath(info['dir']).relative_to('lycaon').as_posix()
                              for info in metadata.values())
    exclude = re.compile(catalog()['go']['test:short']['exclude'])
    if packages is not None:
        packages = [package for package in packages if not exclude.search('/' + package.removeprefix('./'))]
        if metadata is not None and has_coverage and same_recipe:
            covered = coverage_partition(changed, metadata, exclude)
    failed_packages = (list(dict.fromkeys(package for package in history.get('failed_packages', []) if package in packages))
                       if packages is not None else [])
    if failed_packages:
        failed.discard('test:short')
    elif 'test:short' in failed:
        # Without a package receipt, rerun the complete failed selection first.
        covered.clear()
    remaining_packages = ([package for package in packages if package not in covered and package not in failed_packages]
                          if packages is not None else None)
    selected = []
    for phase in phases:
        if 'test:short' not in phase or remaining_packages is None:
            selected.append(phase)
            continue
        index = phase.index('test:short')
        if index > 1:
            selected.append(phase[:index])
        if remaining_packages:
            selected.append(['./task', 'test:short', '--', *remaining_packages])
        if phase[index + 1:]:
            selected.append(['./task', *phase[index + 1:]])
    priority, selected = extract_targets(selected, failed)
    early_phases, selected = extract_targets(selected, set(early))
    early_names = [name for phase in early_phases for name in phase[1:]]
    if len(early_names) != len(set(early_names)):
        raise ValueError('ready early target occurs more than once')
    ordered_early = [['./task', *[name for name in early if name in early_names]]] if early_names else []
    priority_packages = [['./task', 'test:short', '--', *failed_packages]] if failed_packages else []
    return [*priority_packages, *priority, *ordered_early, *selected], packages
