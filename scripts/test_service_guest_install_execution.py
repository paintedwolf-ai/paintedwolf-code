import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

with patch.object(sys, "path", [str(Path(__file__).parent / "service-guest"), *sys.path]):
    import assemble


class ServiceGuestInstallation(unittest.TestCase):
    def test_staging_retains_the_selected_install_after_a_failed_replacement(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            output = root / "resources/service-guest"
            args = ["assemble", "--system", "linux", "--arch", "amd64", "--cache",
                    str(root / "cache"), "--output", str(output)]
            def build(args, work, stage):
                (stage / "manifest.json").write_text('{"schema":1}')
                (stage / "kernel").write_bytes(b"selected kernel")

            rename = Path.rename
            def refuse_stage(path, destination):
                if path.name == "stage":
                    raise OSError("staged image could not be installed")
                return rename(path, destination)

            with patch.object(sys, "argv", args), patch.object(assemble, "assemble", side_effect=build):
                assemble.main()
                (output / "kernel").write_bytes(b"previous kernel")
                with patch.object(Path, "rename", refuse_stage), self.assertRaises(OSError):
                    assemble.main()
                self.assertEqual((output / "kernel").read_bytes(), b"previous kernel")
                assemble.main()
                self.assertEqual((output / "kernel").read_bytes(), b"selected kernel")
                (output / "manifest.json").unlink()
                with self.assertRaisesRegex(ValueError, "unrecognized guest staging directory"):
                    assemble.main()
                self.assertEqual((output / "kernel").read_bytes(), b"selected kernel")
