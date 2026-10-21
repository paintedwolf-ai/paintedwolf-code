"""The managed browser's ELF runtime and the APT adapter are hard package dependencies.

Distributions install recommendations and weak dependencies only by default, and minimal images
or `--no-install-recommends` drop them. A library the shipped browser loads, or the module the
configured-source query imports, must therefore be rendered into Depends/Requires by the real
assembler, never into Recommends.
"""

import importlib.util
import json
from pathlib import Path
import re
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[3]

# Each requirement is satisfied by any one of the named packages in a single dependency row.
REQUIRED = {
    "deb": {
        "NSS": {"libnss3"},
        "NSPR": {"libnspr4"},
        "ATK bridge": {"libatk-bridge2.0-0t64", "libatk-bridge2.0-0"},
        "GBM": {"libgbm1"},
        "xkbcommon": {"libxkbcommon0"},
        "ALSA": {"libasound2t64", "libasound2"},
        "APT adapter": {"python3-apt"},
    },
    "rpm": {
        "NSS": {"nss"},
        "NSPR": {"nspr"},
        "ATK bridge": {"at-spi2-atk"},
        "GBM": {"mesa-libgbm"},
        "xkbcommon": {"libxkbcommon"},
        "ALSA": {"alsa-lib"},
    },
}
PACKAGE_NAME = re.compile(r"\s*([^\s(<>=]+)")


def assembler():
    spec = importlib.util.spec_from_file_location("linux_package_assemble", ROOT / "scripts/linux-package-assemble.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def names(row):
    return {PACKAGE_NAME.match(alternative).group(1) for alternative in row.split("|")}


def satisfied(rows, accepted):
    return [row for row in rows if names(row) & accepted]


def control_field(text, field):
    for line in text.splitlines():
        if line.startswith(f"{field}: "):
            return [row.strip() for row in line[len(field) + 2:].split(",") if row.strip()]
    return []


def spec_field(text, field):
    return [line[len(field) + 2:].strip() for line in text.splitlines() if line.startswith(f"{field}: ")]


class LinuxRuntimeDependencyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.assemble = assembler()
        cls.catalog = json.loads((ROOT / "packaging/linux/dependencies.json").read_text(encoding="utf-8"))

    def assert_hard(self, family, depends, recommends):
        for requirement, accepted in REQUIRED[family].items():
            with self.subTest(family=family, requirement=requirement):
                self.assertEqual(len(satisfied(depends, accepted)), 1, f"{family} must depend on {requirement} once")
                self.assertEqual(satisfied(recommends, accepted), [], f"{family} must not merely recommend {requirement}")

    def test_every_catalog_family_declares_the_runtime_it_loads(self):
        families = {key for key, value in self.catalog.items() if isinstance(value, dict)}
        self.assertEqual(families, set(REQUIRED), "a new package family must declare its browser runtime here")
        for family in sorted(families):
            rendered = self.assemble.rendered_dependencies(ROOT, family)
            self.assert_hard(family, rendered["depends"], rendered["recommends"])

    def test_rendered_deb_control_and_rpm_spec_carry_the_runtime_as_hard_dependencies(self):
        with tempfile.TemporaryDirectory() as directory:
            tree = Path(directory)
            shell = tree / "opt/painted-wolf-code/bin/painted-wolf-code"
            shell.parent.mkdir(parents=True)
            shell.write_text("fixture")
            control = self.assemble.deb_control(ROOT, tree, "1.0.2~rc.1", "x86_64")
            spec = self.assemble.rpm_spec(ROOT, tree, "1.0.2~rc.1", 3)
        self.assert_hard("deb", control_field(control, "Depends"), control_field(control, "Recommends"))
        self.assert_hard("rpm", spec_field(spec, "Requires"), spec_field(spec, "Recommends"))
        self.assertIn("AutoReqProv: no", spec.splitlines(), "declared Requires are the only rpm dependencies")

    def test_hardware_vulkan_stays_optional(self):
        for family, accepted in {"deb": {"libvulkan1"}, "rpm": {"vulkan-loader"}}.items():
            rendered = self.assemble.rendered_dependencies(ROOT, family)
            self.assertEqual(satisfied(rendered["depends"], accepted), [])
            self.assertEqual(len(satisfied(rendered["recommends"], accepted)), 1)


if __name__ == "__main__":
    unittest.main()
