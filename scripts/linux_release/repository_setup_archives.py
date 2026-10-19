"""Read the bounded, script-free payload of repository control packages without installing."""

import io
from pathlib import PurePosixPath
import stat
import resource
import tempfile
import subprocess
import tarfile

LIMIT = 1024 * 1024


def command(arguments):
    def bound():
        resource.setrlimit(resource.RLIMIT_FSIZE, (4 * LIMIT, 4 * LIMIT))

    with tempfile.TemporaryFile() as output, tempfile.TemporaryFile() as errors:
        result = subprocess.run(arguments, stdout=output, stderr=errors, timeout=120, preexec_fn=bound)
        if result.returncode:
            raise ValueError("setup native archive query failed or exceeded its bound")
        output.seek(0)
        return output.read(4 * LIMIT + 1)


def name(raw):
    value = raw.removeprefix("./").rstrip("/")
    if value in {"", "."}:
        return ""
    path = PurePosixPath(value)
    if path.is_absolute() or ".." in path.parts or str(path) != value:
        raise ValueError("setup archive contains an unsafe path")
    return value


def tar_members(data):
    records = {}
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:*") as archive:
        for member in archive:
            path = name(member.name)
            if not path:
                if not member.isdir() or member.uid or member.gid or member.mode != 0o755:
                    raise ValueError("setup archive root has an unexpected identity or mode")
                continue
            if path in records or member.uid != 0 or member.gid != 0:
                raise ValueError("setup archive duplicates a path or has a foreign owner")
            if member.isdir():
                if member.mode != 0o755:
                    raise ValueError("setup directory has an unexpected mode")
                records[path] = None
            elif member.isfile() and member.mode == 0o644 and member.size <= LIMIT:
                records[path] = archive.extractfile(member).read()
            else:
                raise ValueError("setup archive contains a link, special file or unsafe mode")
    return records


def payload(records, expected):
    ancestors = {str(parent) for path in expected for parent in PurePosixPath(path).parents if str(parent) != "."}
    files = {path: data for path, data in records.items() if data is not None}
    if files != expected or any(path not in ancestors for path, data in records.items() if data is None):
        raise ValueError("setup payload differs from the selected configuration and public key")


def deb(package, version, expected, config):
    identity = command(["dpkg-deb", "--show", "--showformat=${Package}\n${Version}\n${Architecture}\n", str(package)])
    if identity.decode() != f"painted-wolf-code-repo\n{version}\nall\n":
        raise ValueError("setup DEB identity differs from the selected version")
    control = tar_members(command(["dpkg-deb", "--ctrl-tarfile", str(package)]))
    if set(control) != {"control", "conffiles"} or control["conffiles"] != ("/" + config + "\n").encode():
        raise ValueError("setup DEB contains unexpected scripts, triggers or conffiles")
    fields = {}
    for line in control["control"].decode().splitlines():
        if line.startswith(" "):
            continue
        key, separator, value = line.partition(": ")
        if not separator or key in fields:
            raise ValueError("setup DEB has malformed control fields")
        fields[key] = value
    if set(fields) != {"Package", "Version", "Architecture", "Section", "Priority", "Maintainer", "Description"}:
        raise ValueError("setup DEB has unexpected control relationships")
    payload(tar_members(command(["dpkg-deb", "--fsys-tarfile", str(package)])), expected)


def cpio(data):
    records, offset = {}, 0
    while True:
        header = data[offset:offset + 110]
        if len(header) != 110 or header[:6] != b"070701":
            raise ValueError("setup RPM requires a bounded newc payload")
        try:
            fields = [int(header[index:index + 8], 16) for index in range(6, 110, 8)]
        except ValueError as error:
            raise ValueError("setup RPM has an invalid cpio header") from error
        _, mode, uid, gid, links, _, size, _, _, _, _, namesize, checksum = fields
        offset += 110
        raw = data[offset:offset + namesize]
        if namesize < 1 or namesize > 4096 or len(raw) != namesize or raw[-1:] != b"\0" or b"\0" in raw[:-1]:
            raise ValueError("setup RPM has an invalid cpio pathname")
        offset = (offset + namesize + 3) & ~3
        if raw == b"TRAILER!!!\0":
            if size or any(data[offset:]):
                raise ValueError("setup RPM has bytes after its payload trailer")
            return records
        path = name(raw[:-1].decode())
        body = data[offset:offset + size]
        offset = (offset + size + 3) & ~3
        if (not path or path in records or uid or gid or checksum or size > LIMIT or len(body) != size
                or links != 1 or not stat.S_ISREG(mode) or stat.S_IMODE(mode) != 0o644):
            raise ValueError("setup RPM contains an unsafe or duplicate payload member")
        records[path] = body


