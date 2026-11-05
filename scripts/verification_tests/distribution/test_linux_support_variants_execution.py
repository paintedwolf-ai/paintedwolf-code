"""Typed release and monitor variants cannot disappear behind prose facts."""
import copy
import json
import unittest

from linux_release.support_matrix import CATALOG, expand, qualification, variants


class LinuxSupportVariantTests(unittest.TestCase):
    def setUp(self):
        self.catalog = json.loads(CATALOG.read_text())
        self.capabilities = {item["id"] for item in self.catalog["capabilities"]}
        self.fedora = next(item for item in self.catalog["environments"] if item["id"] == "fedora-maintained-gnome")

    def test_release_and_scale_cartesian_requirements_include_both_fedora_generations(self):
        required = variants(self.fedora, "windowing", self.capabilities)
        self.assertEqual(len(required), 8)
        self.assertEqual({item["release"]["version_id"] for item in required}, {"43", "44"})
        self.assertEqual({tuple(item["scales_percent"]) for item in required}, {(100,), (150,), (200,), (100, 150)})
        self.assertEqual(len(variants(self.fedora, "documents", self.capabilities)), 2)
        self.assertEqual(len(expand(self.catalog)), len({item["id"] for item in expand(self.catalog)}))

    def test_conversation_semantics_keep_every_release_without_repeating_display_scales(self):
        for environment in self.catalog["environments"]:
            with self.subTest(environment=environment["id"]):
                required = variants(environment, "conversation", self.capabilities)
                releases = environment["qualification"]["releases"]
                self.assertEqual(len(required), len(releases))
                self.assertEqual({item["release"]["id"] for item in required}, {item["id"] for item in releases})
                self.assertEqual({tuple(item["scales_percent"]) for item in required}, {(100,)})
                for capability in ("input", "accessibility", "windowing"):
                    layout = variants(environment, capability, self.capabilities)
                    expected = {(release["id"], scale) for release in releases for scale in ((100,), (150,), (200,))}
                    self.assertTrue(expected <= {(item["release"]["id"], tuple(item["scales_percent"])) for item in layout})

    def test_unknown_missing_or_ambiguous_metadata_refuses_expansion(self):
        for mutate in (lambda value: value.pop("qualification"),
                       lambda value: value["qualification"]["releases"].append(copy.deepcopy(value["qualification"]["releases"][0])),
                       lambda value: value["qualification"]["display"].update(protocol="maybe-wayland"),
                       lambda value: value["qualification"]["scaling"].update(uniform_percent=[True]),
                       lambda value: value["qualification"]["scaling"].update(mixed_capabilities=["undeclared"]),
                       lambda value: value["qualification"]["scaling"].update(uniform_capabilities=["undeclared"]),
                       lambda value: value["qualification"]["scaling"].update(uniform_percent=[150, 200])):
            value = copy.deepcopy(self.fedora)
            mutate(value)
            with self.assertRaises(ValueError):
                qualification(value, self.capabilities)

    def test_minimal_wm_and_nixos_channel_requirements_are_typed(self):
        environments = {item["id"]: item for item in self.catalog["environments"]}
        self.assertEqual(environments["arch-i3"]["qualification"]["display"]["clipboard_manager"], "absent")
        self.assertEqual(environments["arch-sway"]["qualification"]["display"], {"protocol": "wayland", "xwayland": "disabled", "clipboard_manager": "permitted"})
        for key in ("nixos-stable-gnome-wayland", "nixos-unstable-gnome-wayland"):
            release = environments[key]["qualification"]["releases"][0]
            self.assertEqual(release["snapshot_kind"], "nixos-revision")
            self.assertEqual(release["os_ids"], ["nixos"])
