"""Every student tier converts to a checkpoint mlx-lm loads strictly and runs.

Each tier is a tiny model with that tier's architecture, built by mlx-lm and written in
the Hugging Face layout a merged student arrives in: fp32 shards with an index, fused
expert projections, K/V projections on KV-shared layers, and a vision tower.
"""

import json
from pathlib import Path
import tempfile
import unittest

import mlx.core as mx
import numpy as np
from safetensors.numpy import save_file

from convert_mlx import convert, source_identity

try:
    from mlx.utils import tree_flatten
    from mlx_lm.utils import _get_classes, load_model
except ImportError:
    load_model = None

ROPE = {"full_attention": {"partial_rotary_factor": 0.25, "rope_theta": 1000000.0, "rope_type": "proportional"},
        "sliding_attention": {"rope_theta": 10000.0, "rope_type": "default"}}
BASE = {"hidden_size": 128, "intermediate_size": 256, "num_attention_heads": 2, "num_key_value_heads": 1, "head_dim": 64,
        "global_head_dim": 128, "vocab_size": 256, "vocab_size_per_layer_input": 256, "sliding_window": 8,
        "use_double_wide_mlp": False, "rope_parameters": ROPE, "final_logit_softcapping": 30.0, "tie_word_embeddings": True,
        "layer_types": ["sliding_attention", "sliding_attention", "sliding_attention", "full_attention"], "num_hidden_layers": 4}
# Tiny models with each pinned tier's architecture.
TIERS = {
    "e4b": ("gemma4", dict(BASE, model_type="gemma4_text", hidden_size_per_layer_input=64, num_kv_shared_layers=2)),
    "12b": ("gemma4_unified", dict(BASE, model_type="gemma4_unified_text", hidden_size_per_layer_input=0, num_kv_shared_layers=0,
                                   attention_k_eq_v=True, num_global_key_value_heads=1)),
    "26b-a4b": ("gemma4", dict(BASE, model_type="gemma4_text", hidden_size_per_layer_input=0, num_kv_shared_layers=0,
                               attention_k_eq_v=True, num_global_key_value_heads=1, enable_moe_block=True, num_experts=4,
                               top_k_experts=2, moe_intermediate_size=128)),
}
PROFILE_KEYS = ("num_hidden_layers", "hidden_size", "hidden_size_per_layer_input", "num_kv_shared_layers", "enable_moe_block")


def profiles():
    return {tier: {key: text.get(key, False if key == "enable_moe_block" else None) for key in PROFILE_KEYS}
            for tier, (_, text) in TIERS.items()}


def hugging_face_tensors(parameters, text):
    """mlx-lm parameters renamed and fused back into a merged checkpoint's layout."""
    out = {}
    for name, value in parameters.items():
        name = name.replace("language_model.model.", "model.language_model.")
        if ".experts.switch_glu." in name:
            continue
        out[name] = np.array(value.astype(mx.float32))
    for name, value in parameters.items():
        if name.endswith(".experts.switch_glu.gate_proj.weight"):
            base = name.replace("language_model.model.", "model.language_model.").removesuffix(".switch_glu.gate_proj.weight")
            up = parameters[name.replace("gate_proj", "up_proj")]
            out[base + ".gate_up_proj"] = np.concatenate([np.array(value.astype(mx.float32)), np.array(up.astype(mx.float32))], axis=-2)
            out[base + ".down_proj"] = np.array(parameters[name.replace("gate_proj", "down_proj")].astype(mx.float32))
    first_shared = text["num_hidden_layers"] - text.get("num_kv_shared_layers", 0)
    for layer in range(first_shared, text["num_hidden_layers"]):
        prefix = "model.language_model.layers.%d.self_attn." % layer
        out[prefix + "k_proj.weight"] = np.ones((text["head_dim"], text["hidden_size"]), dtype=np.float32)
        out[prefix + "v_proj.weight"] = np.ones((text["head_dim"], text["hidden_size"]), dtype=np.float32)
        out[prefix + "k_norm.weight"] = np.ones((text["head_dim"],), dtype=np.float32)
    out["model.vision_tower.patch_embedder.input_proj.weight"] = np.ones((64, 64), dtype=np.float32)
    return out


