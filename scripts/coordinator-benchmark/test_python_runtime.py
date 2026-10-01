import pathlib
import subprocess
import sys
import tempfile
import unittest
import venv
from unittest.mock import patch
import python_runtime


class PythonRuntimeTests(unittest.TestCase):
    def test_identity_survives_venv_handoffs_and_changes_with_requirements(self):
        with tempfile.TemporaryDirectory() as directory:
            environment = pathlib.Path(directory) / 'runtime'
            venv.EnvBuilder(with_pip=False).create(environment)
            script = 'import python_runtime; print(python_runtime.runtime_identity(b"requirements"))'
            result = subprocess.run([str(environment/'bin/python3'), '-c', script],
                                    cwd=pathlib.Path(python_runtime.__file__).parent, check=True, capture_output=True, text=True)
            self.assertEqual(result.stdout.strip(), python_runtime.runtime_identity(b'requirements'))
            self.assertNotEqual(python_runtime.runtime_identity(b'requirements'), python_runtime.runtime_identity(b'changed'))
            with patch.object(sys, 'executable', '/different/invoking/venv/bin/python3'):
                self.assertEqual(result.stdout.strip(), python_runtime.runtime_identity(b'requirements'))
