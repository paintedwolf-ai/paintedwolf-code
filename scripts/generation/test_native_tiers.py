"""native_tiers.json states exactly the tiers the native runtime's own checks accept.

Each backend refuses an artifact whose architecture is not the one it implements: the
MLX engine in `mlx_engine/config.rs`, the Vulkan adapter in `vulkan/artifact.cpp`. This
reads the dimensions those checks require and holds every pinned student architecture
to them, so the declared tiers cannot drift from what the runtime loads.
"""
import ast
import json
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]
NATIVE = ROOT / "lycaon/internal/generation/native"
# The converter's architecture fact behind each dimension a backend checks.
MLX_FACTS = {"num_hidden_layers": "num_hidden_layers", "hidden_size": "hidden_size",
             "hidden_size_per_layer_input": "hidden_size_per_layer_input", "num_kv_shared_layers": "num_kv_shared_layers"}
VULKAN_FACTS = {"gemma4.block_count": "num_hidden_layers", "gemma4.embedding_length": "hidden_size",
                "gemma4.embedding_length_per_layer_input": "hidden_size_per_layer_input",
                "gemma4.attention.shared_kv_layers": "num_kv_shared_layers"}


def profiles():
    """The pinned student architectures, read from the converter without importing MLX."""
    tree = ast.parse((ROOT / "scripts/generation/convert_mlx.py").read_text())
    (value,) = [node.value for node in tree.body if isinstance(node, ast.Assign) and node.targets[0].id == "PROFILES"]
    return ast.literal_eval(value)


def accepted(required, facts):
    """The tiers whose architecture has every dimension a backend requires."""
    missing = set(facts) - set(required)
    if missing:
        raise AssertionError("the runtime no longer checks %s; restate how it decides a tier" % sorted(missing))
    return sorted(tier for tier, profile in profiles().items()
                  if not profile["enable_moe_block"] and all(profile[fact] == required[name] for name, fact in facts.items()))


class NativeTiers(unittest.TestCase):
    def setUp(self):
        self.declared = json.loads((ROOT / "scripts/generation/native_tiers.json").read_text())

    def test_mlx_tiers_are_the_ones_the_engine_accepts(self):
        source = (NATIVE / "src/mlx_engine/config.rs").read_text()
        required = {name: int(value) for name, value in re.findall(r'\("([a-z_]+)",\s*(\d+)\)', source)}
        self.assertIn('text["enable_moe_block"] != false', source)
        self.assertEqual(sorted(self.declared["mlx"]), accepted(required, MLX_FACTS))

    def test_vulkan_tiers_are_the_ones_the_adapter_accepts(self):
        source = (NATIVE / "vulkan/artifact.cpp").read_text()
        required = {name: int(value) for name, value in re.findall(r'\{"(gemma4\.[a-z_.]+)",\s*(\d+)u\}', source)}
        self.assertEqual(sorted(self.declared["vulkan"]), accepted(required, VULKAN_FACTS))

    def test_a_tier_is_accepted_only_with_every_required_dimension(self):
        e4b = profiles()["e4b"]
        required = {name: e4b[fact] for name, fact in MLX_FACTS.items()}
        self.assertEqual(accepted(required, MLX_FACTS), ["e4b"])
        self.assertEqual(accepted({**required, "num_kv_shared_layers": 0}, MLX_FACTS), [])
        twelve = profiles()["12b"]
        self.assertEqual(accepted({name: twelve[fact] for name, fact in MLX_FACTS.items()}, MLX_FACTS), ["12b"])
        with self.assertRaisesRegex(AssertionError, "no longer checks"):
            accepted({"hidden_size": 2560}, MLX_FACTS)


if __name__ == "__main__":
    unittest.main()
