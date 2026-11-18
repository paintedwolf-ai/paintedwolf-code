"""Keep release probe tier names aligned with the converter. Runtime tests verify the geometries."""
import ast
import json
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]
NATIVE = ROOT / "lycaon/internal/generation/native"
def profiles():
    """The pinned student architectures, read from the converter without importing MLX."""
    tree = ast.parse((ROOT / "scripts/generation/convert_mlx.py").read_text())
    (value,) = [node.value for node in tree.body if isinstance(node, ast.Assign) and node.targets[0].id == "PROFILES"]
    return ast.literal_eval(value)


class NativeTiers(unittest.TestCase):
    def test_each_backend_accepts_the_exported_tiers(self):
        declared = json.loads((ROOT / "scripts/generation/native_tiers.json").read_text())
        for backend in ("mlx", "vulkan"):
            self.assertCountEqual(declared[backend], profiles())
