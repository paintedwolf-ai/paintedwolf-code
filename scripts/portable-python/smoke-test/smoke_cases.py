"""Comprehensive guest smoke test for all 13 public launch distributions + stdlib."""

import io
import os
import sys
import traceback
from importlib import metadata, resources
from pathlib import Path

sys.path.insert(0, "/packages")

FAILURES = []


def check(name, fn):
    try:
        fn()
        print(f"  {name}: PASS")
    except Exception as e:
        FAILURES.append((name, f"{type(e).__name__}: {e}"))
        print(f"  {name}: FAIL ({type(e).__name__}: {e})")
        traceback.print_exc()


def test_stdlib():
    import ast
    import base64
    import csv
    import datetime
    import decimal
    import difflib
    import fractions
    import hashlib
    import json
    import sqlite3
    import tokenize
    import tomllib
    import unittest
    import xml.etree.ElementTree as ET
    import zipfile
    import zlib

    assert zlib.decompress(zlib.compress(b"portable-python")) == b"portable-python"
    assert len(hashlib.sha256(b"portable-python").hexdigest()) == 64
    conn = sqlite3.connect(":memory:")
    assert conn.execute("select 21 * 2").fetchone()[0] == 42
    assert tomllib.loads('[tool]\nname = "wolf"\n')["tool"]["name"] == "wolf"
    rows = list(csv.DictReader(io.StringIO("k,v\na,1\nb,2\n")))
    assert rows == [{"k": "a", "v": "1"}, {"k": "b", "v": "2"}]
    assert json.loads(json.dumps({"ok": True}))["ok"] is True
    assert ET.fromstring("<root><item>x</item></root>").find("item").text == "x"
    zbuf = io.BytesIO()
    with zipfile.ZipFile(zbuf, "w", compression=zipfile.ZIP_DEFLATED) as zf:
        zf.writestr("hello.txt", "world")
    zbuf.seek(0)
    with zipfile.ZipFile(zbuf, "r") as zf:
        assert zf.read("hello.txt") == b"world"
    assert isinstance(ast.parse("x = 1 + 2"), ast.Module)
    toks = list(tokenize.generate_tokens(io.StringIO("x = 1").readline))
    assert any(t.string == "x" for t in toks)
    assert list(difflib.unified_diff(["a\n"], ["b\n"]))
    assert fractions.Fraction(1, 3) + fractions.Fraction(1, 6) == fractions.Fraction(1, 2)
    assert decimal.Decimal("0.1") + decimal.Decimal("0.2") == decimal.Decimal("0.3")
    assert datetime.date(2026, 11, 26).isoformat() == "2026-11-26"
    assert base64.b64decode(base64.b64encode(b"wolf")) == b"wolf"
    assert hasattr(unittest, "TestCase")


def test_pyyaml():
    import yaml

    assert yaml.__with_libyaml__ is False
    docs = list(yaml.safe_load_all("a: 1\n---\nb: [2, 3]\n"))
    assert docs == [{"a": 1}, {"b": [2, 3]}]
    try:
        yaml.safe_load("!!python/object/apply:os.system ['echo bad']")
        raise AssertionError("unsafe tag should have been rejected")
    except yaml.YAMLError:
        pass
    dumped = yaml.safe_dump({"name": "wolf", "items": [1, 2]})
    assert yaml.safe_load(dumped) == {"name": "wolf", "items": [1, 2]}


