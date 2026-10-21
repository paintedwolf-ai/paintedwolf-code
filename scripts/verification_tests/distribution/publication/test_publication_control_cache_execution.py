"""Control construction preserves source-only and signed-cache authority boundaries."""
import base64
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import Mock, patch

from linux_release.publication.control import control_cache as owner


class ControlCacheTests(unittest.TestCase):
    def fixture(self):
        source={"version":"1.0.2","source_commit":"a"*40,"flake_lock_sha256":"b"*64}
        request={"schema_version":1,"version":"1.0.2","application_commit":"a"*40,
                 "flake_lock_sha256":"b"*64,"system":"x86_64-linux","outputs":["/nix/store/"+"c"*32+"-app"]}
        report={"request":request,"status":"passed","content_verified":True,"closure_sha256":"d"*64,
                "closure":{request["outputs"][0]:{"nar_hash":"sha256-"+base64.b64encode(bytes(32)).decode()}}}
        return source,request,report

    def test_disabled_cache_uses_verified_source_without_signing_or_cache_claims(self):
        source,request,report=self.fixture()
        describe=Mock(return_value=report)
        with tempfile.TemporaryDirectory() as temporary,patch.object(owner,"load",return_value=None), \
                patch.dict("sys.modules",{"scripts.nix_cache.source":types.SimpleNamespace(describe_verified_source=describe)}):
            root=Path(temporary)
            artifacts,reports=owner.build([{"request":request}],root,source,rehearsal=False,
                                           architectures=["amd64"],source_repository=Path("/source"))
            describe.assert_called_once_with(request,Path("/source"),rehearsal=False)
            self.assertEqual(len(artifacts),5)
            self.assertFalse(reports[0]["cache_enabled"])
            self.assertFalse((root/"x86_64-linux/cache").exists())
            self.assertEqual({row["identity"]["sha256"] for row in artifacts},{"0"*64})

    def test_protected_disabled_state_refuses_signed_cache_input(self):
        source,request,_=self.fixture()
        request["mode"]="production"
        with tempfile.TemporaryDirectory() as temporary,patch.object(owner,"load",return_value=None):
            with self.assertRaisesRegex(ValueError,"activation"):
                owner.build([{"request":request,"policy":{},"private_key":"/key"}],Path(temporary),source,
                            rehearsal=False,architectures=["amd64"],source_repository=Path("/source"))

    def test_selected_source_and_every_native_cpu_remain_required(self):
        source,request,_=self.fixture()
        request["application_commit"]="e"*40
        with tempfile.TemporaryDirectory() as temporary,patch.object(owner,"load",return_value=None):
            with self.assertRaisesRegex(ValueError,"immutable application source"):
                owner.build([{"request":request}],Path(temporary),source,rehearsal=False,
                            architectures=["amd64"],source_repository=Path("/source"))
        with tempfile.TemporaryDirectory() as temporary,patch.object(owner,"load",return_value=None):
            with self.assertRaisesRegex(ValueError,"every selected CPU"):
                owner.build([],Path(temporary),source,rehearsal=False,
                            architectures=["amd64","arm64"],source_repository=Path("/source"))
