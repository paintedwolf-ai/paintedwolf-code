"""Collect exact installed native package bytes without rebuilding them in a release job."""
import argparse
import hashlib
import os
from pathlib import Path
import shutil
import subprocess

from ..installed_evidence import artifacts
from ..installed_provenance import controller_context
from ..release_admission import document
from ..release_source import facts
from .controller import retain
from .files import verify_files
ARCHES = {"amd64": "linux-x86_64", "arm64": "linux-aarch64"}
FAMILIES = {"deb": "deb", "rpm": "rpm", "pacman": "pkg.tar.zst"}


def collect(selected, catalog, destination):
    """Only independently admitted file identities may acquire canonical public names."""
    if destination.exists() or destination.is_symlink():
        raise ValueError("native publication collection requires a fresh destination")
    inputs, _ = selected.authorize()
    source = selected.source
    verified = {row['id']: row for row in verify_files(inputs['artifacts'], source['source_commit'], selected.root / 'packages')}
    identities = {}
    for identifier, row in artifacts(inputs['artifacts'], source['source_commit']).items():
        if row['identity']['kind'] != 'file':
            continue
        key = (ARCHES[row['architecture']], FAMILIES[row['installation']])
        if key in identities:
            raise ValueError("native publication identity is ambiguous")
        identities[key] = identifier
    destination.mkdir(mode=0o700)
    retained = []
    try:
        for platform, row in catalog.items():
            if row['publication'] != 'public' or platform not in ARCHES.values():
                continue
            if row['updater_extension'] is not None:
                raise ValueError("native package publication cannot acquire an in-app updater")
            for extension in row['package_extensions']:
                identifier = identities.get((platform, extension))
                if identifier is None:
                    raise ValueError("public native package lacks installed campaign admission")
                name = f"painted-wolf-code_v{source['version']}_{platform}.{extension}"
                target = destination / name
                with (selected.root / 'packages' / identifier).open('rb') as origin, target.open('xb') as output:
                    shutil.copyfileobj(origin, output, 1 << 20)
                target.chmod(0o600)
                digest = hashlib.sha256()
                with target.open('rb') as stream:
                    for chunk in iter(lambda: stream.read(1 << 20), b''):
                        digest.update(chunk)
                expected = verified[identifier]
                if digest.hexdigest() != expected['sha256'] or target.stat().st_size != expected['bytes']:
                    raise ValueError("native publication bytes changed during collection")
                (destination / (name + '.sha256')).write_text(digest.hexdigest() + '\n')
                retained.append(dict(file=name, artifact_id=identifier, sha256=digest.hexdigest(), bytes=expected['bytes']))
        selected.authorize()
        verify_files(inputs['artifacts'], source['source_commit'], selected.root / 'packages')
        return retained
    except BaseException:
        shutil.rmtree(destination)
        raise


def main(arguments=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source-repository', type=Path, required=True)
    parser.add_argument('--source-commit', required=True)
    parser.add_argument('--campaign-run-id', type=int, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--retained-dir', type=Path, required=True)
    args = parser.parse_args(arguments)
    try:
        producer = controller_context(os.environ)
        from scripts.ci_policy.github import api
        from scripts.release_distribution_contract.platforms import parse_platforms
        from ..openpgp import selection
        from ..support_matrix import CATALOG
        from .generations import CATALOG as signing_catalog
        from .authority import repository_origin, PREFIX
        roots = (args.source_repository.resolve(strict=True), Path(__file__).resolve().parents[3])
        if any(path.resolve().is_relative_to(root) for path in (args.output, args.retained_dir) for root in roots):
            raise ValueError("collection outputs must be outside source and controller checkouts")
        facts(args.source_repository, args.source_commit)
        raw = subprocess.check_output(['git', '--no-replace-objects', '-C', str(args.source_repository),
            'show', args.source_commit + ':packaging/release-platforms.json'], timeout=60)
        platforms = parse_platforms(raw)
        catalog, _ = document(CATALOG)
        signing, _ = selection(signing_catalog, False)
        selected = retain(api, os.environ['GITHUB_REPOSITORY'], producer['repository_id'], args.campaign_run_id,
            args.source_repository, args.source_commit, args.retained_dir, catalog=catalog,
            signing_generation=signing['generation'], key_fingerprint=signing['fingerprint'],
            repository_base_url=repository_origin(os.environ['LINUX_REPOSITORY_ORIGIN']) + '/' + PREFIX)
        collect(selected, platforms, args.output)
    except (OSError, ValueError, KeyError, TypeError, ImportError) as error:
        parser.exit(1, 'Native publication collection refused: ' + str(error) + '\n')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
