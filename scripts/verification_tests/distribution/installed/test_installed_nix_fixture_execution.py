"""Fixed derivative and root observer contracts for installed Nix lifetimes."""
from __future__ import annotations

import copy
from pathlib import Path
import unittest

from scripts.linux_release.installed_cases import nix_fixture as fixture


def selected():
    token, builder = 'c' * 32, '/nix/store/' + 'a' * 32 + '-fixture-tools/bin/busybox'
    name = 'paintedwolf-nix-cancel-' + token
    drv, output = '/nix/store/' + 'd' * 32 + '-' + name + '.drv', '/nix/store/' + 'e' * 32 + '-' + name
    arguments = ['sh', '-c', fixture.cancellation_script(builder, token)]
    record = {'system': 'x86_64-linux', 'static_builder': {'path': builder}, 'cancellation': {
        'derivation': drv, 'derivation_sha256': 'f' * 64, 'output': output, 'token': token, 'arguments': arguments}}
    row = {'version': 4, 'system': record['system'], 'builder': builder, 'args': arguments,
           'env': {'allowSubstitutes': '', 'builder': builder, 'name': name, 'out': output,
                   'preferLocalBuild': '1', 'system': record['system']},
           'inputs': {'drvs': {}, 'srcs': [Path(builder).parts[3]]},
           'outputs': {'out': {'path': Path(output).name}}}
    return record, {'version': 4, 'derivations': {Path(drv).name: row}}


class InstalledNixFixtureTests(unittest.TestCase):
    def test_exact_tiny_derivative_is_admitted(self):
        record, graph = selected()
        self.assertEqual(fixture.derivation(graph, record), record['cancellation'])
        self.assertIn('sleep 300', record['cancellation']['arguments'][-1])
        self.assertIn('fixture_token=' + record['cancellation']['token'], record['cancellation']['arguments'][-1])

    def test_foreign_builder_outputs_arguments_and_impure_environment_are_refused(self):
        for field, value in [('builder', '/bin/sh'), ('args', ['sh', '-c', 'uncontrolled']),
                             ('system', 'aarch64-linux'), ('outputs', {}),
                             ('inputs', {'drvs': {}, 'srcs': []})]:
            with self.subTest(field=field):
                record, graph = selected()
                row = next(iter(graph['derivations'].values()))
                row[field] = value
                with self.assertRaises(ValueError):
                    fixture.derivation(graph, record)
        record, graph = selected()
        next(iter(graph['derivations'].values()))['env']['__noChroot'] = '1'
        with self.assertRaises(ValueError):
            fixture.derivation(graph, record)
        record, graph = selected()
        graph['derivations']['another.drv'] = copy.deepcopy(next(iter(graph['derivations'].values())))
        with self.assertRaises(ValueError):
            fixture.derivation(graph, record)

    def test_observer_is_fixed_and_does_not_enumerate_unrelated_processes(self):
        compile(fixture.OBSERVER, '<fixed-nix-observer>', 'exec')
        self.assertIn("'nix-daemon.service'", fixture.OBSERVER)
        self.assertIn("'task',str(current),'children'", fixture.OBSERVER)
        self.assertIn("'start_ticks'", fixture.OBSERVER)
        self.assertIn("lease.open('x')", fixture.OBSERVER)
        self.assertNotIn("Path('/proc').iterdir", fixture.OBSERVER)
        self.assertNotIn('/environ', fixture.OBSERVER)
        self.assertNotIn('killpg', fixture.OBSERVER)
        with self.assertRaises(ValueError):
            fixture.observe({}, 'arbitrary-root-command')


if __name__ == '__main__':
    unittest.main()
