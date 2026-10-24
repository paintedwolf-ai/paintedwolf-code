"""Release Rust builds remap builder paths, and staging refuses binaries that still carry them."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from linux_release import build_paths

SCRIPTS = Path(__file__).resolve().parents[2]
HELPER = SCRIPTS / "toolchain/rust-path-remap.sh"


def remapped(environment, root, times=1):
    script = f'source "{HELPER}"\n' + f'rust_path_remap "{root}"\n' * times + (
        'printf "%s" "${CARGO_ENCODED_RUSTFLAGS}"; printf "\\n%s\\n%s\\n%s" '
        '"${RUSTFLAGS-unset}" "${CFLAGS}" "${CXXFLAGS}"')
    base = {"PATH": os.environ["PATH"]}
    result = subprocess.run(["bash", "-c", script], env={**base, **environment}, capture_output=True,
                            text=True, check=True, timeout=30)
    encoded, rustflags, cflags, cxxflags = result.stdout.split("\n")
    return encoded.split("\x1f"), rustflags, cflags, cxxflags


class RustPathRemap(unittest.TestCase):
    environment = {"HOME": "/home/builder", "CARGO_HOME": "/home/builder/.cargo",
                   "RUSTUP_HOME": "/opt/rustup", "PW_TEST_SNAPSHOT_ROOT": "/snapshots"}

    def test_specific_prefixes_follow_broader_ones_because_rustc_applies_the_last_match(self):
        flags, rustflags, cflags, cxxflags = remapped(self.environment, "/home/builder/work/source")
        self.assertEqual(flags, ["--remap-path-prefix=/home/builder=/build-home",
                                 "--remap-path-prefix=/opt/rustup=/rustup",
                                 "--remap-path-prefix=/home/builder/.cargo=/cargo",
                                 "--remap-path-prefix=/snapshots=/source-snapshots",
                                 "--remap-path-prefix=/home/builder/work/source=/source"])
        self.assertEqual(rustflags, "unset")
        maps = [flag.replace("--remap-path-prefix=", "-ffile-prefix-map=") for flag in flags]
        self.assertEqual((cflags.split(), cxxflags.split()), (maps, maps))

    def test_existing_flags_are_kept_and_repeated_sourcing_adds_nothing(self):
        flags, _, cflags, _ = remapped({**self.environment, "RUSTFLAGS": "-C  target-cpu=native",
                                        "CFLAGS": "-O2"}, "/src", times=2)
        self.assertEqual(flags[:2], ["-C", "target-cpu=native"])
        self.assertEqual(len(flags), 7)
        self.assertEqual(cflags.split()[0], "-O2")
        self.assertEqual(len(cflags.split()), 6)
        encoded = "-Cdebuginfo=0\x1f--cfg=feature"
        flags, *_ = remapped({**self.environment, "CARGO_ENCODED_RUSTFLAGS": encoded,
                              "RUSTFLAGS": "-C ignored"}, "/src")
        self.assertEqual(flags[:2], ["-Cdebuginfo=0", "--cfg=feature"])
        self.assertNotIn("ignored", flags)

    def test_source_snapshots_keep_the_checkout_compiler_fingerprint(self):
        checkout = remapped(self.environment, "/checkout")
        for slot in ("/snapshots/first-0", "/snapshots/second-1"):
            captured = remapped({**self.environment, "PW_SOURCE_ROOT_ORIGINAL": "/checkout"}, slot)
            self.assertEqual(captured, checkout)
            self.assertIn("--remap-path-prefix=/snapshots=/source-snapshots", captured[0])

    def test_a_checkout_with_spaces_stays_one_rustc_argument(self):
        flags, _, cflags, _ = remapped(self.environment, "/home/builder/my work")
        self.assertEqual(flags[-1], "--remap-path-prefix=/home/builder/my work=/source")
        self.assertNotIn("my work", cflags)

    def test_snapshot_slots_keep_identical_compiler_flags(self):
        environment = {**self.environment, "PW_SOURCE_SNAPSHOT_COMMIT": "captured"}
        first = remapped(environment, "/tmp/source-snapshots/repo-0")
        second = remapped(environment, "/tmp/source-snapshots/repo-1")
        self.assertEqual(first, second)
        self.assertEqual(first[0][-1], "--remap-path-prefix=/tmp/source-snapshots=/source")
        self.assertNotIn("repo-0", first[2])

    def test_unset_toolchain_homes_default_beneath_home(self):
        flags, *_ = remapped({"HOME": "/home/builder"}, "/src")
        self.assertIn("--remap-path-prefix=/home/builder/.cargo=/cargo", flags)
        self.assertIn("--remap-path-prefix=/home/builder/.rustup=/rustup", flags)
        flags, *_ = remapped({"HOME": ""}, "/src")
        self.assertEqual(flags[-1], "--remap-path-prefix=/src=/source")


class StagedBuilderPaths(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.tree = Path(temporary.name)
        self.bin = self.tree / build_paths.INSTALL_ROOT / "bin"
        self.bin.mkdir(parents=True)
        self.needles = build_paths.builder_paths({"HOME": "/home/runner"}, Path("/home/runner/work/pw"))

    def binary(self, name, data):
        (self.bin / name).write_bytes(data)

    def test_builder_paths_cover_home_toolchains_and_source_once(self):
        self.assertEqual(self.needles, ["/home/runner", "/home/runner/.cargo", "/home/runner/.rustup",
                                        "/home/runner/work/pw"])
        self.assertEqual(build_paths.builder_paths({"HOME": "/", "CARGO_HOME": "/c/"}, Path("/s")), ["/c", "/s"])
        with self.assertRaises(ValueError):
            build_paths.builder_paths({}, Path("relative"))

    def test_remapped_first_party_binaries_pass(self):
        self.binary("painted-wolf-code", b"\x7fELF /cargo/registry/src/tokio/src/lib.rs /source/src/main.rs")
        self.binary("pw", b"\x7fELF go build -trimpath")
        self.assertEqual(build_paths.check(self.tree, self.needles), ["painted-wolf-code", "pw"])

    def test_a_leaked_path_is_found_even_across_read_boundaries(self):
        leak = b"/home/runner/.cargo/registry/src/index/tokio/src/lib.rs"
        self.binary("bialy", b"x" * (build_paths.CHUNK - 10) + leak)
        self.binary("pw-document-core", b"/build-home/elsewhere")
        with self.assertRaisesRegex(ValueError, r"bialy: /home/runner, /home/runner/\.cargo$"):
            build_paths.check(self.tree, self.needles)

    def test_third_party_engines_and_missing_binaries_are_not_first_party_evidence(self):
        engine = self.tree / build_paths.INSTALL_ROOT / "engine-root/gitengine/bin"
        engine.mkdir(parents=True)
        (engine / "git").write_bytes(b"/home/runner/work/git")
        with self.assertRaisesRegex(ValueError, "no first-party executable"):
            build_paths.check(self.tree, self.needles)

    def test_the_command_line_uses_the_builder_environment(self):
        self.binary("painted-wolf-code", b"/home/tester/.cargo/registry/src/x.rs")
        command = ["python3", str(SCRIPTS / "linux_release/build_paths.py"), "--tree", str(self.tree),
                   "--source", str(self.tree)]
        environment = {"PATH": os.environ["PATH"], "HOME": "/home/tester"}
        refused = subprocess.run(command, env=environment, capture_output=True, text=True, timeout=30)
        self.assertEqual(refused.returncode, 1)
        self.assertIn("painted-wolf-code: /home/tester", refused.stderr)
        self.binary("painted-wolf-code", b"/cargo/registry/src/x.rs")
        accepted = subprocess.run(command, env=environment, capture_output=True, text=True, timeout=30)
        self.assertEqual(accepted.returncode, 0, accepted.stderr)


if __name__ == "__main__":
    unittest.main()
