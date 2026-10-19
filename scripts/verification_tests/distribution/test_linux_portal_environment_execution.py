"""Portal snapshots never combine service generations or claim interactive success."""

import unittest
from unittest.mock import patch

from linux_release.portal_environment import capture, ObservationUnavailable, SERVICES, SessionBus


class FakeBus:
    def __init__(self):
        self.generation = "bus-generation"
        self.owners = {name: ":1." + str(index) for index, name in enumerate(SERVICES)}
        self.calls = []
        self.after_properties = lambda: None
        self.version = 3

    def bus_id(self):
        return self.generation

    def owner(self, name):
        return self.owners[name]

    def properties(self, owner, path, interface):
        self.calls.append((owner, path, interface))
        self.after_properties()
        return {"version": self.version}


class PortalEnvironmentTests(unittest.TestCase):
    def test_stable_snapshot_records_only_unique_owner_versions(self):
        bus = FakeBus()
        result = capture(bus)
        self.assertEqual(result["status"], "observed")
        self.assertEqual(result["qualification_status"], "unverified")
        self.assertTrue(all(owner.startswith(":") for owner, _, _ in bus.calls))
        self.assertEqual(len(result["services"]), len(SERVICES))
        self.assertTrue(all(interface["version"] == 3 for service in result["services"]
                            for interface in service["interfaces"]))
        self.assertNotIn("cases", result)

    def test_missing_service_is_unverified_and_never_queried(self):
        bus = FakeBus()
        bus.owners = dict.fromkeys(SERVICES)
        result = capture(bus)
        self.assertEqual(result["status"], "unverified")
        self.assertEqual(bus.calls, [])

    def test_missing_malformed_and_boolean_versions_are_unknown(self):
        for version in (None, True, 0, -1, 0x100000000, "3"):
            bus = FakeBus()
            bus.version = version
            result = capture(bus)
            self.assertEqual(result["status"], "observed")
            for service in result["services"]:
                for interface in service["interfaces"]:
                    self.assertEqual(interface["status"], "version-unavailable")
                    self.assertIsNone(interface["version"])

    def test_owner_replacement_or_bus_reconnect_invalidates_snapshot(self):
        for change, code in ((lambda bus: bus.owners.update({next(iter(SERVICES)): ":1.999"}), "portal-owner-changed"),
                             (lambda bus: setattr(bus, "generation", "replacement-bus"), "bus-generation-changed")):
            bus = FakeBus()
            bus.after_properties = lambda: change(bus)
            result = capture(bus)
            self.assertEqual(result["status"], "unverified")
            self.assertIn(code, result["errors"])

    def test_disappearance_and_typed_call_failure_remain_unverified(self):
        bus = FakeBus()
        def lost():
            raise ObservationUnavailable("portal-bus-call-failed")
        bus.after_properties = lost
        result = capture(bus)
        self.assertEqual(result["status"], "unverified")
        self.assertEqual(result["errors"], ["portal-bus-call-failed"])
        bus = FakeBus()
        bus.owners[next(iter(SERVICES))] = "org.freedesktop.portal.Desktop"
        self.assertIn("unique-owner-unavailable", capture(bus)["errors"])

    def test_absent_session_address_does_not_autolaunch_a_bus(self):
        with patch.dict("os.environ", {}, clear=True), self.assertRaises(ObservationUnavailable) as raised:
            SessionBus()
        self.assertEqual(raised.exception.code, "session-bus-address-unavailable")
