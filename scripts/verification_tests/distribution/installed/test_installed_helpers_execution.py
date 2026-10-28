"""Installed observations remain bounded and incomplete interactions fail closed."""
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import zipfile

from scripts.linux_release.installed_cases.app_api import EngineAPI
from scripts.linux_release.campaign.guests import command, verify_execution_receipt
from scripts.linux_release.installed_cases.desktop import execute
from scripts.linux_release.installed_cases.records import (
    MAX_OBSERVATION_BYTES,
    compact_physical_input,
    expand_physical_transactions,
    retain,
)
from scripts.linux_release.installed_execution import case_family, driver_family, execute_case
from scripts.linux_release.installed_transfer import export, receive


class InstalledHelperTests(unittest.TestCase):
    def test_observation_retention_is_exclusive_and_does_not_promote_missing_cases(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binding = {"source_commit": "a" * 40, "artifact": {"installation": "deb"}}
            result = execute(binding, "windowing", "resize", root)
            self.assertEqual(result["status"], "unverified")
            self.assertTrue(result["evidence"])
            with self.assertRaises(FileExistsError):
                retain(binding, "windowing", "resize", root, "passed", {})
            with self.assertRaises(ValueError):
                retain(binding, "../escape", "case", root, "passed", {})

    def test_binary_streams_cannot_replace_structured_case_observations(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            binding = {"source_commit": "a" * 40, "artifact": {"installation": "deb"}}
            streams = {"stdout": b"\x00binary output", "stderr": b"\xffdiagnostic"}
            result = retain(binding, "session", "actual-case", root, "passed", {"observed": True}, streams)
            for name, raw in streams.items():
                self.assertEqual((root / "session/actual-case" / (name + ".bin")).read_bytes(), raw)
            document = json.loads((root / result["evidence"][-1]["path"]).read_text())
            self.assertEqual(document["observations"], {"observed": True})
            self.assertEqual(document["artifact"], binding["artifact"])
            self.assertEqual(document["status"], "passed")

    def test_registry_selects_nix_only_for_nix_installations(self):
        self.assertEqual(driver_family("updates", "deb"), "packages")
        self.assertEqual(driver_family("updates", "nix"), "nix_workflows")
        self.assertEqual(driver_family("session", "rpm"), "security")
        self.assertEqual(driver_family("performance", "deb"), "performance")
        for case in ("redacted-send", "ocr-and-documents"):
            self.assertEqual(case_family("documents", case, "deb"), "documents")
        self.assertEqual(case_family("documents", "scanner-startup", "deb"), "products")
        self.assertEqual(case_family("installation", "launcher-arguments", "deb"), "desktop")

    def test_api_rejects_external_and_redirect_authority_before_network(self):
        api = EngineAPI(19876, "private-token")
        for path in ("https://example.com/v1/sessions", "//example.com", "/other", "/v1/sessions#fragment"):
            with self.assertRaises(ValueError):
                api.request("GET", path)
        with self.assertRaises(ValueError):
            api.request("TRACE", "/health")

    def test_transfer_round_trip_and_refuses_traversal_and_symlinks(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            leaf = root / "leaf"
            leaf.mkdir()
            for name in ("campaign.json", "binding.json", "driver-registry.json", "execution.json", "variant.json"):
                (leaf / name).write_text("{}")
            archive = root / "leaf.zip"
            with archive.open("wb") as stream:
                export(leaf, stream)
            receive(archive, root / "received")
            self.assertEqual((root / "received/campaign.json").read_text(), "{}")
            (leaf / "link").symlink_to(leaf / "campaign.json")
            with self.assertRaises(ValueError):
                export(leaf, io.BytesIO())
            with zipfile.ZipFile(archive, "w") as stream:
                stream.writestr("../escape", "unsafe")
            with self.assertRaises(ValueError):
                receive(archive, root / "unsafe")

    def test_guest_command_pins_ssh_identity_and_quotes_paths(self):
        target = {"id": "noble", "host": "127.0.0.1", "port": 45557, "user": "tester",
                  "identity_file": "/private/key", "known_hosts": "/private/known_hosts",
                  "checkout": "/checkout with spaces", "runtime_bin_dirs": ["/toolchain/bin"],
                  "artifact_id": "deb-amd64", "environment_id": "ubuntu-24-gnome",
                  "final_artifacts": "/control/final.json", "file_root": "/packages",
                  "install_root": "/opt/painted-wolf-code", "output": "/fresh/output",
                  "nix_program": None, "case_inputs": None, "variant_id": "noble-scale-100",
                  "snapshot": {"kind": "package-inventory", "value": "a" * 64, "channel": None}, "input_adapter": None}
        result = command(target, ["./task", "test:linux:installed", "--", "--run-cases"])
        self.assertIn("StrictHostKeyChecking=yes", result)
        self.assertIn("IdentitiesOnly=yes", result)
        self.assertIn("cd '/checkout with spaces'", result[-1])
        self.assertIn("exec env PATH=/toolchain/bin:/usr/local/bin:/usr/bin:/bin ./task", result[-1])
        for field, value in (("host", "-oProxyCommand=untrusted"), ("port", True),
                             ("runtime_bin_dirs", ["/bin:untrusted"])):
            invalid = {**target, field: value}
            with self.assertRaises(ValueError):
                command(invalid, ["./task"])
        with self.assertRaises(ValueError):
            command({**target, "command": "untrusted"}, ["./task"])

    def test_stale_leaf_output_cannot_replace_a_fresh_execution(self):
        target = {"artifact_id": "selected", "environment_id": "guest", "variant_id": "floor-scale-100",
                  "snapshot": {"kind": "package-inventory", "value": "f" * 64, "channel": None}, "input_adapter": None}
        request = {"format_version": 1, "execution_nonce": "a" * 32, "source_commit": "b" * 40,
                   "driver_tree": "c" * 40, "artifact_id": target["artifact_id"], "environment_id": target["environment_id"], "variant_id": target["variant_id"]}
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "leaf").mkdir()
            (root / "execution-request.json").write_text(json.dumps(request))
            (root / "leaf/execution.json").write_text(json.dumps(request))
            variant = {"format_version": 1, "variant": {"id": target["variant_id"],
                       "release": {"snapshot_kind": "package-inventory", "channel": None}},
                       "snapshot": target["snapshot"], "monitors": {}, "factory": None}
            (root / "leaf/variant.json").write_text(json.dumps(variant))
            self.assertEqual(verify_execution_receipt(root, target, "b" * 40, "c" * 40), request)
            for name, value in (("execution_nonce", "d" * 32), ("driver_tree", "e" * 40), ("format_version", True)):
                (root / "leaf/execution.json").write_text(json.dumps({**request, name: value}))
                with self.assertRaises(ValueError):
                    verify_execution_receipt(root, target, "b" * 40, "c" * 40)

    def test_retain_compacts_physical_input_into_linked_stream_without_mutating_caller(self):
        binding = {"source_commit": "a" * 40, "artifact": {"installation": "deb"}}
        lineage = [{"pid": 100 + index, "parent_pid": 99 + index, "start_time_ticks": 1000 + index,
                    "comm": "painted-wolf", "cmdline": ["/opt/painted-wolf-code/painted-wolf-code"]}
                   for index in range(6)]
        focus = {"launch": lineage[0], "entry": lineage[-1], "frame": lineage[1],
                 "frame_lineage": lineage, "entry_lineage": lineage,
                 "accessible_chain": [{"bus_name": ":1.42", "path": f"/org/a11y/atspi/accessible/{index}",
                                       "pid": 105, "role": "panel"} for index in range(8)],
                 "entry_focused": True, "frame_active": True, "frame_visible": True}

        def make_tx(tx_id: str, operation: str, count: int, completed: bool = True) -> dict:
            events = [{"request": {"format_version": 1, "transaction_id": tx_id, "operation": operation,
                                   "ordinal": ordinal, "focus": focus},
                       "delivery": {"format_version": 1, "transaction_id": tx_id, "operation": operation,
                                    "ordinal": ordinal, "qmp_ack": {"return": {}}},
                       "focus_after": focus}
                      for ordinal in range(count)]
            return {"format_version": 1, "vm_id": "owned", "transaction_id": tx_id,
                    "operation": operation, "completed": completed, "events": events, "text_readback": False}

        tx1 = make_tx("1" * 32, "provider-name-v1", 35)
        tx2 = make_tx("2" * 32, "provider-endpoint-v1-41234", 27)
        tx3 = make_tx("3" * 32, "provider-fixture-credential-v1", 19)
        tx4 = make_tx("4" * 32, "reply-fixture-prompt-v1", 55, completed=False)
        tx5 = make_tx("5" * 32, "session-lock-v1", 2)
        tx6 = make_tx("6" * 32, "editor-replace-save-v1", 10)
        fixed_input = [tx1, tx2, tx3, tx4]
        observations = {
            "launch": {"fixed_input": fixed_input, "session": {"User": "30005"}},
            "native_input": tx4,
            "transitions": [tx5],
            "lock_transition": tx5,
            "editor_keyboard": {"physical_vm_keyboard": tx6},
        }
        inline_raw = (json.dumps({"format_version": 1, "source_commit": binding["source_commit"],
                                  "artifact": binding["artifact"], "capability": "session",
                                  "case": "inhibitor-lifetime", "status": "unverified",
                                  "observations": observations}, indent=2, sort_keys=True) + "\n").encode()
        self.assertGreater(len(inline_raw), MAX_OBSERVATION_BYTES)
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            result = retain(binding, "session", "inhibitor-lifetime", root, "unverified", observations)
            self.assertIn("events", fixed_input[0])
            self.assertIn("events", observations["native_input"])
            leaf = root / "session/inhibitor-lifetime/execution.json"
            stream_path = root / "session/inhibitor-lifetime/physical-input.bin"
            self.assertTrue(stream_path.is_file())
            self.assertLess(leaf.stat().st_size, MAX_OBSERVATION_BYTES)
            document = json.loads(leaf.read_text())
            compacted_fixed = document["observations"]["launch"]["fixed_input"]
            self.assertEqual(len(compacted_fixed), 4)
            self.assertNotIn("events", compacted_fixed[0])
            self.assertEqual(compacted_fixed[0]["event_count"], 35)
            self.assertEqual(compacted_fixed[0]["physical_input_stream"], "physical-input.bin")
            self.assertEqual(document["observations"]["native_input"], compacted_fixed[3])
            stream_payload = json.loads(stream_path.read_bytes())
            self.assertEqual(len(stream_payload["transactions"]), 6)
            self.assertEqual(expand_physical_transactions(leaf, compacted_fixed), fixed_input)
            self.assertEqual(expand_physical_transactions(leaf, document["observations"]["transitions"]), [tx5])
            self.assertEqual(expand_physical_transactions(
                leaf, [document["observations"]["editor_keyboard"]["physical_vm_keyboard"]]), [tx6])
            self.assertEqual(result["evidence"][-1]["path"], "session/inhibitor-lifetime/execution.json")
