"""Independent engine provenance, build manifest verification, and harness setup helpers."""
import hashlib
import json
import os
import pathlib
import platform
import shlex
import subprocess
import uuid
import smoke
from built_runtime import engine_path
from portable_command.contracts import DIRECTORY, ROOT, bytes_value


def file_sha256(path):
    return hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()


def _git(root, *args):
    proc = subprocess.run(['git', '-C', str(root), *args], capture_output=True, text=True, check=False)
    return proc.stdout.strip() if proc.returncode == 0 else ''


def resolve_engine_binary(root=ROOT, engine_bin=None):
    if engine_bin:
        return pathlib.Path(engine_bin).resolve(), 'cli:--engine'
    env_bin = os.environ.get('PW_ENGINE_BIN', '').strip()
    if env_bin:
        return pathlib.Path(env_bin).resolve(), 'env:PW_ENGINE_BIN'
    return pathlib.Path(engine_path(root)).resolve(), 'checkout-build'


def resolve_engine_manifest(engine_manifest=None):
    if engine_manifest:
        return pathlib.Path(engine_manifest).resolve()
    env_manifest = os.environ.get('PW_ENGINE_MANIFEST', '').strip()
    return pathlib.Path(env_manifest).resolve() if env_manifest else None


def materialize_helpers(helpers, helpers_dir):
    """Materialize fixture helper executables into a per-case bin directory."""
    if not helpers:
        return None
    dest = pathlib.Path(helpers_dir).resolve()
    dest.mkdir(parents=True, exist_ok=True)
    for name, spec in helpers.items():
        if not name or '/' in name or '\\' in name or name in {'.', '..'}:
            raise ValueError('invalid helper executable name: ' + str(name))
        script = dest / name
        if spec.get('kind') == 'argv-json':
            script.write_text("#!/bin/sh\nexec python3 -c 'import json,sys; print(json.dumps(sys.argv[1:]))' \"$@\"\n")
        else:
            out = shlex.quote(str(spec.get('stdout') or ''))
            err = shlex.quote(str(spec.get('stderr') or ''))
            code = int(spec.get('status', 0))
            script.write_text(f"#!/bin/sh\nprintf '%s' {out}\nprintf '%s' {err} >&2\nexit {code}\n")
        script.chmod(0o755)
    return dest


def make_app_factory(engine_bin=None, base_factory=smoke.Application, helpers_dir=None):
    """Wrap smoke.Application so an explicit engine binary, PW_ENGINE_BIN, or helper PATH is honored."""
    if base_factory is not smoke.Application:
        return base_factory
    binary, _ = resolve_engine_binary(ROOT, engine_bin)
    def factory(directory):
        orig = smoke.engine_path
        orig_path = os.environ.get('PATH')
        smoke.engine_path = lambda _root: binary
        if helpers_dir is not None:
            os.environ['PATH'] = str(helpers_dir) + (os.pathsep + orig_path if orig_path else '')
        try:
            return base_factory(directory)
        finally:
            smoke.engine_path = orig
            if helpers_dir is not None:
                if orig_path is None:
                    os.environ.pop('PATH', None)
                else:
                    os.environ['PATH'] = orig_path
    return factory


def inject_drafts(application, project_id, root_id, drafts):
    """Inject application-owned editor drafts via /v1/projects/{id}/editor-documents."""
    bindings, all_ok = {}, True
    for rel_path, spec in (drafts or {}).items():
        text = bytes_value(spec['bytes']).decode('utf-8')
        try:
            opened = application.request(f'/v1/projects/{project_id}/editor-documents', {
                'path': rel_path, 'root_id': root_id, 'client_id': 'portable-command-harness',
            })
            updated = application.request(f"/v1/projects/{project_id}/editor-documents/{opened['id']}", {
                'operation_id': str(uuid.uuid4()), 'client_id': 'portable-command-harness',
                'expected_revision': opened['revision'], 'content': text, 'eol': 'lf',
            }, method='PUT')
            bindings[rel_path] = {'injected': True, 'document_id': updated['id'], 'revision': updated['revision'], 'alias': spec.get('revision')}
        except Exception as exc:
            all_ok = False
            bindings[rel_path] = {'injected': False, 'alias': spec.get('revision'), 'error': str(exc)}
    return all_ok, bindings


