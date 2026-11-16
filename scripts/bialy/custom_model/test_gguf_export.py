"""GGUF inventory, interrupted export orchestration and the served checks, with fixture binaries."""
import argparse
import json
import os
from pathlib import Path
import shutil
import stat
import sys
import tempfile
import unittest
from unittest.mock import patch

from common import file_digest, inventory, write_new
from gguf_export import Server, convert, inspect_gguf
from rendering import TEMPLATE, template_sha256
import serving_check


def write_fixture(path, template):
    import gguf
    import numpy as np
    writer = gguf.GGUFWriter(path, "llama")
    writer.add_chat_template(template)
    writer.add_tensor("fixture.weight", np.ones((4,4),dtype=np.float32))
    writer.write_header_to_file()
    writer.write_kv_data_to_file()
    writer.write_tensors_to_file()
    writer.close()


def reference(completion):
    """A behaviour reference over the fixture server's tokenization, and what the server answers."""
    prompt = "<bos>hi"
    ids = [2, ord("h"), ord("i")]
    return ({"prompts": [{"name": "greeting", "prompt": prompt, "prompt_tokens": ids, "tokens": [7, 8, 9], "margins": [6.0, 5.0, 4.0]}]},
            {json.dumps(ids): completion})


class Export(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = root = Path(directory.name)
        self.template = TEMPLATE.read_text()
        (root/"merge/model").mkdir(parents=True)
        (root/"merge/model/config.json").write_text("{}")
        server = root/"server"
        server.write_text("#!%s\nimport runpy\nrunpy.run_path(%r, run_name='__main__')\n"
                          % (sys.executable, str(Path(__file__).with_name("fixture_llama_server.py"))))
        server.chmod(server.stat().st_mode | stat.S_IXUSR)
        (root/"quantizer").write_text("fixture binary")
        self.args = argparse.Namespace(merge=root/"merge",out=root/"out",llama_cpp=root/"llama",revision="a"*40,
                                       quantizer=root/"quantizer",server=server,quantization="Q4_K_M",resume=False)
        self.calls = []
        environment = patch.dict(os.environ, {"FIXTURE_TEMPLATE": str(TEMPLATE), "FIXTURE_COMPLETION": str(root/"completion.json")})
        environment.start()
        self.addCleanup(environment.stop)

    def merged(self, completion=None, template=None):
        manifest = {"files":inventory(self.root/"merge/model"), "template_sha256":template_sha256(template or self.template)}
        if completion is not None:
            expected, answers = reference(completion)
            write_new(self.root/"merge/behavior.json", expected)
            (self.root/"completion.json").write_text(json.dumps(answers))
            manifest["behavior_sha256"] = file_digest(self.root/"merge/behavior.json")
        write_new(self.root/"merge/manifest.json", manifest)

    def run_tool(self, command, **kwargs):
        if "--outfile" in command:
            self.calls.append("convert")
            write_fixture(Path(command[command.index("--outfile")+1]), self.template)
        else:
            self.calls.append("quantize")
            shutil.copyfile(command[1],command[2])

    def convert(self):
        with patch("gguf_export.checkout_identity",return_value="a"*40), patch("gguf_export.subprocess.run",side_effect=self.run_tool):
            return convert(self.args)

    def test_a_base_checkpoint_is_served_and_rendered_without_a_reference(self):
        self.merged()
        self.convert()
        manifest = json.loads((self.root/"out/manifest.json").read_text())
        self.assertEqual(manifest["schema"], "pw-custom-gguf/2")
        self.assertEqual(manifest["template_sha256"], template_sha256(self.template))
        self.assertIsNone(manifest["behavior_sha256"])
        for name in ("unquantized", "quantized"):
            self.assertIsNone(manifest["served"][name]["behavior"])
            self.assertEqual(manifest["served"][name]["rendering"]["template_sha256"], manifest["template_sha256"])
            self.assertIn("declared tools and parallel calls", manifest["served"][name]["rendering"]["cases"])
        self.assertEqual(len(inspect_gguf(self.root/"out/model-f16.gguf")["tensors"]),1)

    def test_an_artifact_that_reproduces_the_merge_records_its_agreement(self):
        self.merged(completion=[7, 8, 9])
        self.convert()
        manifest = json.loads((self.root/"out/manifest.json").read_text())
        self.assertEqual(manifest["behavior_sha256"], file_digest(self.root/"merge/behavior.json"))
        self.assertEqual(manifest["served"]["quantized"]["behavior"],
                         {"precision": "Q4_K_M", "tolerance": 2.0, "agreed_tokens": [3], "departures": []})
        self.assertEqual(manifest["served"]["unquantized"]["behavior"]["precision"], "F16")

    def test_an_artifact_that_generates_differently_is_refused_and_a_resume_reuses_the_conversions(self):
        self.merged(completion=[7, 1, 9])
        with self.assertRaisesRegex(ValueError, "beyond its tolerance"):
            self.convert()
        self.assertFalse((self.root/"out/manifest.json").exists())
        (self.root/"completion.json").write_text(json.dumps(reference([7, 8, 9])[1]))
        self.args.resume = True
        self.convert()
        self.convert()
        self.assertEqual(self.calls, ["convert", "quantize"])

    def test_a_changed_reference_or_another_template_is_refused(self):
        self.merged(completion=[7, 8, 9])
        (self.root/"merge/behavior.json").write_text("{}")
        with self.assertRaisesRegex(ValueError, "behaviour reference changed"):
            self.convert()
        shutil.rmtree(self.root/"out")
        (self.root/"merge/manifest.json").unlink()
        (self.root/"merge/behavior.json").unlink()
        self.merged(template="another template")
        with self.assertRaisesRegex(ValueError, "does not embed the template"):
            self.convert()

    def test_a_server_serving_another_template_or_conversation_is_refused(self):
        other = self.root/"other.jinja"
        other.write_text(self.template + "{# changed #}")
        with patch.dict(os.environ, {"FIXTURE_TEMPLATE": str(other)}), Server(self.args.server, "model", self.root/"server.log") as url:
            with self.assertRaisesRegex(ValueError, "does not serve the training chat template"):
                serving_check.check(url)
        with Server(self.args.server, "model", self.root/"server.log") as url:
            real = serving_check.call
            def trimmed(url, path, body=None):
                value = real(url, path, body)
                return {"prompt": value["prompt"].strip()} if path == "/apply-template" else value
            with self.assertRaisesRegex(ValueError, "renders a conversation differently from training"):
                serving_check.check(url, trimmed)
            def doubled(url, path, body=None):
                value = real(url, path, body)
                return {"tokens": [2, *value["tokens"]]} if path == "/tokenize" and body["add_special"] else value
            with self.assertRaisesRegex(ValueError, "exactly one BOS"):
                serving_check.check(url, doubled)
            with self.assertRaisesRegex(ValueError, "server refused /missing"):
                serving_check.call(url, "/missing", {})

    def test_a_server_that_exits_is_reported_with_its_log(self):
        with patch.dict(os.environ, {"FIXTURE_REFUSES_TO_LOAD": "1"}):
            with self.assertRaisesRegex(ValueError, "did not load the artifact"):
                Server(self.args.server, "model", self.root/"server.log").__enter__()


if __name__ == "__main__":
    unittest.main()
