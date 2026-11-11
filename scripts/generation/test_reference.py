"""Fixture contracts for bounded conversion and mapped PLE lookup."""

import json
from pathlib import Path
import tarfile
import tempfile
import unittest

import mlx.core as mx
import numpy as np
from safetensors.numpy import save_file

from convert_mlx import convert, digest
from mapped_ple import MappedPLE
from sources import unpack


class ReferenceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.source = self.root / "source"
        self.source.mkdir()
        self.output = self.root / "output"
        self.prefix = "language_model.model.embed_tokens_per_layer"
        self.values = np.arange(4 * 128, dtype=np.float32).reshape(4, 128) / 101
        save_file({"model.language_model.embed_tokens_per_layer.weight": self.values}, self.source / "model.safetensors")
        config = {"text_config": {"num_hidden_layers": 42, "hidden_size": 2560,
                                  "hidden_size_per_layer_input": 256, "num_kv_shared_layers": 18}}
        (self.source / "config.json").write_text(json.dumps(config))
        for name in ["tokenizer.json", "tokenizer_config.json", "generation_config.json", "chat_template.jinja"]:
            (self.source / name).write_text("{}")
        mx.set_default_device(mx.cpu)

    def converted(self):
        convert(self.source, self.output, digest(self.source / "model.safetensors"))
        return MappedPLE(self.output, self.prefix, 64, 4)

    def test_mapped_rows_match_resident_dequantization(self):
        mapped = self.converted()
        tensors = mx.load(str(self.output / "model.safetensors"))
        resident = mx.dequantize(tensors[self.prefix + ".weight"], tensors[self.prefix + ".scales"],
                                tensors[self.prefix + ".biases"], group_size=64, bits=4)
        ids = mx.array([[3, 1, 3, 0]])
        actual = mapped(ids)
        mx.eval(actual, resident)
        np.testing.assert_array_equal(np.array(actual), np.array(resident[ids]))
        packed, scales, biases = mx.quantize(mx.array(self.values.astype(np.float16)), group_size=64, bits=4)
        for name, expected in zip(["weight", "scales", "biases"], [packed, scales, biases]):
            np.testing.assert_array_equal(np.array(tensors[self.prefix + "." + name]), np.array(expected))
        self.assertEqual(mapped.parameters(), {})

    def test_mixed_precision_embeddings_preserve_selected_rows(self):
        for bits in (6, 8):
            with self.subTest(bits=bits):
                destination = self.root / f"precision-{bits}"
                precision = {"weight_bits": 4, "ple_bits": bits, "embedding_bits": 8}
                convert(self.source, destination, digest(self.source / "model.safetensors"), precision)
                mapped = MappedPLE(destination, self.prefix, 64, bits)
                packed, scales, biases = mx.quantize(mx.array(self.values.astype(np.float16)), group_size=64, bits=bits)
                reference = mx.dequantize(packed, scales, biases, group_size=64, bits=bits)
                ids = mx.array([[2, 0, 2]])
                np.testing.assert_array_equal(np.array(mapped(ids)), np.array(reference[ids]))
                config = json.loads((destination / "config.json").read_text())
                self.assertEqual(config["quantization"]["bits"], 4)
                self.assertEqual(config["quantization"][self.prefix]["bits"], bits)
                record = json.loads((destination / "conversion.json").read_text())
                self.assertEqual(record["precision"], precision)

    def test_mapped_lookup_rejects_unbounded_and_invalid_tokens(self):
        mapped = self.converted()
        for ids in [mx.array([[4]]), mx.array([[-1]]), mx.zeros((1, 257), dtype=mx.int32)]:
            with self.assertRaisesRegex(ValueError, "bounds"):
                mapped(ids)

    def test_conversion_refuses_changed_source_without_publishing(self):
        with self.assertRaisesRegex(ValueError, "digest mismatch"):
            convert(self.source, self.output, "0" * 64)
        self.assertFalse(self.output.exists())

    def test_nonfinite_conversion_removes_only_its_output(self):
        self.values[0, 0] = np.nan
        save_file({"model.language_model.embed_tokens_per_layer.weight": self.values}, self.source / "model.safetensors")
        with self.assertRaisesRegex(ValueError, "non-finite"):
            self.converted()
        self.assertFalse(self.output.exists())
        self.assertTrue((self.source / "model.safetensors").exists())

    def test_source_archive_rejects_escapes_and_links(self):
        for name, kind in [("root/../../escaped", tarfile.REGTYPE), ("root/link", tarfile.SYMTYPE)]:
            archive = self.root / "source.tar"
            with tarfile.open(archive, "w") as output:
                member = tarfile.TarInfo(name)
                member.type = kind
                member.linkname = "../../escaped"
                output.addfile(member)
            with self.assertRaisesRegex(ValueError, "unsafe"):
                unpack(archive, self.root / "unpacked")
        self.assertFalse((self.root / "escaped").exists())


if __name__ == "__main__":
    unittest.main()
