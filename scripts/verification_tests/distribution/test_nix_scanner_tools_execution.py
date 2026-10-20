"""Scanner tools must belong to the selected complete native derivation graph."""
import copy
import unittest
from scripts.linux_release import nix_scanner_tools as host

ROOT = '/nix/store/' + '0' * 32 + '-application.drv'
TOOL = '/nix/store/' + '1' * 32 + '-' + host.PNAME + '-1.0.2.drv'
OUTPUT = '/nix/store/' + '2' * 32 + '-' + host.PNAME + '-1.0.2'
EXE = OUTPUT + '/bin/opengrep-artifact'


def graph():
    return {ROOT: {'system': 'aarch64-linux', 'env': {}, 'inputDrvs': {TOOL: ['out']}},
            TOOL: {'system': 'aarch64-linux', 'env': {'pname': host.PNAME, 'out': OUTPUT},
                   'inputDrvs': {}, 'outputs': {'out': {'path': OUTPUT}}}}


class NixScannerToolsTests(unittest.TestCase):
    def test_only_one_reachable_native_scanner_output_is_selected(self):
        self.assertEqual(host.scanner_derivation(graph(), ROOT, 'aarch64-linux'),
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
                host.scanner_derivation(rows, ROOT, 'aarch64-linux')

