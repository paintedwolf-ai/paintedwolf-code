"""Stand-in for python-apt's apt_pkg, serving the cache described by cache.json beside it.

It implements only the documented apt_pkg surface the probe reads, so a probe that reaches for any
other attribute fails here as it would against an incompatible python-apt.
"""
import json
import os

SELSTATE_INSTALL = 1
SELSTATE_HOLD = 2

with open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "cache.json"), encoding="utf-8") as handle:
    _STATE = json.load(handle)


class _Configuration:
    def __init__(self):
        self.values = {}

    def set(self, key, value):
        self.values[key] = value


config = _Configuration()
_initialized = set()


def init_config():
    _initialized.add("config")


def init_system():
    if "config" not in _initialized:
        raise SystemError("init_config must precede init_system")
    for key in ("Dir::Cache::pkgcache", "Dir::Cache::srcpkgcache"):
        if config.values.get(key) != "":
            raise SystemError(f"{key} would let the probe write the binary cache")
    _initialized.add("system")


class PackageFile:
    def __init__(self, fields):
        self.origin = fields.get("origin", "")
        self.label = fields.get("label", "")
        self.site = fields.get("site", "")
        self.archive = fields.get("archive", "")
        self._trusted = fields.get("trusted")


class Version:
    def __init__(self, fields):
        self.ver_str = fields["version"]
        self.arch = fields["arch"]
        self.file_list = [(PackageFile(item), index) for index, item in enumerate(fields.get("files", []))]
        self._priority = fields.get("priority", 0)


class Package:
    def __init__(self, name, fields):
        self.name = name
        self.has_versions = fields.get("has_versions", True)
        self.current_ver = Version(fields["installed"]) if fields.get("installed") else None
        self.selected_state = SELSTATE_HOLD if fields.get("held") else SELSTATE_INSTALL
        self._candidate = Version(fields["candidate"]) if fields.get("candidate") else None
        self._upgradable = fields.get("upgradable", False)


class Cache:
    def __init__(self, progress):
        if "system" not in _initialized:
            raise SystemError("init_system must precede opening the cache")
        if _STATE.get("unreadable"):
            raise SystemError("E:The package lists or status file could not be parsed or opened.")
        progress.update(0.0)
        progress.done()
        self._packages = {name: Package(name, fields) for name, fields in _STATE["packages"].items()}

    def __contains__(self, name):
        return name in self._packages

    def __getitem__(self, name):
        return self._packages[name]


class Policy:
    def get_priority(self, version):
        return version._priority


class DepCache:
    def __init__(self, cache):
        self.policy = Policy()

    def get_candidate_ver(self, package):
        return package._candidate

    def is_upgradable(self, package):
        return package.current_ver is not None and package._upgradable


class _IndexFile:
    def __init__(self, trusted):
        self.is_trusted = trusted


class SourceList:
    def __init__(self):
        self._read = False

    def read_main_list(self):
        self._read = True

    def find_index(self, package_file):
        if not self._read:
            raise SystemError("read_main_list must precede find_index")
        return None if package_file._trusted is None else _IndexFile(package_file._trusted)
