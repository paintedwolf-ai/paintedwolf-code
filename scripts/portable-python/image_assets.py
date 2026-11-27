"""Asset bundling, third-party notices, and auditable WASI package patches
for scripts/portable-python/build-image.py.
"""

from __future__ import annotations

import tarfile
from pathlib import Path
from typing import Any, Callable


def install_bundled_fonts(
    lock: dict[str, Any],
    cache_dir: Path,
    packages_dir: Path,
    download_verified: Callable[[str, Path, str], Path],
    sha256_file: Callable[[Path], str],
) -> list[dict[str, Any]]:
    """Extract verified redistributable Unicode TrueType fonts and license notices."""
    bundles = lock.get("bundled_fonts") or []
    if not bundles:
        return []
    fonts_dir = packages_dir / "fonts"
    fonts_dir.mkdir(parents=True, exist_ok=True)
    manifest_fonts: list[dict[str, Any]] = []
    for bundle in bundles:
        archive = download_verified(
            bundle["source_url"],
            cache_dir / "fonts" / bundle["filename"],
            bundle["source_sha256"],
        )
        license_member = bundle["license_member"]
        license_dest_name = bundle["license_dest"]
        wanted = {f["archive_member"]: f for f in bundle["files"]}
        found_files: dict[str, bytes] = {}
        license_bytes: bytes | None = None
        mode = "r:bz2" if archive.name.endswith(".bz2") else "r:gz"
        with tarfile.open(archive, mode) as tf:
            for member in tf.getmembers():
                if not member.isfile():
                    continue
                if member.name == license_member:
                    fh = tf.extractfile(member)
                    assert fh is not None
                    license_bytes = fh.read()
                elif member.name in wanted:
                    fh = tf.extractfile(member)
                    assert fh is not None
                    found_files[member.name] = fh.read()
        if license_bytes is None:
            raise RuntimeError(f"Font archive {archive.name} missing license {license_member}")
        lic_out = fonts_dir / license_dest_name
        lic_out.write_bytes(license_bytes)
        for member_name, spec in wanted.items():
            if member_name not in found_files:
                raise RuntimeError(f"Font archive {archive.name} missing {member_name}")
            out_path = fonts_dir / spec["dest_name"]
            out_path.write_bytes(found_files[member_name])
            manifest_fonts.append(
                {
                    "family": spec["family"],
                    "style": spec["style"],
                    "image_path": f"packages/fonts/{spec['dest_name']}",
                    "guest_path": f"/packages/fonts/{spec['dest_name']}",
                    "sha256": sha256_file(out_path),
                    "license": bundle["license"],
                    "license_path": f"packages/fonts/{license_dest_name}",
                    "source_url": bundle["source_url"],
                }
            )
            print(f"[font] {spec['family']} {spec['style']} -> /packages/fonts/{spec['dest_name']}")
    return manifest_fonts


def write_third_party_notices(
    image_dir: Path,
    manifest_dists: list[dict[str, Any]],
    bundled_fonts: list[dict[str, Any]],
) -> str:
    """Write THIRD_PARTY_NOTICES.txt listing all bundled distributions, fonts, and native libs."""
    lines = [
        "Painted Wolf Portable Python — Third-Party Notices",
        "==================================================",
        "",
        "Interpreter & Built-in WASI Native Components:",
        "- CPython 3.12.0 (PSF-2.0)",
        "- WASI-SDK 20.0 / wasi-libc (Apache-2.0 WITH LLVM-exception / MIT)",
        "- zlib 1.3.1 (Zlib License) — built into WASI guest for PNG/DEFLATE support",
        "- bzip2 1.0.8 (bzip2-1.0.6 License) — built into WASI guest for bz2 compression",
        "- SQLite3 3.45.3 (Public Domain) — built into WASI guest for sqlite3 stdlib",
        "- libjpeg (IJG) 9f (IJG License) — built into WASI guest for Pillow JPEG codec",
        "- libxml2 2.12.9 & libxslt 1.1.42 (MIT) — built into WASI guest for lxml.etree XPath/XML support",
        "- Pillow 11.1.0 built-in PIL._imaging C extension (MIT-CMU)",
        "- lxml 5.3.0 built-in lxml.etree & lxml._elementpath Cython/C extensions (BSD-3-Clause)",
        "",
        "Bundled Python Distributions:",
    ]
    for d in sorted(manifest_dists, key=lambda x: x["normalized_name"]):
        vis = "public" if d["public"] else "dependency"
        lines.append(f"- {d['name']} {d['version']} ({d['license']}) [{vis}] — {d['source_url']}")
    if bundled_fonts:
        lines.append("")
        lines.append("Bundled Redistributable Fonts:")
        for f in bundled_fonts:
            lines.append(
                f"- {f['family']} {f['style']} ({f['license']}) at {f['guest_path']} "
                f"(license text: {f['license_path']}) — {f['source_url']}"
            )
    lgpl_dists = [
        d for d in sorted(manifest_dists, key=lambda x: x["normalized_name"])
        if "LGPL" in d.get("license", "").upper()
    ]
    if lgpl_dists:
        lines.extend([
            "",
            "LGPL-3.0 Guest-Library Distribution & Source Availability:",
            "  The Portable Python WASI guest image is distributed separately from host",
            "  sidecar binaries (e.g. Git, OpenGrep). The following guest Python libraries",
            "  are licensed under the GNU Lesser General Public License v3:",
        ])
        for d in lgpl_dists:
            lines.append(f"  - {d['name']} {d['version']} ({d['license']}) — {d['source_url']}")
        lines.extend([
            "  Complete corresponding source archives, license texts, copyright notices,",
            "  local patches (including the fpdf2 WASI URL-loader refusal patch), build",
            "  scripts, and recipient relink/rebuild instructions are provided with the",
            "  release artifacts and in scripts/portable-python/LGPL_COMPLIANCE.md.",
        ])
    lines.append("")
    out = image_dir / "THIRD_PARTY_NOTICES.txt"
    out.write_text("\n".join(lines), encoding="utf-8")
    return "THIRD_PARTY_NOTICES.txt"


