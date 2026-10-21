"""Durable activation intent allows exact crash recovery without expiring authority."""

import unittest

from scripts.linux_release.repository_pending import activation_plan, marker


class PendingActivationTests(unittest.TestCase):
    def setUp(self):
        self.record = {"generation": "a" * 64, "operation": "publish", "version": "1.0.0"}
        self.arguments = {"record": self.record, "expected": None, "storage_prefix": "linux/repositories",
                          "base_url": "https://repository.example.invalid", "phase": "activate",
                          "active": None, "pending": None}
        self.plan = activation_plan(**self.arguments)

    def test_exact_pending_retry_and_completed_current_are_retained(self):
        self.assertEqual(activation_plan(**{**self.arguments, "pending": marker(self.plan, "pending")}), self.plan)
        self.assertEqual(activation_plan(**{**self.arguments, "active": self.record,
                         "pending": marker(self.plan, "completed")}), self.plan)

    def test_pending_cannot_be_repurposed_or_expired(self):
        for changed in [{"record": {**self.record, "generation": "b" * 64}},
                        {"expected": "b" * 64}, {"phase": "stage"},
                        {"record": {**self.record, "operation": "recover"}},
                        {"base_url": "https://other.example.invalid"}, {"storage_prefix": "other"}]:
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                activation_plan(**{**self.arguments, "pending": marker(self.plan, "pending"), **changed})

    def test_completed_marker_requires_exact_current_state(self):
        for active in [None, {**self.record, "version": "0.9.0"}]:
            with self.subTest(active=active), self.assertRaisesRegex(ValueError, "active authority"):
                activation_plan(**{**self.arguments, "active": active, "pending": marker(self.plan, "completed")})

    def test_unknown_or_extended_marker_shapes_are_refused(self):
        for pending in [{}, {**marker(self.plan, "pending"), "expires": "later"},
                        marker({**self.plan, "format_version": True}, "pending"),
                        marker({**self.plan, "expected": 1}, "pending"),
                        marker({**self.plan, "generation": "arbitrary"}, "pending"),
                        marker({**self.plan, "phase": "stage"}, "pending")]:
            with self.subTest(pending=pending), self.assertRaises(ValueError):
                activation_plan(**{**self.arguments, "pending": pending})
        with self.assertRaises(ValueError):
            marker(self.plan, "expired")
