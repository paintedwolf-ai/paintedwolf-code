"""Control retention neither activates repositories nor accepts incomplete package grids."""
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from linux_release.publication import controls as owner
from linux_release.publication import control_packages as packages


class ControlRetentionTests(unittest.TestCase):
    def fixture(self, root):
        inputs=root/"inputs"
        inputs.mkdir()
        (inputs/"app.deb").write_bytes(b"candidate")
        row={"id":"app.deb","installation":"deb","architecture":"amd64","source_commit":"a"*40,
             "identity":{"kind":"file","sha256":hashlib.sha256(b"candidate").hexdigest()}}
        manifest={"format_version":1,"source_commit":"a"*40,"artifacts":[row]}
        return inputs,row,manifest

    def test_retained_candidate_bytes_are_independent_and_exact(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)
            inputs,row,manifest=self.fixture(root)
            result=packages.stage(manifest,inputs,root/"packages",{"source_commit":"a"*40},["deb"],["amd64"])
            self.assertEqual(result,{"app.deb":row})
            self.assertEqual((root/"packages/app.deb").read_bytes(),b"candidate")
            (inputs/"app.deb").write_bytes(b"changed")
            self.assertEqual((root/"packages/app.deb").read_bytes(),b"candidate")

    def test_missing_cpu_and_tampered_package_refuse(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)
            inputs,_,manifest=self.fixture(root)
            with self.assertRaisesRegex(ValueError,"every selected family and CPU"):
                packages.stage(manifest,inputs,root/"missing",{"source_commit":"a"*40},["deb"],["amd64","arm64"])
            (inputs/"app.deb").write_bytes(b"tampered")
            with self.assertRaisesRegex(ValueError,"immutable bytes"):
                packages.stage(manifest,inputs,root/"tampered",{"source_commit":"a"*40},["deb"],["amd64"])

    def test_alias_conversion_requires_exact_owned_signed_inventory(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)/"generation"
            root.mkdir()
            target=root/"repo.db.tar.zst"
            target.write_bytes(b"signed database")
            alias=root/"repo.db"
            alias.symlink_to(target.name)
            manifest={"objects":[{"path":alias.name,"sha256":hashlib.sha256(target.read_bytes()).hexdigest(),"size":target.stat().st_size}]}
            packages.regular_aliases(root,manifest)
            self.assertFalse(alias.is_symlink())
            self.assertEqual(alias.read_bytes(),target.read_bytes())
            alias.unlink()
            alias.symlink_to("../outside")
            (root.parent/"outside").write_bytes(b"outside")
            try:
                with self.assertRaises(ValueError):
                    packages.regular_aliases(root,manifest)
            finally:
                (root.parent/"outside").unlink()

    def test_failed_assembly_removes_only_its_fresh_output(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary)
            inputs,_,manifest=self.fixture(root)
            with patch.object(owner,"selection",return_value=({"generation":"fixture","fingerprint":"fixture"},None)), \
                    patch.object(owner,"assemble",side_effect=ValueError("signature failed")):
                with self.assertRaisesRegex(ValueError,"signature failed"):
                    owner.build(manifest,inputs,root/"controls",{"source_commit":"a"*40,"source_tree":"b"*40},
                                root/"catalog",root/"private","https://downloads.invalid",{},[],producer=None,
                                source_repository=root,rehearsal=True,families=["deb"],architectures=["amd64"])
            self.assertFalse((root/"controls").exists())
            self.assertEqual((inputs/"app.deb").read_bytes(),b"candidate")
