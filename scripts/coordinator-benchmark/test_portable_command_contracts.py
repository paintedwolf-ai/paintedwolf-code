import copy
import pathlib
import tempfile
import unittest
from portable_command_contracts import bytes_value, fixture_path, load_suite, materialize, validate_suite


class PortableCommandContracts(unittest.TestCase):
    def test_catalog_closure_and_oar_required_cases(self):
        suite = load_suite()
        self.assertEqual(len(suite['cases']), len(suite['required_cases']))
        self.assertTrue({'first-mutation', 'same-file-transform', 'browser-obligation', 'partial-write-unknown-http'} <= set(suite['required_cases']))
        self.assertEqual({q for c in suite['cases'] for q in c['qualification_cases']}, {f'Q{i:02}' for i in range(1,23)})
        self.assertTrue(all('expected_outcome' in t for c in suite['cases'] for t in c['turns']))

    def test_refuse_missing_duplicate_unbounded_and_nonbyte_contracts(self):
        for mutation in ('missing', 'duplicate', 'profile', 'generator', 'bytes'):
            altered = copy.deepcopy(load_suite())
            if mutation == 'missing': altered['cases'].pop()
            if mutation == 'duplicate': altered['cases'].append(altered['cases'][0])
            if mutation == 'profile': altered['cases'][0]['profile'] = 'unconfined'
            if mutation == 'generator': altered['cases'][0]['setup']['generators'] = [{'kind':'numbered-files','count':100000000,'directory':'tree'}]
            if mutation == 'bytes': altered['cases'][0]['expected']['stdout'] = {'base64':'!!'}
            with self.subTest(mutation=mutation), self.assertRaises(ValueError): validate_suite(altered)
        for name in ('../escape', '/synthetic/absolute', '.', 'x\\y', None):
            with self.subTest(name=name), self.assertRaises(ValueError): fixture_path(name)

    def test_materialization_is_repeatable_binary_safe_and_never_executes_programs(self):
        suite = load_suite()
        case = copy.deepcopy(suite['cases'][0])
        case['setup']['files'] = {'-leading option;$(marker)': {'base64':'AP8K'}, 'dir/space name': 'text\n'}
        case['turns'][0]['program'] = 'external -- forbidden-to-run'
        with tempfile.TemporaryDirectory() as temporary:
            first, second = pathlib.Path(temporary)/'first', pathlib.Path(temporary)/'second'
            self.assertEqual(materialize(case, first), materialize(case, second))
            self.assertEqual((first/'-leading option;$(marker)').read_bytes(), b'\0\xff\n')
            with self.assertRaises(ValueError): materialize(case, first)
        self.assertEqual(bytes_value('hello'), b'hello')
