"""Read APT's configured policy and cache through apt_pkg without creating or refreshing it."""
import json
import sys

try:
    import apt_pkg
except ImportError:
    print(json.dumps({"schema": 1, "adapter_available": False}))
    raise SystemExit(0)


class _Quiet:
    """Open the cache without writing progress text into the JSON report."""
    def update(self, percent=None):
        pass

    def done(self):
        pass


apt_pkg.init_config()
apt_pkg.config.set("Dir::Cache::pkgcache", "")
apt_pkg.config.set("Dir::Cache::srcpkgcache", "")
apt_pkg.init_system()
cache = apt_pkg.Cache(_Quiet())
depcache = apt_pkg.DepCache(cache)
sources = apt_pkg.SourceList()
sources.read_main_list()

name = sys.argv[1]
package = cache[name] if name in cache else None
installed = package.current_ver if package is not None and package.has_versions else None
candidate = depcache.get_candidate_ver(package) if installed is not None else None


def trusted(package_file):
    index = sources.find_index(package_file)
    return bool(index and index.is_trusted)


print(json.dumps({
    "schema": 1,
    "adapter_available": True,
    "installed": installed.ver_str if installed else None,
    "architecture": installed.arch if installed else None,
    "held": package.selected_state == apt_pkg.SELSTATE_HOLD if installed else None,
    "candidate": {
        "version": candidate.ver_str,
        "architecture": candidate.arch,
        "newer": depcache.is_upgradable(package),
        "priority": depcache.policy.get_priority(candidate),
        "origins": [{"id": origin.origin or origin.label or origin.site, "suite": origin.archive,
                     "trusted": trusted(origin)}
                    for origin, _ in candidate.file_list if origin.archive != "now"],
    } if candidate else None,
}))
