import contextlib
import io
import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch
import yaml
import capture
import local_runtime


class LocalRuntimeTests(unittest.TestCase):
    def response(self, value):
        return contextlib.closing(io.BytesIO(json.dumps(value).encode()))

    def test_model_identity_records_defaults_without_disclosing_private_text(self):
        tags = {'models': [{'name': 'qwen:latest', 'digest': 'a'*64}]}
        show = {'details': {'format': 'gguf', 'family': 'qwen35', 'parameter_size': '27B', 'quantization_level': 'Q4_K_M'},
                'parameters': 'temperature 1\ntop_k 20\nstop "private stop string"', 'system': 'private system', 'template': 'template'}
        with patch.object(local_runtime.urllib.request, 'urlopen', side_effect=[self.response(value) for value in
                          [tags, show, {'version': '0.33.3'}, tags]]) as request:
            runtime = local_runtime.ollama_model('http://private-server:11434/v1', 'qwen:latest')
        self.assertEqual(runtime['model_digest'], 'a'*64)
        self.assertEqual(runtime['sampler_defaults'], {'temperature': 1, 'top_k': 20})
        self.assertNotIn('private', json.dumps(runtime))
        self.assertEqual(request.call_args_list[1].args[0].full_url, 'http://private-server:11434/api/show')
        changed = {'models': [{'name': 'qwen:latest', 'digest': 'b'*64}]}
        with patch.object(local_runtime.urllib.request, 'urlopen', side_effect=[self.response(value) for value in
                          [tags, show, {'version': '0.33.3'}, changed]]):
            with self.assertRaisesRegex(ValueError, 'changed while capturing'):
                local_runtime.ollama_model('http://private-server:11434', 'qwen:latest')

    def test_capture_hides_endpoint_and_verification_rejects_model_changes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            providers = {'providers': [{'id': 'local', 'kind': 'ollama', 'base_url': 'http://private-server:11434',
                                        'models': [{'id': 'qwen:latest'}]},
                                       {'id': 'cloud', 'kind': 'fixture', 'base_url': 'https://example.com/v1'}]}
            hosted = {'provider_id': 'cloud', 'model': 'worker'}
            policy = {'coordinator': {'provider_id': 'local', 'model': 'qwen:latest'}, 'lite': hosted,
                      'agent_pool': {'models': [hosted], 'selection': 'first'}}
            (root / 'providers.local.yaml').write_text(yaml.safe_dump(providers))
            (root / 'model-policy.yaml').write_text(yaml.safe_dump(policy))
            (root / 'engine').write_bytes(b'engine')
            (root / 'catalog').mkdir()
            runtime = {'model_digest': 'a'*64}
            with patch.object(capture, 'ollama_model', return_value=runtime):
                config = capture.describe(root, root / 'engine', root / 'catalog')
            self.assertIsNone(config['coordinator']['endpoint'])
            self.assertNotIn('private-server', json.dumps(config))
            with patch.object(local_runtime, 'ollama_model', return_value=runtime):
                self.assertEqual(local_runtime.verify_runtime(config, root), local_runtime.runtime_identity(config))
            with patch.object(local_runtime, 'ollama_model', return_value={'model_digest': 'b'*64}):
                with self.assertRaisesRegex(ValueError, 'differs from the frozen plan'):
                    local_runtime.verify_runtime(config, root)


if __name__ == '__main__':
    unittest.main()
