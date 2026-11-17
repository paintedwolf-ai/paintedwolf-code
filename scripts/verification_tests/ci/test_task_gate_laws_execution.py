"""The check gate runs the drift, vulnerability and generation checks, and those tasks never skip."""
import unittest

from verification_plan import expand
from verification_tests.ci.workflows import taskfile

GATED = ("codegen:detection-packs:check", "codegen:wire-enums:check", "db:sqlc:check", "lint:vuln")
# Their inputs change without any repository file changing, or their outputs are not enumerable.
UNFINGERPRINTED = ("lint:vuln", "codegen:wire-enums", "codegen:wire-enums:check")


class TaskGateLaws(unittest.TestCase):
    def test_the_check_gate_runs_every_drift_and_vulnerability_check_and_no_active_fuzzing(self):
        stages = {stage["name"] for stage in expand(["check"])}
        for name in GATED:
            self.assertIn(name, taskfile(), name)
            self.assertIn(name, stages, name)
        self.assertNotIn("test:fuzz", stages)

    def test_tasks_without_file_inputs_declare_no_fingerprint_task_could_skip_on(self):
        for name in UNFINGERPRINTED:
            task = taskfile()[name]
            self.assertEqual([key for key in ("sources", "generates", "status") if task.get(key)], [], name)


if __name__ == "__main__":
    unittest.main()
