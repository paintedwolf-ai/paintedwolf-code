"""Versioned distribution facts, independent of signing credentials and transport."""
from __future__ import annotations

from datetime import datetime
import base64
import json
from pathlib import PurePosixPath
import re
from urllib.parse import urlsplit

from release_semver import parse

PLATFORMS = {"darwin-aarch64": ("darwin", "aarch64", {"dmg"}),
             "linux-x86_64": ("linux", "x86_64", {"deb", "rpm", "pkg.tar.zst"}),
             "linux-aarch64": ("linux", "aarch64", {"deb", "rpm", "pkg.tar.zst"}),
             "windows-x86_64": ("windows", "x86_64", {"exe"})}
ARTIFACT = {"file", "url", "sha256", "bytes", "content_type"}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def exact(value, fields):
    require(isinstance(value, dict) and set(value) == set(fields), "unsupported distribution fields")


def digest(value, size=64):
    require(isinstance(value, str) and re.fullmatch(r"[0-9a-f]{" + str(size) + "}", value),
            "invalid distribution digest")
    return value


def canonical(value):
    return (json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False) + "\n").encode()


def decode(raw):
    def unique(pairs):
        value = {}
        for key, item in pairs:
            require(key not in value, "distribution repeats a JSON field")
            value[key] = item
        return value
    require(isinstance(raw, bytes) and len(raw) <= 2 << 20, "distribution exceeds its byte bound")
    value = json.loads(raw, object_pairs_hook=unique)
    require(canonical(value) == raw, "distribution must use its canonical immutable encoding")
    return value


def url(value, origin):
    require(isinstance(value, str), "distribution URL must be a string")
    parsed = urlsplit(value)
    require(value.startswith(origin + "/") and not parsed.query and not parsed.fragment
            and not parsed.username and not parsed.password and "//" not in parsed.path
            and "%" not in parsed.path and "\\" not in parsed.path
            and all(part not in {".", ".."} for part in parsed.path.split("/")), "invalid distribution URL")


def artifact(row, origin):
    exact(row, ARTIFACT)
    require(isinstance(row["file"], str) and re.fullmatch(r"[A-Za-z0-9+._-]+", row["file"]),
            "invalid distribution artifact name")
    url(row["url"], origin)
    require(PurePosixPath(urlsplit(row["url"]).path).name == row["file"], "distribution artifact URL name differs")
    digest(row["sha256"])
    require(type(row["bytes"]) is int and 0 < row["bytes"] <= 64 << 30, "invalid distribution artifact size")
    require(isinstance(row["content_type"], str) and re.fullmatch(r"[a-z0-9.+-]+/[a-z0-9.+-]+", row["content_type"]),
            "invalid distribution content type")


def repository(row, origin, channel):
    exact(row, {"family", "architecture", "channel", "generation", "inventory_sha256", "activation_sha256",
                "base_url", "signing_generation", "key_fingerprint", "setup", "public_key"})
    require(row["family"] in {"deb", "rpm", "pacman"} and row["architecture"] in {"both", "x86_64", "aarch64"}
            and row["channel"] in {"stable", "preview"} and (channel == "stable" or row["channel"] == "preview"),
            "invalid distribution repository selection")
    require(isinstance(row["generation"], str) and re.fullmatch(r"[A-Za-z0-9._-]+", row["generation"]),
            "invalid repository generation")
    for name in ("inventory_sha256", "activation_sha256"):
        digest(row[name])
    require(type(row["signing_generation"]) is int and row["signing_generation"] > 0,
            "invalid repository signing generation")
    require(isinstance(row["key_fingerprint"], str) and re.fullmatch(r"[0-9A-F]{40,64}", row["key_fingerprint"]),
            "invalid repository signing fingerprint")
    url(row["base_url"], origin)
    artifact(row["setup"], origin)
    artifact(row["public_key"], origin)


def nix(value, origin, commit):
    if value is None:
        return
    exact(value, {"source", "cache"})
    exact(value["source"], {"git_url", "commit", "flake_lock_sha256"})
    require(value["source"]["commit"] == commit
            and value["source"]["git_url"] == "github:paintedwolf-ai/paintedwolf-code/" + commit,
            "Nix source must select the exact Git revision")
    digest(value["source"]["flake_lock_sha256"])
    cache = value["cache"]
    require(isinstance(cache, dict) and type(cache.get("enabled")) is bool, "Nix cache requires an explicit state")
    if not cache["enabled"]:
        exact(cache, {"enabled"})
        return
    exact(cache, {"enabled", "base_url", "public_keys", "admission_sha256", "closures"})
    url(cache["base_url"], origin)
    digest(cache["admission_sha256"])
    require(isinstance(cache["public_keys"], list) and cache["public_keys"]
            and len(set(cache["public_keys"])) == len(cache["public_keys"])
            and all(isinstance(key, str) and re.fullmatch(r"[A-Za-z0-9._-]+:[A-Za-z0-9+/]{43}=", key)
                    for key in cache["public_keys"]), "invalid Nix cache public trust")
    for key in cache["public_keys"]:
        require(base64.b64encode(base64.b64decode(key.split(":", 1)[1], validate=True)).decode() == key.split(":", 1)[1],
                "Nix cache trust must use canonical public key bytes")
    require(isinstance(cache["closures"], list) and cache["closures"], "Nix cache omits its admitted closures")
    systems = set()
    for closure in cache["closures"]:
        exact(closure, {"system", "store_path", "nar_sha256"})
        require(closure["system"] in {"x86_64-linux", "aarch64-linux"} and closure["system"] not in systems
                and isinstance(closure["store_path"], str)
                and re.fullmatch(r"/nix/store/[0123456789abcdfghijklmnpqrsvwxyz]{32}-[^/\s]+", closure["store_path"]),
                "invalid Nix cache closure identity")
        digest(closure["nar_sha256"])
        systems.add(closure["system"])


