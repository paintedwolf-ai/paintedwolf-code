"""Explicit live harness selections must reach every generation role."""

import unittest
from unittest.mock import Mock

from harness.model_policy import configure, select_model


class HarnessModelPolicyTest(unittest.TestCase):
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
