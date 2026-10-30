"""Incomplete reservation metadata cannot release a held admission capability."""

from verification_resources import Reservation, capacity
from verification_tests.scheduler.test_terminal_settlement_execution import TerminalFixture


class ResourceMetadataTests(TerminalFixture):
    def damage(self, capability, body):
        with self.queue.locked():
            capability.file.seek(0)
            capability.file.truncate()
            capability.file.write(body)
            capability.file.flush()

    def test_incomplete_held_metadata_blocks_work_without_rewriting_the_lease(self):
        for body in ("", '{"ticket":'):
            with self.subTest(body=body):
                capability = self.capability()
                inode = capability.path.stat().st_ino
                self.damage(capability, body)
                status = self.queue.status()
                self.assertEqual(status["capacity"]["available"], 0)
                self.assertEqual(status["operations"][0]["locks"], ["*"])
                self.assertEqual(status["operations"][0]["metadata_status"], "unreadable")
                self.assertEqual(capability.path.stat().st_ino, inode)
                self.assertEqual(capability.path.read_text(), body)
                with Reservation(self.queue, self.queue.lock_file, "next", "Following work",
                                 {"locks": ["unrelated"], "workers": 1}, 1) as following:
                    self.assertFalse(following.try_acquire())
                    capability.file.close()
                    self.assertTrue(following.try_acquire())
                    self.assertFalse(capability.path.exists())
                self.assertEqual(self.queue.status()["operations"], [])
                self.assertEqual(self.queue.status()["capacity"]["available"], capacity())

    def test_completed_metadata_restores_precise_accounting_while_lock_stays_held(self):
        capability = self.capability()
        self.damage(capability, '{"ticket":')
        self.assertEqual(self.queue.status()["capacity"]["available"], 0)
        capability.write()
        status = self.queue.status()
        self.assertEqual(status["capacity"]["reserved"], 1)
        self.assertEqual(status["operations"][0]["ticket"], capability.entry["ticket"])
        self.assertNotIn("metadata_status", status["operations"][0])
        self.assertTrue(capability.path.exists())

    def test_incomplete_unlocked_metadata_is_reclaimed_after_ownership_probe(self):
        for index, body in enumerate(("", '{"ticket":')):
            with self.subTest(body=body):
                path = self.queue.root / f"orphan-{index}.resource"
                path.write_text(body)
                self.assertEqual(self.queue.status()["operations"], [])
                self.assertFalse(path.exists())
                self.assertEqual(self.queue.status()["capacity"]["available"], capacity())
