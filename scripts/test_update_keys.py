#!/usr/bin/env python3
"""Offline release policy rehearsal using synthetic signing generations."""
import base64
import copy
import json
import importlib.util
import sys
from unittest.mock import patch
from pathlib import Path
import tempfile
import unittest

from release_semver import compare, parse
from update_keys import feed_key, fingerprint, load_registry, release_binding, validate_binding, validate_publication, validate_bridge_advance


def public_key(number):
    raw = b"Ed" + number.to_bytes(8, "little") + bytes([number]) * 32
    return base64.b64encode(b"untrusted comment: test public key\n" + base64.b64encode(raw) + b"\n").decode()


def registry():
    return {"schema_version": 1, "signing_generation": 3, "embedded_generation": 3, "generations": [
        {"generation": 1, "public_key": public_key(1), "feed_public_key": public_key(11), "successor": 2, "bridge_version": "2.0.0"},
        {"generation": 2, "public_key": public_key(2), "feed_public_key": public_key(12), "successor": 3, "bridge_version": "3.0.0"},
        {"generation": 3, "public_key": public_key(3), "feed_public_key": public_key(13), "successor": None, "bridge_version": None},
    ]}


def release(keys, version, signing, embedded):
    selected = {**keys, "signing_generation": signing, "embedded_generation": embedded}
    return {"version": version, "update_keys": release_binding(selected, version)}


class FeedSignatureCheck(unittest.TestCase):
    FIXTURE = Path(__file__).resolve().parent.parent / "lycaon-den/src-tauri/src/update_service/fixtures/signed-feed.json"

    def load(self):
        spec = importlib.util.spec_from_file_location("feed_signature", Path(__file__).with_name("feed_signature.py"))
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module, json.loads(self.FIXTURE.read_text())

    def test_signature_bindings_are_checked_before_publication(self):
        feed_signature, fixture = self.load()
        timestamp = feed_signature.check(fixture["signature"], file="latest-stable-key-1.json", version="1.2.3", number=1, rehearsal=True)
        self.assertGreater(timestamp, 1_700_000_000)
        with self.assertRaises(ValueError):
            feed_signature.check(fixture["signature"], file="latest-preview-key-1.json", version="1.2.3", number=1, rehearsal=True)
        with self.assertRaises(ValueError):
            feed_signature.check(fixture["signature"], file="latest-stable-key-1.json", version="1.2.4", number=1, rehearsal=True)
        with self.assertRaises(ValueError):
            feed_signature.check(fixture["unbound_signature"], file="latest-stable-key-1.json", version="1.2.3", number=1, rehearsal=True)
        # Outside a rehearsal the signer must be the registered feed key of the generation.
        with self.assertRaises(ValueError):
            feed_signature.check(fixture["signature"], file="latest-stable-key-1.json", version="1.2.3", number=1, rehearsal=False)