def write_checkpoint(directory, model_type, text, tensors):
    directory.mkdir()
    names = sorted(tensors)
    shards = [names[0::2], names[1::2]]
    weight_map = {}
    for n, shard in enumerate(shards, 1):
        file = "model-%05d-of-00002.safetensors" % n
        save_file({name: tensors[name] for name in shard}, directory / file)
        weight_map.update({name: file for name in shard})
    (directory / "model.safetensors.index.json").write_text(json.dumps({"metadata": {}, "weight_map": weight_map}))
    (directory / "config.json").write_text(json.dumps({"model_type": model_type, "text_config": text, "tie_word_embeddings": True}))
    for name in ("tokenizer.json", "tokenizer_config.json", "generation_config.json"):
        (directory / name).write_text("{}")
    (directory / "chat_template.jinja").write_text("{{ messages }}")


@unittest.skipIf(load_model is None, "mlx-lm is installed with the Apple silicon reference")
class StudentTierLoadTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        mx.set_default_device(mx.cpu)
        mx.random.seed(11)

    def test_every_tier_loads_strictly_and_runs_after_conversion(self):
        for tier, (model_type, text) in TIERS.items():
            with self.subTest(tier=tier):
                config = {"model_type": model_type, "text_config": dict(text)}
                model_class, args_class = _get_classes(config)
                model = model_class(args_class.from_dict(config))
                parameters = dict(tree_flatten(model.parameters()))
                source = self.root / ("source-" + tier)
                write_checkpoint(source, model_type, dict(text), hugging_face_tensors(parameters, text))
                output = self.root / ("mlx-" + tier)
                record = convert(source, output, source_identity(source), profiles=profiles())
                self.assertEqual(record["profile"], tier)
                loaded, loaded_config = load_model(output, strict=True)
                logits = loaded(mx.array([[1, 2, 3, 4, 5]]))
                mx.eval(logits)
                self.assertTrue(np.isfinite(np.array(logits.astype(mx.float32))).all())
                self.assertEqual(loaded_config["quantization"]["bits"], 4)
                if text.get("enable_moe_block"):
                    router = loaded_config["quantization"]["language_model.model.layers.0.router.proj"]
                    self.assertEqual(router["bits"], 8)
                    gate = parameters["language_model.model.layers.0.experts.switch_glu.gate_proj.weight"]
                    converted = mx.load(str(output / "model.safetensors"))
                    base = "language_model.model.layers.0.experts.switch_glu.gate_proj"
                    restored = mx.dequantize(converted[base + ".weight"], converted[base + ".scales"], converted[base + ".biases"],
                                             group_size=64, bits=4)
                    self.assertLess(float(mx.abs(restored.astype(mx.float32) - gate.astype(mx.float32)).max()), 0.05)


class LayoutTests(unittest.TestCase):
    """Real quantization on the split experts, without mlx-lm, so it runs on Linux too."""

    def test_split_experts_dequantize_to_their_halves(self):
        mx.set_default_device(mx.cpu)
        rng = np.random.default_rng(3)
        fused = (rng.standard_normal((4, 256, 128)) * 0.02).astype(np.float32)
        text = {"num_hidden_layers": 1, "hidden_size": 128, "hidden_size_per_layer_input": 0, "num_kv_shared_layers": 0,
                "enable_moe_block": True}
        with tempfile.TemporaryDirectory() as temporary:
            source = Path(temporary) / "source"
            source.mkdir()
            save_file({"model.language_model.layers.0.experts.gate_up_proj": fused}, source / "model.safetensors")
            (source / "config.json").write_text(json.dumps({"text_config": text}))
            for name in ("tokenizer.json", "tokenizer_config.json", "generation_config.json", "chat_template.jinja"):
                (source / name).write_text("{}")
            output = Path(temporary) / "mlx"
            convert(source, output, source_identity(source), profiles={"moe": text})
            converted = mx.load(str(output / "model.safetensors"))
            for half, part in ((fused[:, :128], "gate_proj"), (fused[:, 128:], "up_proj")):
                base = "language_model.model.layers.0.experts.switch_glu." + part
                restored = mx.dequantize(converted[base + ".weight"], converted[base + ".scales"], converted[base + ".biases"],
                                         group_size=64, bits=4)
                self.assertLess(float(np.abs(np.array(restored.astype(mx.float32)) - half).max()), 0.01)


if __name__ == "__main__":
    unittest.main()
