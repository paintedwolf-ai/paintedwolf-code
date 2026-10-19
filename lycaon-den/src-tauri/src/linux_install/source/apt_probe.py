"""Read python-apt's configured policy and cache without creating or refreshing it."""
import json
import sys

try:
    import apt
    import apt_pkg
except ImportError:
    print(json.dumps({"schema": 1, "adapter_available": False}))
    raise SystemExit(0)

apt_pkg.init_config()
apt_pkg.config.set("Dir::Cache::pkgcache", "")
apt_pkg.config.set("Dir::Cache::srcpkgcache", "")
apt_pkg.init_system()
cache = apt.Cache()
package = cache[sys.argv[1]] if sys.argv[1] in cache else None
installed = package.installed if package is not None else None
candidate = package.candidate if installed is not None else None
print(json.dumps({
    "schema": 1,
    "adapter_available": True,
    "installed": installed.version if installed else None,
    "architecture": installed.architecture if installed else None,
    "held": package._pkg.selected_state == apt_pkg.SELSTATE_HOLD if installed else None,
    "candidate": {
        "version": candidate.version,
        "architecture": candidate.architecture,
        "newer": package.is_upgradable,
        "priority": candidate.policy_priority,
        "origins": [{"id": origin.origin or origin.label or origin.site, "suite": origin.archive,
                     "trusted": origin.trusted}
                    for origin in candidate.origins if origin.archive != "now"],
    } if candidate else None,
}))