class UpdateKeyRehearsal(unittest.TestCase):
    def test_skip_two_rotations_from_both_channels(self):
        keys = registry()
        bridge1 = release(keys, "2.0.0", 1, 2)
        bridge2 = release(keys, "3.0.0", 2, 3)
        latest = release(keys, "3.1.0", 3, 3)
        feeds = {feed_key(channel, gen): manifest for channel in ("stable", "preview")
                 for gen, manifest in [(1, bridge1), (2, bridge2), (3, latest)]}
        for channel, current in [("stable", "1.0.0"), ("preview", "2.0.0-rc.99")]:
            number = 1
            visited = []
            while True:
                offer = feeds[feed_key(channel, number)]
                validate_publication(keys, offer, number)
                if compare(parse(offer["version"]), parse(current)) <= 0:
                    break
                visited.append(offer["version"])
                # The embedded key activates on installation.
                current = offer["version"]
                number = offer["update_keys"]["embedded_generation"]
            self.assertEqual(visited, ["2.0.0", "3.0.0", "3.1.0"])
        with self.assertRaises(ValueError):
            validate_publication(keys, latest, 1)
        with self.assertRaises(ValueError):
            release(keys, "1.9.0", 1, 1)

    def test_failed_bridge_install_and_halt_keep_source_trust(self):
        keys = registry()
        bridge = release(keys, "2.0.0", 1, 2)
        current_generation = 1
        for installed in (False, False, True):
            validate_publication(keys, bridge, current_generation)
            if installed:
                current_generation = bridge["update_keys"]["embedded_generation"]
        self.assertEqual(current_generation, 2)
        wrong = copy.deepcopy(bridge)
        wrong["update_keys"]["signing_key_fingerprint"] = fingerprint(public_key(2))
        with self.assertRaises(ValueError):
            validate_binding(keys, wrong, 1)
        replacement_keys = copy.deepcopy(keys)
        replacement_keys["generations"][0]["bridge_version"] = "2.0.1"
        replacement = release(replacement_keys, "2.0.1", 1, 2)
        validate_publication(replacement_keys, replacement, 1)
        self.assertGreater(compare(parse(replacement["version"]), parse(bridge["version"])), 0)
        with self.assertRaises(ValueError):
            validate_publication(replacement_keys, bridge, 1)
        validate_publication(replacement_keys, bridge, 1, halt=True)

    def test_bridge_must_exceed_highest_preview_and_replacement_cannot_go_back(self):
        keys = registry()
        bridge = release(keys, "2.0.0", 1, 2)
        validate_bridge_advance(bridge, {"version": "2.0.0-rc.99"})
        validate_bridge_advance(bridge, bridge)
        for version in ["2.0.0", "2.1.0-rc.1", "2.0.1"]:
            with self.assertRaises(ValueError):
                validate_bridge_advance(bridge, {"version": version})

    def test_registry_rejects_missing_routes_and_reused_keys(self):
        for mutation in (lambda value: value["generations"].pop(1),
                         lambda value: value["generations"][1].update(public_key=public_key(1)),
                         lambda value: value["generations"][1].update(feed_public_key=public_key(2)),
                         lambda value: value["generations"][1].pop("feed_public_key"),
                         lambda value: value["generations"][0].update(successor=3),
                         lambda value: value["generations"][0].update(bridge_version="2.0.0-rc.1"),
                         lambda value: value["generations"][1].update(bridge_version="1.9.0")):
            value = registry()
            mutation(value)
            with tempfile.TemporaryDirectory() as temp:
                path = Path(temp) / "keys.json"
                path.write_text(json.dumps(value))
                with self.assertRaises(ValueError):
                    load_registry(path)

    def test_cascade_halt_requires_all_feeds_and_validates_before_writing(self):
        spec = importlib.util.spec_from_file_location("halt_plan", Path(__file__).with_name("release-halt-plan.py"))
        planner = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(planner)
        keys = registry()
        feeds = {feed_key(channel, number): release(keys, version, number, embedded)
                 for channel in ("stable", "preview")
                 for number, version, embedded in [(1, "2.0.0", 2), (2, "3.0.0", 3), (3, "3.1.0", 3)]}
        rows = [{"generation": number, "channel": channel, **(
                    {"bad": "2.0.0", "last_good": "1.9.0"} if number == 1 else
                    {"keep_version": "3.0.0" if number == 2 else "3.1.0"})}
                for number in (1, 2, 3) for channel in ("stable", "preview")]
        self.assertEqual(planner.distribution_plan(rows), {"bad_versions": ["2.0.0"], "channels": []})
        all_bad = copy.deepcopy(rows)
        all_bad[-1] = {"generation": 3, "channel": "preview", "bad": "3.1.0", "last_good": "3.0.1"}
        self.assertEqual(planner.distribution_plan(all_bad)["channels"], [all_bad[-1]])
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "plan.json"
            with patch.object(planner, "load_registry", return_value=keys), patch.object(sys, "argv", ["halt", "--plan", str(path)]), patch.object(planner, "read_storage", side_effect=feeds.get), patch.object(planner.subprocess, "run") as run:
                run.return_value.returncode = 0
                path.write_text(json.dumps({"source_generation": 1, "feeds": rows[:-1]}))
                with self.assertRaises(ValueError):
                    planner.main()
                run.assert_not_called()
                bad_keep = copy.deepcopy(rows)
                bad_keep[1] = {"generation": 1, "channel": "preview", "keep_version": "2.0.0"}
                path.write_text(json.dumps({"source_generation": 1, "feeds": bad_keep}))
                with self.assertRaises(ValueError):
                    planner.main()
                run.assert_not_called()
                path.write_text(json.dumps({"source_generation": 1, "feeds": rows}))
                planner.main()
                self.assertEqual(len(run.call_args_list), 4)
                self.assertTrue(all("--dry-run" in call.args[0] for call in run.call_args_list[:2]))
                self.assertTrue(all("--dry-run" not in call.args[0] for call in run.call_args_list[2:]))
                feeds[feed_key("stable", 1)] = {**feeds[feed_key("stable", 1)], "version": "1.9.0"}
                run.reset_mock()
                planner.main()
                self.assertEqual(len(run.call_args_list), 4)

    def test_generation_one_feeds_halt_only_to_a_replacement(self):
        spec = importlib.util.spec_from_file_location("halt_plan", Path(__file__).with_name("release-halt-plan.py"))
        planner = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(planner)
        keys = registry()
        feeds = {feed_key(channel, number): release(keys, version, number, embedded)
                 for channel in ("stable", "preview")
                 for number, version, embedded in [(1, "2.0.0", 2), (2, "3.0.0", 3)]}
        with patch.object(planner, "read_storage", side_effect=feeds.get):
            with self.assertRaises(ValueError):
                planner.plan_withdrawal(keys, 1, "2.0.0", None)
            plan = planner.plan_withdrawal(keys, 1, "2.0.0", "1.9.0")
            self.assertEqual([row["last_good"] for row in plan["feeds"] if row["generation"] == 1], ["1.9.0", "1.9.0"])
            withdrawn_only = planner.plan_withdrawal(keys, 2, "3.0.0", None)
            self.assertEqual([row["last_good"] for row in withdrawn_only["feeds"] if "bad" in row], [None, None])
        rows = [{"generation": 1, "channel": channel, "bad": "2.0.0", "last_good": None} for channel in ("stable", "preview")]
        rows += [{"generation": 2, "channel": channel, "keep_version": "3.0.0"} for channel in ("stable", "preview")]
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "plan.json"
            path.write_text(json.dumps({"source_generation": 1, "feeds": rows}))
            with patch.object(planner, "load_registry", return_value=keys), patch.object(sys, "argv", ["halt", "--plan", str(path)]), patch.object(planner, "read_storage", side_effect=feeds.get), patch.object(planner.subprocess, "run") as run:
                with self.assertRaises(ValueError):
                    planner.main()
                run.assert_not_called()

    def test_real_registry_carries_distinct_artifact_and_feed_keys_for_the_embedded_generation(self):
        keys = load_registry()
        row = keys["generations"][keys["embedded_generation"] - 1]
        self.assertNotEqual(fingerprint(row["public_key"]), fingerprint(row["feed_public_key"]))


if __name__ == "__main__":
    unittest.main()