def validate(document):
    exact(document, {"schema_version", "kind", "mode", "version", "channel", "notes", "pub_date", "origin",
                     "source", "controller_commit", "updater_manifest_sha256", "signing_generation", "platforms",
                     "repositories", "nix", "aur"})
    require(type(document["schema_version"]) is int and document["schema_version"] == 1
            and document["kind"] == "painted-wolf-distribution" and document["mode"] in {"production", "rehearsal"},
            "unsupported distribution document")
    require(parse(document["version"]).channel == document["channel"], "distribution channel differs from version")
    require(isinstance(document["notes"], str) and document["notes"].strip(), "distribution notes are missing")
    require(isinstance(document["pub_date"], str)
            and datetime.fromisoformat(document["pub_date"].replace("Z", "+00:00")).tzinfo is not None,
            "distribution date requires a timezone")
    origin = document["origin"]
    require(isinstance(origin, str) and re.fullmatch(r"https://[a-z0-9.-]+(?::[0-9]+)?", origin), "invalid download origin")
    if document["mode"] == "production":
        require(origin == "https://downloads.paintedwolf.dev", "production distribution requires its maintained origin")
    source = document["source"]
    exact(source, {"repository", "commit", "tree", "catalog_sha256"})
    require(source["repository"] == "https://github.com/paintedwolf-ai/paintedwolf-code", "foreign distribution source")
    for name in ("commit", "tree"):
        digest(source[name], 40)
    digest(source["catalog_sha256"])
    digest(document["controller_commit"], 40)
    digest(document["updater_manifest_sha256"])
    require(type(document["signing_generation"]) is int and document["signing_generation"] > 0,
            "invalid distribution signing generation")
    require(isinstance(document["platforms"], dict) and document["platforms"], "distribution omits public platforms")
    for key, row in document["platforms"].items():
        require(key in PLATFORMS, "unknown canonical distribution platform")
        exact(row, {"os", "architecture", "update_owner", "packages"})
        system, architecture, extensions = PLATFORMS[key]
        require((row["os"], row["architecture"]) == (system, architecture)
                and row["update_owner"] == ("package_manager" if system == "linux" else "application"),
                "distribution platform ownership differs from its canonical identity")
        require(isinstance(row["packages"], dict) and row["packages"] and set(row["packages"]) <= extensions,
                "invalid distribution package family")
        for extension, package in row["packages"].items():
            artifact(package, origin)
            name = f"painted-wolf-code_v{document['version']}_{key}.{extension}"
            require(package["file"] == name and package["url"] == origin + "/releases/v" + document["version"] + "/" + name,
                    "distribution package does not use its immutable release URL")
    require(isinstance(document["repositories"], list), "distribution repositories must be an explicit list")
    identities = set()
    for row in document["repositories"]:
        repository(row, origin, document["channel"])
        identity = (row["family"], row["architecture"], row["channel"])
        require(identity not in identities, "distribution repeats repository authority")
        identities.add(identity)
    nix(document["nix"], origin, source["commit"])
    if any(key.startswith("linux-") for key in document["platforms"]):
        require(document["nix"] is not None, "public Linux distribution requires its exact Nix source")
    aur = document["aur"]
    require(isinstance(aur, dict) and type(aur.get("enabled")) is bool, "AUR requires an explicit availability state")
    if aur["enabled"]:
        exact(aur, {"enabled", "git_url", "commit", "source_sha256", "package_sha256"})
        origins = ({"https://aur.archlinux.org/painted-wolf-code.git",
                    "https://aur.archlinux.org/painted-wolf-code-bin.git"} if document["channel"] == "stable"
                   else {"https://aur.archlinux.org/painted-wolf-code-preview-bin.git"})
        require(aur["git_url"] in origins, "foreign or cross-channel AUR source")
        digest(aur["commit"], 40)
        digest(aur["source_sha256"])
        digest(aur["package_sha256"])
    else:
        exact(aur, {"enabled"})
    return document
