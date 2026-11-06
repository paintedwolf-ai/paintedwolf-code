"""Portable contracts for generation helpers that normally depend on MLX."""

import importlib.util
import json
from pathlib import Path
import struct
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

import numpy as np


ROOT = Path(__file__).resolve().parent


class Tensor:
    def __init__(self, value=0):
        self.value = value

    def __getitem__(self, _):
        return self

    def item(self):
        return self.value


class MLXFixture(unittest.TestCase):
    def setUp(self):
        self.core = types.ModuleType("mlx.core")
        self.core.cpu = object()
        self.core.array = lambda value: np.asarray(value)
        self.core.eval = lambda *values: None
        self.core.quantize = lambda value, group_size, bits: (
            np.zeros((value.shape[0], value.shape[1] * bits // 32), dtype=np.uint32),
            np.ones((value.shape[0], value.shape[1] // group_size), dtype=np.float16),
            np.zeros((value.shape[0], value.shape[1] // group_size), dtype=np.float16),
        )
        self.core.dequantize = lambda packed, _scales, _biases, **_: np.zeros(
            (packed.shape[0], packed.shape[1] * 32 // 4), dtype=np.float16)
        self.core.set_default_device = lambda _: None
        self.core.set_cache_limit = lambda _: None
        self.nn = types.ModuleType("mlx.nn")
        self.nn.Module = type("Module", (), {})
        self.mlx = types.ModuleType("mlx")
        self.mlx.__path__ = []
        self.mlx.core = self.core
        self.mlx.nn = self.nn
        self.modules = patch.dict(sys.modules, {
            "mlx": self.mlx, "mlx.core": self.core, "mlx.nn": self.nn,
        })
        self.modules.start()
        self.addCleanup(self.modules.stop)

    def load(self, name):
        path = ROOT / (name + ".py")
        spec = importlib.util.spec_from_file_location(name, path)
        module = importlib.util.module_from_spec(spec)
        sys.modules[name] = module
        self.addCleanup(lambda: sys.modules.pop(name, None))
        spec.loader.exec_module(module)
        return module

    @staticmethod
    def write_tensors(path, tensors):
        header, chunks, cursor = {}, [], 0
        for name, (dtype, array) in tensors.items():
            raw = array.tobytes()
            header[name] = {
                "dtype": dtype, "shape": list(array.shape),
                "data_offsets": [cursor, cursor + len(raw)],
            }
            chunks.append(raw)
            cursor += len(raw)
        encoded = json.dumps(header, separators=(",", ":")).encode()
        encoded += b" " * ((-len(encoded)) % 8)
        path.write_bytes(struct.pack("<Q", len(encoded)) + encoded + b"".join(chunks))


class ConversionTests(MLXFixture):
    def test_conversion_writes_bounded_quantized_artifact(self):
        converter = self.load("convert_mlx")
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "source"
            source.mkdir()
            self.write_tensors(source / "model.safetensors", {
                "model.language_model.embed_tokens_per_layer.weight":
                    ("F32", np.arange(128, dtype=np.float32).reshape(2, 64)),
            })
            config = {
                "text_config": {"num_hidden_layers": 42, "hidden_size": 2560,
                                "hidden_size_per_layer_input": 256, "num_kv_shared_layers": 18},
            }
            (source / "config.json").write_text(json.dumps(config))
            for name in ("tokenizer.json", "tokenizer_config.json", "chat_template.jinja", "generation_config.json"):
                (source / name).write_text("{}")
            output = root / "converted"
            converter.convert(source, output, converter.digest(source / "model.safetensors"))
            record = json.loads((output / "conversion.json").read_text())
            converted = (output / "model.safetensors").read_bytes()
            header_size, = struct.unpack("<Q", converted[:8])
            tensors = json.loads(converted[8:8 + header_size])
            self.assertEqual(record["schema"], "paintedwolf-mlx-conversion/1")
            self.assertEqual(json.loads((output / "config.json").read_text())["dtype"], "float16")
            self.assertIn("language_model.model.embed_tokens_per_layer.scales", tensors)
            self.assertEqual(record["precision"]["ple_bits"], 4)

    def test_conversion_rejects_bad_precision_digest_and_nonfinite_data(self):
        converter = self.load("convert_mlx")
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "source"
            source.mkdir()
            weights = source / "model.safetensors"
            self.write_tensors(weights, {
                "model.language_model.embed_tokens_per_layer.weight":
                    ("F32", np.full((1, 64), np.nan, dtype=np.float32)),
            })
            (source / "config.json").write_text(json.dumps({
                "text_config": {"num_hidden_layers": 42, "hidden_size": 2560,
                                "hidden_size_per_layer_input": 256, "num_kv_shared_layers": 18},
            }))
            for name in ("tokenizer.json", "tokenizer_config.json", "chat_template.jinja", "generation_config.json"):
                (source / name).write_text("{}")
            output = root / "converted"
            with self.assertRaisesRegex(ValueError, "precision requires"):
                converter.convert(source, output, "0" * 64, {"weight_bits": 5})
            with self.assertRaisesRegex(ValueError, "digest mismatch"):
                converter.convert(source, output, "0" * 64)
            with self.assertRaisesRegex(ValueError, "non-finite"):
                converter.convert(source, output, converter.digest(weights))
            self.assertFalse(output.exists())

    def test_mapped_ple_reads_rows_and_rejects_invalid_shapes_or_ids(self):
        mapped_module = self.load("mapped_ple")
        prefix = "language_model.model.embed_tokens_per_layer"
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.write_tensors(root / "model.safetensors", {
                prefix + ".weight": ("U32", np.zeros((2, 16), dtype="<u4")),
                prefix + ".scales": ("F16", np.ones((2, 2), dtype="<f2")),
                prefix + ".biases": ("F16", np.zeros((2, 2), dtype="<f2")),
            })
            mapped = mapped_module.MappedPLE(root, prefix, 64, 4)
            result = mapped(np.array([[1, 0]], dtype=np.int32))
            self.assertEqual(result.shape, (1, 2, 128))
            for ids in (np.array([[2]]), np.array([[-1]]), np.zeros((1, 257), dtype=np.int32)):
                with self.assertRaisesRegex(ValueError, "bounds"):
                    mapped(ids)
            with self.assertRaisesRegex(ValueError, "requires affine"):
                mapped_module.MappedPLE(root, prefix, 32, 4)


class BenchmarkHelperTests(MLXFixture):
    def test_mlx_benchmark_runs_with_deterministic_clock_and_reports_usage(self):
        cache_module = types.ModuleType("mlx_lm.models.cache")
        cache_module.make_prompt_cache = lambda _model: [types.SimpleNamespace(state=object())]
        class Model:
            language_model = types.SimpleNamespace(model=types.SimpleNamespace(embed_tokens_per_layer=None))
            def parameters(self):
                return {}
            def __call__(self, *_args, **_kwargs):
                return Tensor(1)

        mlx_lm = types.ModuleType("mlx_lm")
        mlx_lm.__path__ = []
        mlx_lm.load = lambda *_, **__: (
            Model(),
            object(),
            {"text_config": {"num_hidden_layers": 42, "hidden_size": 2560}},
        )
        mlx_lm.models = types.ModuleType("mlx_lm.models")
        mlx_lm.models.__path__ = []
        self.core.reset_peak_memory = lambda: None
        self.core.isfinite = lambda _: True
        self.core.all = lambda _: Tensor(True)
        self.core.min = lambda _: Tensor(-1)
        self.core.max = lambda _: Tensor(1)
        self.core.argmax = lambda *_args, **_kwargs: Tensor(7)
        self.core.get_peak_memory = lambda: 10
        self.core.get_active_memory = lambda: 8
        self.core.get_cache_memory = lambda: 2
        self.core.clear_cache = lambda: None
        mapped = types.ModuleType("mapped_ple")
        mapped.replace_ple = lambda *_: None
        usage = types.SimpleNamespace(ru_maxrss=10, ru_majflt=1, ru_minflt=2, ru_inblock=3)
        class Clock:
            current = 0.0
            def __call__(self):
                self.current += 0.25
                return self.current

        with patch.dict(sys.modules, {
            "mlx_lm": mlx_lm, "mlx_lm.models": mlx_lm.models,
            "mlx_lm.models.cache": cache_module, "mapped_ple": mapped,
        }):
            benchmark = self.load("mlx_bench")
            with tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                input_path = root / "input.json"
                input_path.write_text(json.dumps({
                    "prompt_tokens": [1, 2, 3], "prefill_chunk": 2,
                    "generation_tokens": 2, "trials": 1, "ple": "mapped",
                }))
                with patch.object(benchmark.time, "perf_counter", side_effect=Clock()), \
                        patch.object(benchmark.resource, "getrusage", return_value=usage), \
                        patch.object(sys, "argv", ["mlx_bench", "--model", str(root), "--input", str(input_path)]), \
                        patch("builtins.print") as output:
                    benchmark.main()
                self.assertEqual(output.call_count, 2)
                self.assertIn('"tokens": [7, 7]', output.call_args_list[-1].args[0])

    def test_prompt_renders_with_local_tokenizer_only(self):
        tokenizer = types.SimpleNamespace(
            apply_chat_template=lambda *_, **kwargs: [1, 2, 3])
        transformers = types.ModuleType("transformers")
        transformers.AutoTokenizer = types.SimpleNamespace(
            from_pretrained=lambda *args, **kwargs: tokenizer)
        with patch.dict(sys.modules, {"transformers": transformers}), tempfile.TemporaryDirectory() as temporary:
            prompt = self.load("prompt")
            root = Path(temporary)
            messages = root / "messages.json"
            output = root / "rendered.json"
            messages.write_text(json.dumps({"messages": [{"role": "user", "content": "hello"}]}))
            with patch.object(sys, "argv", ["prompt", str(root), str(messages), str(output)]):
                prompt.main()
            self.assertEqual(json.loads(output.read_text()), {"prompt_tokens": [1, 2, 3]})


if __name__ == "__main__":
    unittest.main()