def test_openpyxl():
    import zipfile
    from openpyxl import Workbook, load_workbook
    from openpyxl.styles import Font

    wb = Workbook()
    ws = wb.active
    ws.title = "Roster"
    ws["A1"] = "Item"
    ws["B1"] = "Value"
    ws["A1"].font = Font(bold=True)
    ws["A2"] = "alpha"
    ws["B2"] = "=2+3"
    ws.column_dimensions["A"].width = 18
    ws.freeze_panes = "A2"
    ws2 = wb.create_sheet("Summary")
    ws2["A1"] = "total"

    # BytesIO round-trip
    buf = io.BytesIO()
    wb.save(buf)
    buf.seek(0)
    wb_re = load_workbook(buf)
    assert wb_re.sheetnames == ["Roster", "Summary"]
    rows = list(wb_re["Roster"].iter_rows(values_only=True))
    assert rows[1] == ("alpha", "=2+3")
    assert wb_re["Roster"]["A1"].font.bold is True
    assert wb_re["Roster"].freeze_panes == "A2"

    # Path round-trip + ZIP check
    xlsx_path = Path("/tmp/smoke_roster.xlsx")
    wb.save(xlsx_path)
    with zipfile.ZipFile(xlsx_path, "r") as zf:
        assert "xl/worksheets/sheet1.xml" in zf.namelist()
    wb_disk = load_workbook(xlsx_path)
    assert wb_disk["Roster"]["B2"].value == "=2+3"


def test_pytest():
    import pytest

    os.chdir("/tmp")
    tfile = Path("/tmp/test_inproc_smoke.py")
    tfile.write_text(
        "def test_ok(tmp_path, monkeypatch, caplog):\n"
        "    monkeypatch.setenv('PP_TEST', '1')\n"
        "    p = tmp_path / 'f.txt'\n"
        "    p.write_text('hi')\n"
        "    assert p.read_text() == 'hi'\n"
        "\n"
        "def test_fail():\n"
        "    assert 1 == 2\n",
        encoding="utf-8",
    )
    code_ok = pytest.main([
        "-q", "--capture=sys", "-p", "no:cacheprovider", "-p", "no:faulthandler",
        "-o", "log_file=/tmp/inproc-ok.log", "--rootdir=/tmp",
        f"{tfile}::test_ok",
    ])
    assert int(code_ok) == 0, f"expected 0, got {code_ok}"
    code_fail = pytest.main([
        "-q", "--capture=sys", "-p", "no:cacheprovider", "-p", "no:faulthandler",
        "-o", "log_file=/tmp/inproc-fail.log", "--rootdir=/tmp",
        f"{tfile}::test_fail",
    ])
    assert int(code_fail) == 1, f"expected 1, got {code_fail}"


def test_tomlkit():
    import tomlkit
    import tomllib

    src = '# header\n[project]\nname = "demo" # inline\nversion = "1.0.0"\n'
    doc = tomlkit.parse(src)
    doc["project"]["version"] = "1.1.0"
    rendered = tomlkit.dumps(doc)
    assert "# header" in rendered
    assert "# inline" in rendered
    assert 'name = "demo"' in rendered
    assert tomllib.loads(rendered)["project"]["version"] == "1.1.0"


def test_ruamel_yaml():
    from ruamel.yaml import YAML

    y = YAML(typ="rt", pure=True)
    y.preserve_quotes = True
    src = '# top comment\ndefaults: &def\n  role: "worker"\nservice:\n  <<: *def\n  name: "wolf"\n'
    doc = y.load(src)
    doc["service"]["name"] = "lynx"
    out = io.StringIO()
    y.dump(doc, out)
    text = out.getvalue()
    assert "# top comment" in text
    assert 'name: "lynx"' in text
    assert "&def" in text and "*def" in text


def test_packaging():
    from packaging.markers import Marker
    from packaging.requirements import Requirement
    from packaging.version import Version

    req = Requirement("demo>=2.0,<3.0; python_version >= '3.12'")
    assert Version("2.5.1") in req.specifier
    assert Version("3.0.0") not in req.specifier
    assert Marker("python_version >= '3.12'").evaluate({"python_version": "3.12"})