def inspect_engine_provenance(root=ROOT, engine_bin=None, evaluator_git_head='', evaluator_git_tree='', engine_manifest=None):
    """Bind the engine executable and build/source identity independently from the evaluator checkout."""
    binary, build_source = resolve_engine_binary(root, engine_bin)
    exists = binary.is_file()
    sha = file_sha256(binary) if exists else ''
    product_version = go_version = ''
    vcs = {}
    if exists:
        try:
            ver = subprocess.run([str(binary), '--version'], capture_output=True, text=True, check=False)
            if ver.returncode == 0:
                product_version = ver.stdout.strip()
        except OSError:
            pass
        try:
            meta = subprocess.run(['go', 'version', '-m', str(binary)], capture_output=True, text=True, check=False)
            if meta.returncode == 0:
                for line in meta.stdout.splitlines():
                    parts = line.strip().split('\t')
                    if line.startswith(str(binary)) and ': ' in line:
                        go_version = line.split(': ', 1)[1].strip()
                    elif len(parts) >= 2 and parts[0] == 'build' and '=' in parts[1]:
                        k, v = parts[1].split('=', 1)
                        if k.startswith('vcs.'):
                            vcs[k] = v
        except OSError:
            pass

    mpath = resolve_engine_manifest(engine_manifest)
    m_data, m_sha = {}, ''
    m_match = m_path_match = m_head_match = m_tree_match = None
    if mpath is not None and mpath.is_file():
        m_sha = file_sha256(mpath)
        try:
            raw = json.loads(mpath.read_text())
            if isinstance(raw, dict):
                m_data = raw
        except (OSError, ValueError):
            m_data = {}
        m_match = bool(exists and sha and m_data.get('executable_sha256') == sha)
        m_exec = str(m_data.get('executable_path') or '')
        m_path_match = bool(m_exec and pathlib.Path(m_exec).resolve() == binary)
        m_head = str(m_data.get('git_head') or '')
        m_tree = str(m_data.get('git_tree') or '')
        m_head_match = bool(m_head and (not evaluator_git_head or m_head == evaluator_git_head))
        m_tree_match = bool(m_tree and (not evaluator_git_tree or m_tree == evaluator_git_tree))
    else:
        m_head = m_tree = ''

    vcs_rev = vcs.get('vcs.revision', '')
    if mpath is not None:
        provenance_verified = bool(m_match and m_path_match and m_head_match and m_tree_match)
        effective_rev = m_head
    else:
        provenance_verified = bool(exists and sha and vcs_rev and vcs.get('vcs.modified') in {'', 'false'})
        effective_rev = vcs_rev
    matches_head = bool(effective_rev and evaluator_git_head and evaluator_git_head.startswith(effective_rev)) if effective_rev else None
    return {
        'executable_path': str(binary),
        'executable_sha256': sha,
        'executable_exists': exists,
        'build_source': build_source,
        'product_version': product_version,
        'go_version': go_version,
        'build_vcs_revision': vcs_rev,
        'build_vcs_time': vcs.get('vcs.time', ''),
        'build_vcs_modified': vcs.get('vcs.modified', ''),
        'manifest_path': str(mpath) if mpath is not None else '',
        'manifest_sha256': m_sha,
        'manifest_git_head': m_head,
        'manifest_git_tree': m_tree,
        'manifest_executable_sha256': str(m_data.get('executable_sha256') or ''),
        'manifest_build_command': str(m_data.get('build_command') or ''),
        'manifest_built_at': str(m_data.get('built_at') or ''),
        'manifest_sha256_match': m_match,
        'manifest_path_match': m_path_match,
        'manifest_head_match': m_head_match,
        'manifest_tree_match': m_tree_match,
        'provenance_verified': provenance_verified,
        'matches_evaluator_head': matches_head,
    }


def source_identity(root=ROOT, profile=None, engine_bin=None, engine_manifest=None):
    """Capture immutable evaluator checkout identity and independent engine provenance."""
    git_head = _git(root, 'rev-parse', 'HEAD')
    git_tree = _git(root, 'rev-parse', 'HEAD^{tree}')
    version_file = root / 'VERSION'
    lang_file = root / 'lycaon/config/packs/painted-wolf/platform/tools/command-language-v1/language.json'
    prov = inspect_engine_provenance(
        root, engine_bin=engine_bin, evaluator_git_head=git_head,
        evaluator_git_tree=git_tree, engine_manifest=engine_manifest,
    )
    return {
        'git_head': git_head,
        'git_tree': git_tree,
        'application_version': version_file.read_text().strip() if version_file.is_file() else '',
        'engine_binary_path': prov['executable_path'],
        'engine_binary_sha256': prov['executable_sha256'],
        'engine_provenance': prov,
        'catalog_sha256': file_sha256(lang_file) if lang_file.is_file() else '',
        'suite_sha256': file_sha256(DIRECTORY / 'suite.json'),
        'platform': f'{platform.system().lower()}-{platform.machine().lower()}',
        'profile': profile or '',
    }
