"""Native control-package identities advance with selected repository trust and channel."""

from scripts.release_semver import linux_package_version, parse

PACKAGE = "painted-wolf-code-repo"


def package_identity(version, family, channel, signing_generation):
    if (family not in {"deb", "rpm"} or channel not in {"stable", "preview"}
            or type(signing_generation) is not int or signing_generation < 1):
        raise ValueError("setup identity requires a native family, channel and positive trust generation")
    native = linux_package_version(parse(version))
    if family == "deb":
        native += f"+repo.{signing_generation}.{channel}"
        return {"name": PACKAGE, "version": native, "release": None, "architecture": "all",
                "file": f"{PACKAGE}_{native}_all.deb"}
    release = f"{signing_generation}.{channel}"
    return {"name": PACKAGE, "version": native, "release": release, "architecture": "noarch",
            "file": f"{PACKAGE}-{native}-{release}.noarch.rpm"}
