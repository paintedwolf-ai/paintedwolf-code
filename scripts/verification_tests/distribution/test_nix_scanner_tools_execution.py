"""Scanner tools must belong to the selected complete native derivation graph."""
import copy
import unittest
import json
from types import SimpleNamespace
from unittest.mock import patch
from scripts.linux_release import nix_scanner_tools as host

ROOT = '/nix/store/' + '0' * 32 + '-application.drv'
TOOL = '/nix/store/' + '1' * 32 + '-' + host.PNAME + '-1.0.2.drv'
OUTPUT = '/nix/store/' + '2' * 32 + '-' + host.PNAME + '-1.0.2'
EXE = OUTPUT + '/bin/opengrep-artifact'


def graph():
    return {ROOT: {'system': 'aarch64-linux', 'env': {}, 'inputDrvs': {TOOL: ['out']}},
            TOOL: {'system': 'aarch64-linux', 'env': {'pname': host.PNAME, 'out': OUTPUT},
                   'inputDrvs': {}, 'outputs': {'out': {'path': OUTPUT}}}}


def schema4(rows):
    result = {}
    for path, row in rows.items():
        row = copy.deepcopy(row)
        row['version'] = 4
        row['inputs'] = {'drvs': {key.removeprefix('/nix/store/'): value for key, value in row.pop('inputDrvs').items()}}
        for output in row.get('outputs', {}).values():
            output['path'] = output['path'].removeprefix('/nix/store/')
        result[path.removeprefix('/nix/store/')] = row
    return {'version': 4, 'derivations': result}


class NixScannerToolsTests(unittest.TestCase):
    def test_only_one_reachable_native_scanner_output_is_selected(self):
        self.assertEqual(host.scanner_derivation(schema4(graph()), ROOT, 'aarch64-linux'),
                         {'derivation': TOOL, 'output': OUTPUT, 'executable': EXE, 'system': 'aarch64-linux'})
        for kind in ('disconnected', 'missing', 'architecture', 'output', 'duplicate'):
            rows = graph()
            if kind == 'disconnected':
                rows[ROOT]['inputDrvs'] = {}
            elif kind == 'missing':
                del rows[TOOL]
            elif kind == 'architecture':
                rows[TOOL]['system'] = 'x86_64-linux'
            elif kind == 'output':
                rows[TOOL]['outputs']['out']['path'] = OUTPUT + '/escape'
            else:
                duplicate = TOOL.replace('1' * 32, '3' * 32)
                rows[ROOT]['inputDrvs'][duplicate] = ['out']
                rows[duplicate] = copy.deepcopy(rows[TOOL])
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                host.scanner_derivation(schema4(rows), ROOT, 'aarch64-linux')


    def test_preparation_queries_only_the_pinned_application_and_builds_its_selected_tool(self):
        source = 'git+file:///source?rev=' + 'a' * 40
        with patch.object(host.platform, 'machine', return_value='aarch64'), \
                patch.object(host.subprocess, 'run', side_effect=[SimpleNamespace(stdout=ROOT),
                    SimpleNamespace(stdout=json.dumps(schema4(graph())))]) as run:
            invocation, selected = host.command(['nix'], source, {'PATH': '/fixed'})
        self.assertEqual(run.call_args_list[0].args[0][-1], source + '#packages.aarch64-linux.default.drvPath')
        self.assertEqual(run.call_args_list[1].args[0][-1], ROOT)
        self.assertEqual(invocation[-1], TOOL + '^out')
        self.assertEqual(selected['executable'], EXE)
        self.assertIn('sandbox', invocation)
