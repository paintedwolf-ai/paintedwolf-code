#!/usr/bin/env python3
"""Unit tests for scripts/portable-python/image_assets.py WASI patches."""

from __future__ import annotations

import hashlib
import base64
import tempfile
import unittest
from pathlib import Path

from image_assets import apply_wasi_package_patches


def _record_hash(path: Path) -> tuple[str, int]:
    data = path.read_bytes()
    digest = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b"=").decode("ascii")
    return f"sha256={digest}", len(data)


SAMPLE_FPDF_IMAGE_PARSING = """\
import http.client
import ipaddress
from typing import Any
from urllib.parse import urlsplit
from urllib.request import (
    HTTPHandler,
    HTTPRedirectHandler,
    HTTPSHandler,
    ProxyHandler,
    Request,
    build_opener,
)

class ResourceAccessPolicy:
    REMOTE_PUBLIC = 1
    REMOTE_PRIVATE = 2

class FPDFResourceAccessError(Exception):
    pass

def _validate_remote_url_access(
    url: str,
    resource_access_policy: ResourceAccessPolicy,
) -> tuple[ipaddress.IPv4Address | ipaddress.IPv6Address, ...]:
    parsed_url = urlsplit(url)
    raise AssertionError("should never reach DNS/network validation")

class _PinnedRemoteHTTPConnection(http.client.HTTPConnection):
    pass

class _PinnedRemoteHTTPSConnection(
    _PinnedRemoteHTTPConnection, http.client.HTTPSConnection
):
    pass

def _build_remote_resource_opener(
    resource_access_policy: ResourceAccessPolicy,
    pinned_addresses_by_url: dict[
        str, tuple[ipaddress.IPv4Address | ipaddress.IPv6Address, ...]
    ],
) -> Any:
    return build_opener()
"""


class TestImageAssetsFpdfPatch(unittest.TestCase):
    def test_fpdf_patch_refuses_urls_without_http_downgrade(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            pkg_root = Path(td)
            fpdf_dir = pkg_root / "fpdf"
            dist_info = pkg_root / "fpdf2-2.9.0.dist-info"
            fpdf_dir.mkdir()
            dist_info.mkdir()

            target = fpdf_dir / "image_parsing.py"
            target.write_text(SAMPLE_FPDF_IMAGE_PARSING, encoding="utf-8")
            (dist_info / "RECORD").write_text("fpdf/image_parsing.py,sha256=old,1\n", encoding="utf-8")

            patches = apply_wasi_package_patches(pkg_root, _record_hash)
            self.assertEqual(len(patches), 1)
            self.assertEqual(patches[0]["target"], "packages/fpdf/image_parsing.py")

            patched = target.read_text(encoding="utf-8")
            self.assertNotIn("HTTPSHandler = HTTPHandler", patched)
            self.assertNotIn("http.client.HTTPSConnection", patched)
            self.assertNotIn("getattr(http.client, \"HTTPSConnection\", http.client.HTTPConnection)", patched)
            self.assertIn("_PW_FPDF_URL_DISABLED_MSG", patched)

            # Execute patched module in a namespace and verify both http:// and https:// are refused.
            ns: dict[str, object] = {}
            exec(compile(patched, str(target), "exec"), ns)
            err_cls = ns["FPDFResourceAccessError"]
            validate = ns["_validate_remote_url_access"]
            opener = ns["_build_remote_resource_opener"]

            for url in ("https://example.com/pic.png", "http://example.com/pic.png"):
                with self.assertRaises(err_cls) as ctx:  # type: ignore[arg-type]
                    validate(url, ns["ResourceAccessPolicy"])  # type: ignore[operator]
                self.assertIn("disabled in Portable Python", str(ctx.exception))

            with self.assertRaises(err_cls) as ctx:  # type: ignore[arg-type]
                opener(ns["ResourceAccessPolicy"], {})  # type: ignore[operator]
            self.assertIn("disabled in Portable Python", str(ctx.exception))

            # Verify RECORD was refreshed with the new SHA-256.
            new_h, new_sz = _record_hash(target)
            rec = (dist_info / "RECORD").read_text(encoding="utf-8")
            self.assertIn(f"fpdf/image_parsing.py,{new_h},{new_sz}", rec)

    def test_pyjwt_xlrd_and_icalendar_wasi_patches(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            pkg_root = Path(td)
            # PyJWT fixture
            (pkg_root / "jwt").mkdir()
            (pkg_root / "pyjwt-2.15.1.dist-info").mkdir()
            jwks = pkg_root / "jwt" / "jwks_client.py"
            jwks.write_text(
                "import urllib.request\nfrom ssl import SSLContext\nfrom typing import Any\n"
                "class _NoRedirectHandler: pass\n"
                "class PyJWKClientConnectionError(Exception): pass\n"
                "class C:\n"
                "    ssl_context = None\n"
                "    def fetch_data(self):\n"
                "        try:\n"
                "            handlers: list[Any] = [_NoRedirectHandler()]\n"
                "            if self.ssl_context is not None:\n"
                "                handlers.append(urllib.request.HTTPSHandler(context=self.ssl_context))\n"
                "        except Exception:\n"
                "            raise\n",
                encoding="utf-8",
            )
            (pkg_root / "pyjwt-2.15.1.dist-info" / "RECORD").write_text("jwt/jwks_client.py,sha256=old,1\n", encoding="utf-8")

            # xlrd fixture
            (pkg_root / "xlrd").mkdir()
            (pkg_root / "xlrd-2.0.2.dist-info").mkdir()
            book = pkg_root / "xlrd" / "book.py"
            book.write_text(
                "DEBUG = 0\n\nimport mmap\n\nclass Book:\n    def open(self, use_mmap=True):\n        self.use_mmap = use_mmap\n",
                encoding="utf-8",
            )
            (pkg_root / "xlrd-2.0.2.dist-info" / "RECORD").write_text("xlrd/book.py,sha256=old,1\n", encoding="utf-8")

            # icalendar fixture
            (pkg_root / "icalendar").mkdir()
            (pkg_root / "icalendar-7.3.2.dist-info").mkdir()
            ical = pkg_root / "icalendar" / "__init__.py"
            ical.write_text("from icalendar.cal import Calendar\n", encoding="utf-8")
            (pkg_root / "icalendar-7.3.2.dist-info" / "RECORD").write_text("icalendar/__init__.py,sha256=old,1\n", encoding="utf-8")

            patches = apply_wasi_package_patches(pkg_root, _record_hash)
            targets = {p["target"] for p in patches}
            self.assertEqual(
                targets,
                {
                    "packages/jwt/jwks_client.py",
                    "packages/xlrd/book.py",
                    "packages/icalendar/__init__.py",
                },
            )
            self.assertIn("except ImportError:\n    SSLContext = object", jwks.read_text(encoding="utf-8"))
            self.assertIn("PyJWKClientConnectionError(", jwks.read_text(encoding="utf-8"))
            self.assertIn("self.use_mmap = bool(use_mmap and mmap is not None)", book.read_text(encoding="utf-8"))
            self.assertTrue(ical.read_text(encoding="utf-8").startswith("import dateutil.tz as _pw_dateutil_tz"))


if __name__ == "__main__":
    unittest.main()
