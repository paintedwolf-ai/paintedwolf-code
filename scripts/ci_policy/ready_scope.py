"""Select the fast tier's phases without dropping checks."""
from pathlib import PurePosixPath
import re
from types import SimpleNamespace

from verification_plan import catalog


def coverage_partition(changed, metadata, exclude):
    from coverage_changes import go_touched
    touched = go_touched(changed, metadata, exclude)
    return {'./' + PurePosixPath(metadata[package]['dir']).relative_to('lycaon').as_posix()
            for package in touched if metadata[package]['tested']}


def check_phases(targets, scope, early):
    """Native admission, then the early targets, then the remaining checks."""
    from ci_policy.native_admission import commands as native_commands
    first = [target for target in early if target in targets]
    rest = [target for target in targets if target not in first]
    return [*native_commands(scope), *[['./task', *part] for part in (first, rest) if part]]


def test_phases(scope):
    """Repository contracts, then the affected short tests that changed coverage does not already run."""
    from ci_policy.impact import go_scope
    contracts = ['./task', 'test:contract', '--', './test/contract/...']
    if scope is None:
        return [contracts, ['./task', 'test:short']], None
    from coverage_changes import go_changed_files
    packages = go_scope(scope)
    recipe = catalog()['go']['test:short']
    exclude = re.compile(recipe['exclude'])
    covered = set()
    changed = go_changed_files(SimpleNamespace(paths=scope.get('paths', [])))
    # Only this recipe is identical to the short tests coverage actually runs.
    if changed and recipe['options'] == []:
        from coverage_changes import go_packages
        metadata = go_packages()
        if packages is None:
            packages = sorted('./' + PurePosixPath(info['dir']).relative_to('lycaon').as_posix()
                              for info in metadata.values())
        covered = coverage_partition(changed, metadata, exclude)
    if packages is None:
        return [contracts, ['./task', 'test:short']], None
    packages = [package for package in packages if not exclude.search('/' + package.removeprefix('./'))]
    remaining = [package for package in packages if package not in covered]
    return [contracts, *([['./task', 'test:short', '--', *remaining]] if remaining else [])], packages
