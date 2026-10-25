"""Staging must verify every pinned language, including additional payloads."""

import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import tarfile
import unittest

ROOT = Path(__file__).resolve().parents[3]


class OCRLanguagePayloadTests(unittest.TestCase):
    def test_cached_tree_checks_each_language_before_reuse(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "scripts").mkdir()
            shutil.copyfile(ROOT / "scripts/ocrengine-fetch.sh", root / "scripts/ocrengine-fetch.sh")
            pin_dir = root / "lycaon/config/runtime/ocrengine"
            pin_dir.mkdir(parents=True)
            destination = root / "staged"
            (destination / "bin").mkdir(parents=True)
            (destination / "tessdata").mkdir()
            binary = destination / "bin/tesseract"
            binary.write_bytes(b"#!/bin/sh\nexit 0\n")
            binary.chmod(0o755)
            (destination / "VERSION").write_text("fixture")
            data = []
            for language in ("eng", "osd", "deu"):
                payload = destination / ("tessdata/" + language + ".traineddata")
                payload.write_bytes(language.encode())
                data.append(f"  {language}_traineddata:\n    sha256: {hashlib.sha256(payload.read_bytes()).hexdigest()}\n    bytes: 3\n")
            pin = ("engine_version: fixture\nproducer:\n  repository: fixture/ocr\n  tag: vfixture\n"
                   "recognition_languages: [eng, deu]\ntrained_data:\n" + "".join(data) +
                   "platforms:\n  linux-amd64:\n    url: https://github.com/fixture/ocr/releases/download/vfixture/engine.tar.gz\n"
                   "    sha256: " + "a" * 64 + "\n    bytes: 1\n    binary_sha256: " +
                   hashlib.sha256(binary.read_bytes()).hexdigest() + f"\n    binary_bytes: {binary.stat().st_size}\n")
            (pin_dir / "pin.yaml").write_text(pin)
            command = ["bash", str(root / "scripts/ocrengine-fetch.sh"), "--target", "x86_64-unknown-linux-gnu",
                       "--archive", str(root / "absent-offline-archive")]
            environment = {**os.environ, "OCRENGINE_DEST": str(destination)}
            result = subprocess.run(command, env=environment, text=True, capture_output=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("already at fixture", result.stderr)
            (destination / "tessdata/deu.traineddata").write_bytes(b"bad")
            result = subprocess.run(command, env=environment, text=True, capture_output=True, timeout=10)
            self.assertNotEqual(result.returncode, 0, "an altered extra language was accepted as verified")

    def test_offline_supplemental_import_is_verified_before_replacing_destination(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "scripts").mkdir()
            shutil.copyfile(ROOT / "scripts/ocrengine-fetch.sh", root / "scripts/ocrengine-fetch.sh")
            pin_dir = root / "lycaon/config/runtime/ocrengine"
            pin_dir.mkdir(parents=True)
            producer = root / "producer/ocr"
            (producer / "bin").mkdir(parents=True)
            (producer / "tessdata").mkdir()
            binary = producer / "bin/tesseract"
            binary.write_bytes(b"#!/bin/sh\nexit 0\n")
            binary.chmod(0o755)
            (producer / "VERSION").write_text("fixture")
            models = root / "models"
            models.mkdir()
            entries = []
            for language in ("eng", "osd", "deu"):
                payload = (models if language == "deu" else producer / "tessdata") / (language + ".traineddata")
                payload.write_bytes(language.encode())
                source = "    source: tessdata-fast\n" if language == "deu" else ""
                entries.append(f"  {language}_traineddata:\n{source}    sha256: {hashlib.sha256(payload.read_bytes()).hexdigest()}\n    bytes: 3\n")
            archive = root / "producer.tar.gz"
            with tarfile.open(archive, "w:gz") as stream:
                stream.add(producer, arcname="ocr")
            pin = ("engine_version: fixture\nproducer:\n  repository: fixture/ocr\n  tag: vfixture\n"
                   "trained_data_source:\n  repository: tesseract-ocr/tessdata_fast\n  commit: " + "a" * 40 +
                   "\nrecognition_languages: [eng, deu]\ntrained_data:\n" + "".join(entries) +
                   "platforms:\n  linux-amd64:\n    url: https://github.com/fixture/ocr/releases/download/vfixture/engine.tar.gz\n"
                   "    sha256: " + hashlib.sha256(archive.read_bytes()).hexdigest() +
                   f"\n    bytes: {archive.stat().st_size}\n    binary_sha256: " + hashlib.sha256(binary.read_bytes()).hexdigest() +
                   f"\n    binary_bytes: {binary.stat().st_size}\n")
            (pin_dir / "pin.yaml").write_text(pin)
            destination = root / "staged"
            command = ["bash", str(root / "scripts/ocrengine-fetch.sh"), "--target", "x86_64-unknown-linux-gnu",
                       "--archive", str(archive), "--trained-data-dir", str(models)]
            environment = {**os.environ, "OCRENGINE_DEST": str(destination)}
            result = subprocess.run(command, env=environment, text=True, capture_output=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((destination / "tessdata/deu.traineddata").read_bytes(), b"deu")
            (destination / "VERSION").write_text("previous")
            (models / "deu.traineddata").write_bytes(b"bad")
            result = subprocess.run(command, env=environment, text=True, capture_output=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual((destination / "VERSION").read_text(), "previous")
            (models / "deu.traineddata").unlink()
            result = subprocess.run(command, env=environment, text=True, capture_output=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual((destination / "VERSION").read_text(), "previous")
