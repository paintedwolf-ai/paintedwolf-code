"""GGUF inventory and interrupted export orchestration with fixture binaries."""
import argparse
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch

from common import inventory, write_new
from gguf_export import convert, inspect_gguf


def write_fixture(path):
    import gguf
    import numpy as np
    writer = gguf.GGUFWriter(path, "llama")
    writer.add_chat_template("fixture template")
    writer.add_tensor("fixture.weight", np.ones((4,4),dtype=np.float32))
    writer.write_header_to_file()
    writer.write_kv_data_to_file()
    writer.write_tensors_to_file()
    writer.close()


class ExportRecovery(unittest.TestCase):
    def test_failed_load_reuses_verified_conversion_and_quantization(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root/"merge/model").mkdir(parents=True)
            (root/"merge/model/config.json").write_text("{}")
            write_new(root/"merge/manifest.json",{"files":inventory(root/"merge/model")})
            for name in ("quantizer","loader"):
                (root/name).write_text("fixture binary")
            args = argparse.Namespace(merge=root/"merge",out=root/"out",llama_cpp=root/"llama",
                                      revision="a"*40,quantizer=root/"quantizer",loader=root/"loader",quantization="Q4_K_M",resume=False)
            calls = []
            def run(command, **kwargs):
                if "--outfile" in command:
                    calls.append("convert")
                    write_fixture(Path(command[command.index("--outfile")+1]))
                elif command[0] == str(args.quantizer):
                    calls.append("quantize")
                    shutil.copyfile(command[1],command[2])
                else:
                    calls.append("load")
                    if calls.count("load") == 1:
                        raise RuntimeError("fixture interrupted load")
            with patch("gguf_export.checkout_identity",return_value="a"*40), patch("gguf_export.subprocess.run",side_effect=run):
                with self.assertRaisesRegex(RuntimeError,"interrupted"):
                    convert(args)
                args.resume = True
                convert(args)
                convert(args)
            self.assertEqual(calls,["convert","quantize","load","load"])
            self.assertEqual(len(inspect_gguf(root/"out/model-f16.gguf")["tensors"]),1)


if __name__ == "__main__":
    unittest.main()
