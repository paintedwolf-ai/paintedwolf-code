"""Exercise source-only admission with an actual disposable committed native flake."""
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile

from .adapter import nix
from .rehearsal import must_refuse
from .rehearsal_fixture import local_store
from .source import describe_verified_source


def main():
    system = {"x86_64": "x86_64-linux", "aarch64": "aarch64-linux"}.get(platform.machine())
    if platform.system() != "Linux" or system is None:
        raise ValueError("source-only Nix rehearsal requires a supported native Linux host")
    with tempfile.TemporaryDirectory(prefix="paintedwolf-nix-source-") as directory:
        work = Path(directory)
        source = work / "source"
        source.mkdir()
        (source / "VERSION").write_text("0.0.0-rehearsal\n")
        (source / "flake.lock").write_text(json.dumps({"nodes": {"root": {}}, "root": "root", "version": 7}))
        (source / "flake.nix").write_text('''{
          outputs = { self }: { packages.'''+system+'''.default = builtins.derivation {
            name = "paintedwolf-source-rehearsal"; system = "'''+system+'''";
            builder = "builtin:buildenv"; derivations = "";
            manifest = builtins.toFile "source-rehearsal-manifest" "disposable native fixture";
          }; };
        }''')
        environment = {"PATH": os.environ.get("PATH", os.defpath), "HOME": directory,
                       "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1", "LC_ALL": "C"}
        def git(*arguments):
            result = subprocess.run(["git", "-C", str(source), *arguments], env=environment,
                                    capture_output=True, check=True, timeout=60)
            return result.stdout.decode().strip()
        git("init", "--quiet")
        git("config", "user.name", "Painted Wolf qualification fixture")
        git("config", "user.email", "qualification@example.invalid")
        git("add", "--", "VERSION", "flake.lock", "flake.nix")
        git("commit", "--quiet", "-m", "Create disposable source qualification fixture")
        commit = git("rev-parse", "HEAD")
        store = local_store(work / "store")
        reference = source.as_uri().replace("file:", "git+file:", 1) + "?rev=" + commit
        built = json.loads(nix("build", "--extra-experimental-features", "flakes", "--store", store,
            "--option", "build-users-group", "", "--option", "max-jobs", "1", "--option", "cores", "1",
            "--no-link", "--json", "--no-write-lock-file", reference + "#packages." + system + ".default"))
        request = {"schema_version": 1, "version": "0.0.0-rehearsal", "application_commit": commit,
            "flake_lock_sha256": hashlib.sha256((source / "flake.lock").read_bytes()).hexdigest(),
            "system": system, "outputs": [built[0]["outputs"]["out"]]}
        report = describe_verified_source(request, source, source_store=store, rehearsal=True)
        (source / "VERSION").write_text("9.9.9\n")
        if describe_verified_source(request, source, source_store=store, rehearsal=True) != report:
            raise ValueError("mutable worktree bytes changed immutable Git source admission")
        for field, value in [("version", "9.9.9"), ("flake_lock_sha256", "f" * 64),
                             ("outputs", [request["outputs"][0] + "-wrong"])]:
            must_refuse(lambda: describe_verified_source({**request, field: value}, source,
                        source_store=store, rehearsal=True), "a mismatched source-only " + field)
        must_refuse(lambda: describe_verified_source(request, source, source_store="ssh://builder.invalid"),
                    "an implicit remote store")
        if {"public_keys", "objects", "cache_prefix", "url"} & report.keys():
            raise ValueError("source-only verification acquired public cache authority")
        print(json.dumps({"schema_version": 1, "status": "passed", "mode": "rehearsal", "system": system,
            "closure_paths": len(report["closure"]), "source_commit": commit,
            "checks": ["exact-committed-flake", "recursive-NAR-content", "schema4-native-derivation",
                       "mutable-worktree-isolation", "wrong-version-lock-output-refusal", "remote-store-refusal",
                       "no-public-cache-authority"]}, sort_keys=True))


if __name__ == "__main__":
    main()
