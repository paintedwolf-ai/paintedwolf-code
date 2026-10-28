"""Repository generation actions cannot turn recovery or rotation into ordinary releases."""

from pathlib import Path
import tempfile
import unittest

from scripts.linux_release.repository_objects import encoded, immutable
from scripts.linux_release.repository_operations import identity, transition, retained, mark_withdrawal, archive_trust


class RepositoryOperationsTests(unittest.TestCase):
    def receipt(self, version="1.0.0", signer=1):
        return {"format_version": 1, "family": "deb", "channel": "stable", "version": version,
                "source_commit": "f" * 40, "rehearsal": True, "signing_generation": signer,
                "key_fingerprint": ("A" if signer == 1 else "B") * 40,
                "inventory_sha256": "c" * 64, "packages": [{"file": "application.deb", "sha256": "d" * 64}]}

    def test_ordinary_publication_requires_version_advance_and_same_selected_signer(self):
        first = self.receipt()
        current = transition(first, None, None)
        next_release = self.receipt("1.0.1")
        self.assertEqual(transition(next_release, current, identity(first))["operation"], "publish")
        for receipt in [self.receipt("0.9.0"), self.receipt("1.0.1", 2), {**first, "source_commit": "e" * 40}]:
            with self.subTest(receipt=receipt), self.assertRaises(ValueError):
                transition(receipt, current, identity(first))
        self.assertEqual(transition(first, current, None), current)

    def test_rotation_and_recovery_are_explicit_and_stale_plans_refuse(self):
        first = self.receipt()
        current = transition(first, None, None)
        second = self.receipt("1.0.1", 2)
        rotated = transition(second, current, identity(first), "rotate")
        self.assertEqual(rotated["operation"], "rotate")
        restored = transition(first, rotated, identity(second), "recover")
        self.assertEqual(restored["operation"], "recover")
        for expected in [None, "0" * 64]:
            with self.assertRaises(ValueError):
                transition(second, current, expected, "rotate")
        with self.assertRaises(ValueError):
            transition(self.receipt("1.0.1"), current, identity(first), "rotate")

    def test_withdrawal_retains_content_and_permanently_refuses_reoffering_bad_generation(self):
        good, bad = self.receipt(), self.receipt("1.0.1")
        current = transition(bad, transition(good, None, None), identity(good))
        record = transition(good, current, identity(bad), "withdraw")
        with tempfile.TemporaryDirectory() as temporary:
            store = Path(temporary)
            with self.assertRaises(ValueError):
                retained(store, good, "withdraw")
            archive = store / "generations" / identity(good) / "generation.json"
            immutable(archive, encoded(good))
            retained(store, good, "withdraw")
            mark_withdrawal(store, record)
            mark_withdrawal(store, record)
            retained(store, good, "recover")
            for operation in ["publish", "recover", "rotate"]:
                with self.assertRaises(ValueError):
                    retained(store, bad, operation)
            self.assertEqual(archive.read_bytes(), encoded(good))

    def test_archived_trust_contains_only_public_selected_facts(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            public = directory / "public.asc"
            public.write_bytes(b"public fixture bytes")
            generation = directory / "generation"
            archive_trust(generation, {"generation": 1, "fingerprint": "A" * 40,
                                      "private_key": "must never be archived"}, public, True)
            self.assertNotIn(b"must never", (generation / "key-catalog.json").read_bytes())
            self.assertEqual((generation / "public.asc").read_bytes(), public.read_bytes())

    def test_unknown_current_shapes_and_cross_channel_trust_are_refused(self):
        first = self.receipt()
        current = transition(first, None, None)
        for mutation in [{"format_version": 2}, {"operation": "unknown"}, {"channel": "preview"},
                         {"rehearsal": False}, {"rehearsal": 1}, {"signing_generation": 0}, {"key_fingerprint": "short"}]:
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                transition(self.receipt("1.0.1"), {**current, **mutation}, identity(first))

    def test_idempotent_retry_requires_the_immutable_generation_facts(self):
        first = self.receipt()
        current = transition(first, None, None)
        for mutation in [{"source_commit": "e" * 40}, {"inventory_sha256": "e" * 64},
                         {"packages": []}, {"signing_generation": 2}, {"version": "2.0.0"}]:
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                transition(first, {**current, **mutation}, None)
