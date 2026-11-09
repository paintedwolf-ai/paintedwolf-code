"""Explicit live harness selections must reach every generation role."""

import contextlib
import io
import json
import unittest
from unittest.mock import Mock, patch

from harness.model_policy import configure, main, select_model


class HarnessModelPolicyTest(unittest.TestCase):
    def test_cli_sends_project_patch_with_encoded_model(self):
        responses = [io.BytesIO(b'{"coordinator":{"provider_id":"saved"}}'), io.BytesIO(b'{}')]
        output = io.StringIO()
        with patch.dict("os.environ", {"LYCAON_E2E_API_URL": "http://127.0.0.1:1234", "LYCAON_E2E_TOKEN": "fixture"}), \
                patch("sys.argv", ["model_policy.py", "project id", "", 'model "quoted"']), \
                patch("urllib.request.urlopen", side_effect=responses) as send, \
                contextlib.redirect_stdout(output):
            main()
        read, write = (call.args[0] for call in send.call_args_list)
        self.assertEqual(read.method, "GET")
        self.assertEqual(write.method, "PATCH")
        self.assertEqual(write.full_url, "http://127.0.0.1:1234/v1/settings/model-policy?project_id=project+id")
        self.assertEqual(write.get_header("Authorization"), "Bearer fixture")
        self.assertEqual(json.loads(write.data)["lite"], {"provider_id": "saved", "model": 'model "quoted"'})
        self.assertEqual(output.getvalue(), "saved\n")

    def test_preserves_pool_policy_and_resolves_provider_without_jq(self):
        current = {"coordinator": {"provider_id": "configured", "model": "old"},
                   "agent_pool": {"selection": "random", "models": [{"model": "old"}]},
                   "thinking": {"effort": "medium"}}
        policy = select_model(current, "", 'model "quoted"')
        selection = {"provider_id": "configured", "model": 'model "quoted"'}
        self.assertEqual(policy["coordinator"], selection)
        self.assertEqual(policy["lite"], selection)
        self.assertEqual(policy["agent_pool"], {"selection": "random", "models": [selection]})
        self.assertEqual(policy["thinking"], current["thinking"])
        self.assertEqual(current["coordinator"]["model"], "old")

    def test_missing_pool_uses_supported_selection(self):
        policy = select_model({}, "provider", "model")
        self.assertEqual(policy["agent_pool"]["selection"], "first")
        with self.assertRaises(ValueError):
            select_model({}, "", "model")
        with self.assertRaises(ValueError):
            select_model({}, "provider", "")

    def test_project_policy_is_read_and_written_and_failures_propagate(self):
        request = Mock(side_effect=[{"coordinator": {"provider_id": "saved"}}, {}])
        self.assertEqual(configure("project id", "", "chosen", request), "saved")
        path = "/v1/settings/model-policy?project_id=project+id"
        self.assertEqual(request.call_args_list[0].args, (path,))
        self.assertEqual(request.call_args_list[1].args[0], path)
        self.assertEqual(request.call_args_list[1].args[1]["lite"]["model"], "chosen")
        for responses in ([RuntimeError("read failed")], [{}, RuntimeError("write failed")]):
            with self.subTest(responses=responses), self.assertRaises(RuntimeError):
                configure("project", "provider", "model", Mock(side_effect=responses))
