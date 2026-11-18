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


def go_change(scope):
    """Whether the change can alter what Go compiles; a scope without one is treated as such."""
    return scope is None or scope['full'] or any(
        path.endswith('.go') or path in {'lycaon/go.mod', 'lycaon/go.sum'} for path in scope.get('paths', []))


def check_phases(targets, scope, early):
    """Native admission, then the early targets, then the remaining checks; the cross-platform build only
    when Go can compile differently."""
    from ci_policy.native_admission import commands as native_commands
    if not go_change(scope):
        targets = [target for target in targets if target != 'build:cross']
    first = [target for target in early if target in targets]
    rest = [target for target in targets if target not in first]
    return [*native_commands(scope), *[['./task', *part] for part in (first, rest) if part]]


def test_phases(scope):
    """Repository contracts, the affected short tests that changed coverage does not already run, then the
    integration tests of the packages the change edits."""
    phases, packages = short_phases(scope)
    return [*phases, ['./task', 'test:integration:changes']], packages


def short_phases(scope):
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