def _refresh_record_entry(
    packages_root: Path,
    dist_info_glob: str,
    rel_path: str,
    target_file: Path,
    record_hash: Callable[[Path], tuple[str, int]],
) -> None:
    h, sz = record_hash(target_file)
    for dist_info in packages_root.glob(dist_info_glob):
        rec = dist_info / "RECORD"
        if rec.is_file():
            lines = rec.read_text(encoding="utf-8").splitlines()
            updated = [
                f"{rel_path},{h},{sz}" if line.startswith(f"{rel_path},") else line
                for line in lines
            ]
            rec.write_text("\n".join(updated) + "\n", encoding="utf-8")


def apply_wasi_package_patches(
    packages_root: Path,
    record_hash: Callable[[Path], tuple[str, int]],
) -> list[dict[str, str]]:
    """Apply minimal, auditable WASI compatibility patches and refresh RECORD."""
    patches: list[dict[str, str]] = []

    # Patch 1: pytest 8.4.1 _pytest/compat.py checks win32/emscripten for os.getuid()
    # but omits wasi, breaking the tmp_path fixture on wasm32-wasi.
    compat_py = packages_root / "_pytest" / "compat.py"
    if compat_py.is_file():
        old = 'if sys.platform == "win32" or sys.platform == "emscripten":'
        new = 'if sys.platform in ("win32", "emscripten", "wasi"):'
        text = compat_py.read_text(encoding="utf-8")
        if old in text:
            compat_py.write_text(text.replace(old, new, 1), encoding="utf-8")
            _refresh_record_entry(packages_root, "pytest-*.dist-info", "_pytest/compat.py", compat_py, record_hash)
            patches.append(
                {
                    "target": "packages/_pytest/compat.py",
                    "reason": "Treat sys.platform == 'wasi' like emscripten/win32 in get_user_id() so tmp_path fixture works without os.getuid()",
                }
            )

    # Patch 2: sympy 1.14.0 sympy/external/gmpy.py imports ctypes.c_long/sizeof
    # solely to compute LONG_MAX, but wasm32-wasi CPython omits _ctypes/libffi.
    gmpy_py = packages_root / "sympy" / "external" / "gmpy.py"
    if gmpy_py.is_file():
        old_imp = "from ctypes import c_long, sizeof"
        old_max = "LONG_MAX = (1 << (8*sizeof(c_long) - 1)) - 1"
        text = gmpy_py.read_text(encoding="utf-8")
        if old_imp in text and old_max in text:
            updated_text = text.replace(old_imp, "import struct as _struct", 1).replace(
                old_max, "LONG_MAX = (1 << (8*_struct.calcsize('l') - 1)) - 1", 1
            )
            gmpy_py.write_text(updated_text, encoding="utf-8")
            _refresh_record_entry(packages_root, "sympy-*.dist-info", "sympy/external/gmpy.py", gmpy_py, record_hash)
            patches.append(
                {
                    "target": "packages/sympy/external/gmpy.py",
                    "reason": "Compute LONG_MAX via stdlib struct.calcsize('l') instead of ctypes.sizeof(c_long) so SymPy imports cleanly on wasm32-wasi without _ctypes",
                }
            )

    # Patch 3: fpdf2 2.9.0 fpdf/image_parsing.py unconditionally imports
    # urllib.request.HTTPSHandler and subclasses http.client.HTTPSConnection,
    # which CPython omits on wasm32-wasi (no _ssl). Instead of falling back to
    # plain HTTP classes, replace missing HTTPS classes with fail-closed
    # refusing stubs and explicitly refuse all remote URL image fetches so
    # local file/BytesIO/PIL image loading works with zero unmediated network
    # activity or HTTP downgrade.
    img_parsing_py = packages_root / "fpdf" / "image_parsing.py"
    if img_parsing_py.is_file():
        old_import_block = (
            "from urllib.request import (\n"
            "    HTTPHandler,\n"
            "    HTTPRedirectHandler,\n"
            "    HTTPSHandler,\n"
            "    ProxyHandler,\n"
            "    Request,\n"
            "    build_opener,\n"
            ")"
        )
        new_import_block = (
            "from urllib.request import (\n"
            "    BaseHandler as _PWBaseHandler,\n"
            "    HTTPHandler,\n"
            "    HTTPRedirectHandler,\n"
            "    ProxyHandler,\n"
            "    Request,\n"
            "    build_opener,\n"
            ")\n"
            "_PW_FPDF_URL_DISABLED_MSG = (\n"
            '    "fpdf2 URL image loading is disabled in Portable Python; "\n'
            '    "pass a local file path, BytesIO, or PIL Image"\n'
            ")\n"
            "try:\n"
            "    from urllib.request import HTTPSHandler\n"
            "except ImportError:\n"
            "    class HTTPSHandler(_PWBaseHandler):\n"
            "        def https_open(self, req):\n"
            "            raise FPDFResourceAccessError(_PW_FPDF_URL_DISABLED_MSG)\n"
            "_PW_HTTPS_CONNECTION_BASE = getattr(http.client, \"HTTPSConnection\", None)\n"
            "if _PW_HTTPS_CONNECTION_BASE is None:\n"
            "    class _PW_HTTPS_CONNECTION_BASE:\n"
            "        def __init__(self, *args, **kwargs):\n"
            "            raise FPDFResourceAccessError(_PW_FPDF_URL_DISABLED_MSG)"
        )
        old_validate = (
            "def _validate_remote_url_access(\n"
            "    url: str,\n"
            "    resource_access_policy: ResourceAccessPolicy,\n"
            ") -> tuple[ipaddress.IPv4Address | ipaddress.IPv6Address, ...]:\n"
            "    parsed_url = urlsplit(url)"
        )
        new_validate = (
            "def _validate_remote_url_access(\n"
            "    url: str,\n"
            "    resource_access_policy: ResourceAccessPolicy,\n"
            ") -> tuple[ipaddress.IPv4Address | ipaddress.IPv6Address, ...]:\n"
            "    raise FPDFResourceAccessError(f\"{_PW_FPDF_URL_DISABLED_MSG}: {url!r}\")\n"
            "    parsed_url = urlsplit(url)"
        )
        old_opener = (
            "def _build_remote_resource_opener(\n"
            "    resource_access_policy: ResourceAccessPolicy,\n"
            "    pinned_addresses_by_url: dict[\n"
            "        str, tuple[ipaddress.IPv4Address | ipaddress.IPv6Address, ...]\n"
            "    ],\n"
            ") -> Any:\n"
            "    return build_opener("
        )
        new_opener = (
            "def _build_remote_resource_opener(\n"
            "    resource_access_policy: ResourceAccessPolicy,\n"
            "    pinned_addresses_by_url: dict[\n"
            "        str, tuple[ipaddress.IPv4Address | ipaddress.IPv6Address, ...]\n"
            "    ],\n"
            ") -> Any:\n"
            "    raise FPDFResourceAccessError(_PW_FPDF_URL_DISABLED_MSG)\n"
            "    return build_opener("
        )
        text = img_parsing_py.read_text(encoding="utf-8")
        if old_import_block in text and old_validate in text and old_opener in text:
            patched_text = (
                text.replace(old_import_block, new_import_block, 1)
                .replace("http.client.HTTPSConnection", "_PW_HTTPS_CONNECTION_BASE")
                .replace(old_validate, new_validate, 1)
                .replace(old_opener, new_opener, 1)
            )
            img_parsing_py.write_text(patched_text, encoding="utf-8")
            _refresh_record_entry(packages_root, "fpdf2-*.dist-info", "fpdf/image_parsing.py", img_parsing_py, record_hash)
            patches.append(
                {
                    "target": "packages/fpdf/image_parsing.py",
                    "reason": "Replace absent WASI HTTPS classes with fail-closed refusing stubs and explicitly refuse all remote URL image fetches without falling back to plain HTTP",
                }
            )

    # Patch 4: PyJWT 2.15.1 jwt/jwks_client.py unconditionally imports
    # ssl.SSLContext at module import time, breaking offline `import jwt`
    # on wasm32-wasi (no _ssl). Guard SSLContext and fail closed in fetch_data
    # if HTTPSHandler is unavailable (no plain-HTTP downgrade).
    jwks_py = packages_root / "jwt" / "jwks_client.py"
    if jwks_py.is_file():
        old_ssl = "from ssl import SSLContext"
        new_ssl = (
            "try:\n"
            "    from ssl import SSLContext\n"
            "except ImportError:\n"
            "    SSLContext = object  # type: ignore[misc,assignment]"
        )
        old_fetch = (
            "            handlers: list[Any] = [_NoRedirectHandler()]\n"
            "            if self.ssl_context is not None:\n"
            "                handlers.append(urllib.request.HTTPSHandler(context=self.ssl_context))"
        )
        new_fetch = (
            "            if not hasattr(urllib.request, \"HTTPSHandler\"):\n"
            "                raise PyJWKClientConnectionError(\n"
            "                    \"PyJWKClient remote HTTPS JWKS fetching is disabled in Portable Python without _ssl; \"\n"
            "                    \"use offline key verification or mediated requests\"\n"
            "                )\n"
            "            handlers: list[Any] = [_NoRedirectHandler()]\n"
            "            if self.ssl_context is not None:\n"
            "                handlers.append(urllib.request.HTTPSHandler(context=self.ssl_context))"
        )
        text = jwks_py.read_text(encoding="utf-8")
        if old_ssl in text and old_fetch in text:
            patched_text = text.replace(old_ssl, new_ssl, 1).replace(old_fetch, new_fetch, 1)
            jwks_py.write_text(patched_text, encoding="utf-8")
            _refresh_record_entry(packages_root, "pyjwt-*.dist-info", "jwt/jwks_client.py", jwks_py, record_hash)
            patches.append(
                {
                    "target": "packages/jwt/jwks_client.py",
                    "reason": "Guard absent WASI ssl.SSLContext so offline import jwt succeeds and fail closed in PyJWKClient.fetch_data without plain-HTTP downgrade",
                }
            )

    # Patch 5: xlrd 2.0.2 xlrd/book.py unconditionally imports mmap, which
    # CPython omits on wasm32-wasi. Fall back to standard file reads (use_mmap=False).
    xlrd_book_py = packages_root / "xlrd" / "book.py"
    if xlrd_book_py.is_file():
        old_mmap = "\nimport mmap\n"
        new_mmap = "\ntry:\n    import mmap\nexcept ImportError:\n    mmap = None\n"
        old_use = "        self.use_mmap = use_mmap\n"
        new_use = "        self.use_mmap = bool(use_mmap and mmap is not None)\n"
        text = xlrd_book_py.read_text(encoding="utf-8")
        if old_mmap in text and old_use in text:
            patched_text = text.replace(old_mmap, new_mmap, 1).replace(old_use, new_use, 1)
            xlrd_book_py.write_text(patched_text, encoding="utf-8")
            _refresh_record_entry(packages_root, "xlrd-*.dist-info", "xlrd/book.py", xlrd_book_py, record_hash)
            patches.append(
                {
                    "target": "packages/xlrd/book.py",
                    "reason": "Guard absent WASI mmap module and disable use_mmap when mmap is unavailable so legacy .xls files read via standard file I/O",
                }
            )

    # Patch 6: icalendar 7.3.2 triggers a 22-level nested import chain
    # (icalendar -> ... -> dateutil.tz -> importlib.resources -> tempfile ->
    # shutil -> bz2 -> _compression) that exceeds wasm2c's call-stack depth
    # limit (WASM_RT_TRAP_EXHAUSTION). Pre-importing dateutil.tz at top level
    # keeps peak import nesting shallow (~10) with zero semantic change.
    ical_init_py = packages_root / "icalendar" / "__init__.py"
    if ical_init_py.is_file():
        marker = "import dateutil.tz as _pw_dateutil_tz  # noqa: F401"
        text = ical_init_py.read_text(encoding="utf-8")
        if marker not in text:
            ical_init_py.write_text(marker + "\n" + text, encoding="utf-8")
            _refresh_record_entry(packages_root, "icalendar-*.dist-info", "icalendar/__init__.py", ical_init_py, record_hash)
            patches.append(
                {
                    "target": "packages/icalendar/__init__.py",
                    "reason": "Pre-import dateutil.tz at shallow module depth so icalendar's transitive import chain stays well below wasm2c's call-stack depth limit",
                }
            )
    return patches