def test_jsonschema():
    import jsonschema
    from jsonschema import Draft202012Validator
    from referencing import Registry, Resource
    from referencing.exceptions import Unresolvable

    # Verify rpds pure-Python WASI backend works
    from rpds import HashTrieMap, HashTrieSet, List, Queue

    m = HashTrieMap({"a": 1}).insert("b", 2)
    assert m["a"] == 1 and m["b"] == 2 and len(m.remove("a")) == 1
    s = HashTrieSet(["x"]).insert("y")
    assert "x" in s and "y" in s
    lst = List().push_front("b").push_front("a")
    assert list(lst) == ["a", "b"] and lst.first == "a"
    q = Queue().enqueue(1).enqueue(2)
    assert q.peek == 1 and list(q.dequeue()) == [2]

    # Local $ref registry with Draft 2020-12
    item_schema = Resource.from_contents({
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "$id": "urn:example:item",
        "type": "object",
        "properties": {"count": {"type": "integer", "minimum": 1}},
        "required": ["count"],
    })
    root_schema = {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "properties": {"item": {"$ref": "urn:example:item"}},
        "required": ["item"],
    }
    registry = Registry().with_resource("urn:example:item", item_schema)
    validator = Draft202012Validator(root_schema, registry=registry)
    assert list(validator.iter_errors({"item": {"count": 3}})) == []
    errs = list(validator.iter_errors({"item": {"count": 0}}))
    assert len(errs) == 1
    assert list(errs[0].path) == ["item", "count"]

    # Unknown remote reference raises bounded Unresolvable (no implicit network fetch)
    bad_validator = Draft202012Validator(
        {"$ref": "https://example.invalid/unknown.json"},
        registry=Registry(),
    )
    try:
        bad_validator.validate({})
        raise AssertionError("unknown remote $ref should fail")
    except Unresolvable:
        pass


def test_asttokens():
    from asttokens import ASTTokens

    src = "# café\nvalue = old_expr + 1  # keep\n"
    atok = ASTTokens(src, parse=True)
    node = atok.tree.body[0].value
    assert atok.get_text(node) == "old_expr + 1"
    start, end = atok.get_text_range(node)
    patched = src[:start] + "new_expr" + src[end:]
    assert patched == "# café\nvalue = new_expr  # keep\n"


def test_jinja2():
    import markupsafe
    from jinja2 import DictLoader, Environment, StrictUndefined, UndefinedError

    assert "Markup" in dir(markupsafe)
    env = Environment(
        loader=DictLoader({
            "root.html": "{% include 'part.html' %}",
            "part.html": "<p>{{ title }}</p>",
        }),
        autoescape=True,
        undefined=StrictUndefined,
    )
    assert env.get_template("root.html").render(title="a & b") == "<p>a &amp; b</p>"
    try:
        env.get_template("root.html").render()
        raise AssertionError("StrictUndefined should raise")
    except UndefinedError:
        pass


def test_bs4():
    from bs4 import BeautifulSoup

    html = "<ul><li><a href='/one'>One</a><li><a class='hi' href='/two'>Two</a></ul>"
    soup = BeautifulSoup(io.StringIO(html), "html.parser")
    links = [a["href"] for a in soup.select("ul li > a.hi")]
    assert links == ["/two"]


def test_markdown_it():
    from markdown_it import MarkdownIt

    md = MarkdownIt()
    src = "# Title\n\n```python\nx = 1\n```\n"
    tokens = md.parse(src)
    assert tokens[0].map == [0, 1]
    fences = [t for t in tokens if t.type == "fence"]
    assert len(fences) == 1 and fences[0].info == "python" and fences[0].content == "x = 1\n"
    html = md.render(src)
    assert "<h1>Title</h1>" in html


def test_requests():
    import certifi
    import charset_normalizer
    import requests

    sess = requests.Session()
    req = requests.Request("POST", "https://example.org/api", params={"q": "a b"}, json={"x": 1})
    prep = sess.prepare_request(req)
    assert prep.url == "https://example.org/api?q=a+b"
    assert prep.body == b'{"x": 1}'

    ca_path = certifi.where()
    assert "BEGIN CERTIFICATE" in Path(ca_path).read_text(encoding="utf-8")
    best = charset_normalizer.from_bytes("héllo".encode("utf-8")).best()
    assert best is not None and best.encoding in ("utf_8", "utf-8")


