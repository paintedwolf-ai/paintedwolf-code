"""Release version parsing and platform projections."""

from __future__ import annotations

from dataclasses import dataclass
import re


SEMVER = re.compile(
    r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    r"(?:-((?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)"
    r"(?:\.(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*))?"
    r"(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$"
)


@dataclass(frozen=True)
class Version:
    major: int
    minor: int
    patch: int
    prerelease: tuple[str, ...] | None

    @property
    def core(self) -> str:
        return f"{self.major}.{self.minor}.{self.patch}"

    @property
    def channel(self) -> str:
        return "preview" if self.prerelease else "stable"


def parse(raw: str) -> Version:
    match = SEMVER.fullmatch(raw)
    if not match:
        raise ValueError(f"invalid SemVer: {raw}")
    prerelease = match.group(4)
    return Version(
        *(int(match.group(index)) for index in range(1, 4)),
        None if prerelease is None else tuple(prerelease.split(".")),
    )


def compare(left: Version, right: Version) -> int:
    left_core = (left.major, left.minor, left.patch)
    right_core = (right.major, right.minor, right.patch)
    if left_core != right_core:
        return (left_core > right_core) - (left_core < right_core)
    if left.prerelease is None or right.prerelease is None:
        return (left.prerelease is None) - (right.prerelease is None)
    for a, b in zip(left.prerelease, right.prerelease):
        if a == b:
            continue
        a_num, b_num = a.isdigit(), b.isdigit()
        if a_num and b_num:
            return (int(a) > int(b)) - (int(a) < int(b))
        if a_num != b_num:
            return -1 if a_num else 1
        return (a > b) - (a < b)
    return (len(left.prerelease) > len(right.prerelease)) - (
        len(left.prerelease) < len(right.prerelease)
    )


def parse_release_build(raw: str) -> int:
    if not re.fullmatch(r"[1-9][0-9]*", raw):
        raise ValueError(f"invalid release build: {raw}")
    value = int(raw)
    if value > 65535:
        raise ValueError("release build exceeds the portable 65535 ceiling")
    return value


def windows_package_version(version: Version, release_build: int) -> str:
    if version.major > 255 or version.minor > 255:
        raise ValueError("Windows package major and minor versions must not exceed 255")
    parse_release_build(str(release_build))
    return f"{version.major}.{version.minor}.{release_build}"