def rpm(package, version, expected, config):
    identity = command(["rpm", "-qp", "--qf", "%{NAME}\n%{VERSION}-%{RELEASE}\n%{ARCH}\n%{EPOCHNUM}\n", str(package)])
    if identity.decode() != f"painted-wolf-code-repo\n{version}-1\nnoarch\n0\n":
        raise ValueError("setup RPM identity differs from the selected version")
    tags = ["PREIN", "POSTIN", "PREUN", "POSTUN", "PRETRANS", "POSTTRANS", "VERIFYSCRIPT",
            "TRIGGERSCRIPTS", "FILETRIGGERSCRIPTS", "TRANSFILETRIGGERSCRIPTS",
            "PREINPROG", "POSTINPROG", "PREUNPROG", "POSTUNPROG", "PRETRANSPROG", "POSTTRANSPROG",
            "VERIFYSCRIPTPROG", "TRIGGERSCRIPTPROG", "FILETRIGGERSCRIPTPROG", "TRANSFILETRIGGERSCRIPTPROG",
            "CONFLICTNAME", "OBSOLETENAME", "RECOMMENDNAME", "SUGGESTNAME", "SUPPLEMENTNAME", "ENHANCENAME",
            "PREFIXES"]
    query = "".join("%|" + tag + "?{1}:{0}|" for tag in tags)
    present = command(["rpm", "-qp", "--qf", query, str(package)])
    if len(present) != len(tags) or any(value not in b"01" for value in present):
        raise ValueError("setup RPM script header query has an invalid shape: " + repr(present))
    if present != b"0" * len(tags):
        rejected = [tag for tag, value in zip(tags, present) if value == ord("1")]
        raise ValueError("setup RPM contains scripts or triggers: " + ", ".join(rejected))
    requirements = command(["rpm", "-qp", "--qf", "[%{REQUIRENAME}\n]", str(package)]).decode().splitlines()
    allowed = {"rpmlib(CompressedFileNames)", "rpmlib(FileDigests)", "rpmlib(PayloadFilesHavePrefix)"}
    if len(requirements) != len(set(requirements)) or set(requirements) != allowed:
        raise ValueError("setup RPM contains an unexpected dependency relationship")
    provides = command(["rpm", "-qp", "--qf", "[%{PROVIDENAME}\t%{PROVIDEFLAGS}\t%{PROVIDEVERSION}\n]", str(package)])
    if provides.decode() != f"painted-wolf-code-repo\t8\t{version}-1\n":
        raise ValueError("setup RPM contains unexpected provided identities")
    for tag in ["FILECAPS", "FILECONTEXTS"]:
        values = command(["rpm", "-qp", "--qf", "%|" + tag + "?{[%{" + tag + "}\n]}:{}|", str(package)])
        if any(values.decode().splitlines()):
            raise ValueError("setup RPM carries unexpected capabilities or security contexts")
    rows = command(["rpm", "-qp", "--qf", "[%{FILENAMES}\t%{FILEMODES}\t%{FILEFLAGS}\t%{FILEUSERNAME}\t%{FILEGROUPNAME}\n]", str(package)]).decode().splitlines()
    expected_rows = {f"/{path}\t{stat.S_IFREG | 0o644}\t{17 if path == config else 0}\troot\troot" for path in expected}
    if len(rows) != len(expected_rows) or set(rows) != expected_rows:
        raise ValueError("setup RPM header ownership, modes or configuration flags differ")
    payload(cpio(command(["rpm2cpio", str(package)])), expected)