def test_pypdf():
    from pypdf import PdfReader, PdfWriter
    from pypdf.generic import ContentStream, DictionaryObject, NameObject

    # Build a 2-page PDF with real text stream and distinct page sizes
    w = PdfWriter()
    p1 = w.add_blank_page(width=200, height=100)
    # Add Helvetica font resource and text drawing stream
    font_dict = w._add_object(DictionaryObject({
        NameObject("/Type"): NameObject("/Font"),
        NameObject("/Subtype"): NameObject("/Type1"),
        NameObject("/BaseFont"): NameObject("/Helvetica"),
    }))
    p1[NameObject("/Resources")] = w._add_object(DictionaryObject({
        NameObject("/Font"): DictionaryObject({NameObject("/F1"): font_dict}),
    }))
    stream = ContentStream(None, w)
    stream._data = b"BT /F1 12 Tf 10 50 Td (Hello Portable Python) Tj ET"
    p1[NameObject("/Contents")] = w._add_object(stream)

    w.add_blank_page(width=300, height=400)
    w.add_metadata({"/Title": "Smoke PDF"})

    pdf_path = Path("/tmp/smoke.pdf")
    with pdf_path.open("wb") as f:
        w.write(f)

    r = PdfReader(pdf_path)
    assert len(r.pages) == 2
    assert r.metadata.title == "Smoke PDF"
    assert float(r.pages[0].mediabox.width) == 200.0
    assert float(r.pages[1].mediabox.height) == 400.0
    text = r.pages[0].extract_text()
    assert "Hello Portable Python" in text, f"unexpected extracted text: {text!r}"


def test_metadata_and_resources():
    public_dists = [
        "PyYAML",
        "openpyxl",
        "pytest",
        "tomlkit",
        "ruamel.yaml",
        "packaging",
        "jsonschema",
        "asttokens",
        "Jinja2",
        "beautifulsoup4",
        "markdown-it-py",
        "requests",
        "pypdf",
    ]
    for name in public_dists:
        v = metadata.version(name)
        assert v, f"missing version for {name}"

    pkg_dists = metadata.packages_distributions()
    for imp in ["yaml", "openpyxl", "pytest", "tomlkit", "packaging", "jsonschema", "asttokens", "jinja2", "bs4", "markdown_it", "requests", "pypdf", "rpds"]:
        assert imp in pkg_dists, f"{imp} missing from packages_distributions()"

    eps = metadata.entry_points(group="console_scripts")
    ep_names = {ep.name for ep in eps}
    assert "pytest" in ep_names and "markdown-it" in ep_names

    # importlib.resources.files + as_file
    cacert = resources.files("certifi").joinpath("cacert.pem")
    assert "BEGIN CERTIFICATE" in cacert.read_text(encoding="utf-8")
    with resources.as_file(cacert) as p:
        assert p.is_file()
    draft_schema = resources.files("jsonschema_specifications").joinpath("schemas/draft202012/metaschema.json")
    assert "2020-12" in draft_schema.read_text(encoding="utf-8")


for name, fn in [
    ("stdlib", test_stdlib),
    ("PyYAML", test_pyyaml),
    ("openpyxl", test_openpyxl),
    ("pytest", test_pytest),
    ("tomlkit", test_tomlkit),
    ("ruamel.yaml", test_ruamel_yaml),
    ("packaging", test_packaging),
    ("jsonschema+rpds", test_jsonschema),
    ("asttokens", test_asttokens),
    ("Jinja2+MarkupSafe", test_jinja2),
    ("beautifulsoup4+soupsieve", test_bs4),
    ("markdown-it-py", test_markdown_it),
    ("requests+certifi", test_requests),
    ("pypdf", test_pypdf),
    ("metadata+resources", test_metadata_and_resources),
]:
    check(name, fn)

if FAILURES:
    print(f"\nFAILED {len(FAILURES)} checks: {FAILURES}")
    sys.exit(1)
print("\nALL 15 GUEST CHECKS PASSED")
