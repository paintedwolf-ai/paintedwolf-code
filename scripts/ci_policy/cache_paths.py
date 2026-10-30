"""Resolve the native Go compiler cache before provisioning the toolchain."""
import os
from pathlib import Path, PurePosixPath, PureWindowsPath


def go_cache_directory(environment):
    """Follow Go's os.UserCacheDir defaults and honor an explicit GOCACHE."""
    platform = environment.get("RUNNER_OS")
    if platform not in ("Linux", "macOS", "Windows"):
        raise ValueError("RUNNER_OS must identify a supported native runner")
    values = ({key.upper(): value for key, value in environment.items()}
              if platform == "Windows" else environment)
    path_type = PureWindowsPath if platform == "Windows" else PurePosixPath
    directory = values.get("GOCACHE", "")
    if directory == "off":
        raise ValueError("GOCACHE=off cannot provide a compiler cache payload")
    if not directory:
        if platform == "Windows":
            base = values.get("LOCALAPPDATA", "")
            suffix = ()
        elif platform == "macOS":
            base = values.get("HOME", "")
            suffix = ("Library", "Caches")
        else:
            base = values.get("XDG_CACHE_HOME", "") or values.get("HOME", "")
            suffix = () if values.get("XDG_CACHE_HOME") else (".cache",)
        if not base or not path_type(base).is_absolute():
            raise ValueError("Go cache resolution requires an absolute native cache root")
        directory = str(path_type(base).joinpath(*suffix, "go-build"))
    if not path_type(directory).is_absolute() or any(char in directory for char in "\r\n\0"):
        raise ValueError("GOCACHE must be an absolute single-line path")
    return directory


def main():
    directory = go_cache_directory(os.environ)
    # Pin later Go invocations to the same path used by lookup, restore and save.
    for variable, key in (("GITHUB_OUTPUT", "gocache"), ("GITHUB_ENV", "GOCACHE")):
        with Path(os.environ[variable]).open("a", encoding="utf-8") as output:
            output.write(f"{key}={directory}\n")


if __name__ == "__main__":
    main()
