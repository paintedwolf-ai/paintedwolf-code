"""Exercise generation packaging through its isolated fixture suite."""

from pathlib import Path
import subprocess
import sys
import unittest


class GenerationStagingTests(unittest.TestCase):
    def test_authenticated_runtime_and_seed_staging(self):
        result = subprocess.run([sys.executable, "-m", "unittest", "test_staging"],
                                cwd=Path(__file__).parent / "generation",
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_build_and_measurement_admission_serialize_native_publication(self):
        import verification_plan as planning
        measured = set(planning.invocation_profile(["generation:bench"])["locks"])
        for target in ("build:lycaon-dev", "upgrade:corpus:seed", "upgrade:corpus:boot", "generation:build"):
            built = set(planning.invocation_profile([target])["locks"])
            self.assertIn("generation-runtime", measured & built, target)
            self.assertIn("rust", built, target)
