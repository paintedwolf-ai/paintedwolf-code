"""Engine provenance, durable per-case evidence archiving, Q-ID qualification, and discrepancy breakdown."""
import hashlib
import json
import os
import pathlib
import platform
import subprocess
import uuid
import smoke
from built_runtime import engine_path
from portable_command.contracts import DIRECTORY, ROOT, bytes_value
from progress import save

SETUP_GAPS = {'profile_binding_approximate', 'uninjected_drafts', 'uninjected_host_facts'}
OBSERVATION_GAPS = {'missing_sidecar_log', 'incomplete_sidecar_log', 'missing_program_status_code'}


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


def make_app_factory(engine_bin=None, base_factory=smoke.Application):
    """Wrap smoke.Application so an explicit engine binary or PW_ENGINE_BIN is honored."""
    if base_factory is not smoke.Application:
        return base_factory
    binary, _ = resolve_engine_binary(ROOT, engine_bin)
    def factory(directory):
        orig = smoke.engine_path
        smoke.engine_path = lambda _root: binary
        try:
            return base_factory(directory)
        finally:
            smoke.engine_path = orig
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


def inspect_engine_provenance(root=ROOT, engine_bin=None, evaluator_git_head=''):
    """Bind the engine executable and build/source identity independently from the evaluator checkout."""
    binary, build_source = resolve_engine_binary(root, engine_bin)
    exists = binary.is_file()
    sha = file_sha256(binary) if exists else ''
    product_version = ''
    go_version = ''
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
    vcs_rev = vcs.get('vcs.revision', '')
    matches_head = bool(vcs_rev and evaluator_git_head and evaluator_git_head.startswith(vcs_rev)) if vcs_rev else None
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
        'matches_evaluator_head': matches_head,
    }


def source_identity(root=ROOT, profile=None, engine_bin=None):
    """Capture immutable evaluator checkout identity and independent engine provenance."""
    git_head = _git(root, 'rev-parse', 'HEAD')
    git_tree = _git(root, 'rev-parse', 'HEAD^{tree}')
    version_file = root / 'VERSION'
    lang_file = root / 'lycaon/config/packs/painted-wolf/platform/tools/command-language-v1/language.json'
    prov = inspect_engine_provenance(root, engine_bin=engine_bin, evaluator_git_head=git_head)
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


def slice_sidecar_log(log_path, session_id=None, max_edge=40):
    """Retain opening, session/launch-relevant, and closing/shutdown lines for durable audit."""
    path = pathlib.Path(log_path)
    if not path.is_file():
        return None
    lines = path.read_text(errors='replace').splitlines()
    if len(lines) <= max_edge * 2:
        return '\n'.join(lines) + ('\n' if lines else '')
    prefix = f'session_id={session_id[:8]}' if session_id else ''
    keep = set(range(max_edge)) | set(range(len(lines) - max_edge, len(lines)))
    for idx, line in enumerate(lines):
        low = line.lower()
        if (prefix and prefix in line) or 'process boundary selected' in line or 'shutdown' in low:
            keep.add(idx)
    out = [f'{i + 1}: {lines[i]}' for i in sorted(keep)]
    return '\n'.join(out) + '\n'


def archive_case_evidence(case_dir, bundle_root, *, case_id, session_id=None):
    """Copy durable per-case raw records and return locators with SHA-256 digests."""
    case_dir = pathlib.Path(case_dir)
    dest = pathlib.Path(bundle_root).resolve() / case_id
    dest.mkdir(parents=True, exist_ok=True)
    files = {}
    candidates = (
        'attempt.json', 'observation.json', 'grade.json',
        'raw/invocations.json', 'raw/messages.json', 'raw/events.jsonl',
        'raw/db-audit.json', 'raw/window.json', 'raw/prelude.json', 'raw/filesystem.json',
    )
    for rel in candidates:
        src = case_dir / rel
        if src.is_file():
            target = dest / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            data = src.read_bytes()
            target.write_bytes(data)
            files[rel] = {'path': str(target), 'sha256': hashlib.sha256(data).hexdigest(), 'bytes': len(data)}
    sliced = slice_sidecar_log(case_dir / 'app' / 'sidecar.log', session_id=session_id)
    if sliced is not None:
        target = dest / 'raw' / 'sidecar-slice.log'
        target.parent.mkdir(parents=True, exist_ok=True)
        data = sliced.encode('utf-8')
        target.write_bytes(data)
        files['raw/sidecar-slice.log'] = {'path': str(target), 'sha256': hashlib.sha256(data).hexdigest(), 'bytes': len(data)}
    manifest = {'case_id': case_id, 'bundle_dir': str(dest), 'files': files}
    save(dest / 'manifest.json', manifest)
    return manifest


def classify_case_disposition(record):
    """Assign explicit qualified/unqualified disposition and discrepancy category."""
    gaps = list(record.get('observation_gaps') or [])
    reasons = list(gaps)
    if record.get('has_progress_update') and record.get('native_envelope_qualified') is not True:
        if 'sibling_progress_substitute' not in reasons:
            reasons.append('sibling_progress_substitute')
    if record.get('attempt') != 'measured':
        if 'unmeasured_attempt' not in reasons:
            reasons.append('unmeasured_attempt')
    if record.get('qualification') != 'native-runtime':
        if 'non_native_qualification' not in reasons:
            reasons.append('non_native_qualification')
    disposition = 'qualified' if not reasons else 'unqualified'
    qualified_pass = bool(disposition == 'qualified' and record.get('passed') is True)
    categories = []
    if record.get('has_progress_update') and record.get('native_envelope_qualified') is not True:
        categories.append('proven_contract_mismatch')
    if any(g in SETUP_GAPS for g in gaps) or record.get('attempt') != 'measured':
        categories.append('missing_setup')
    if any(g in OBSERVATION_GAPS for g in gaps):
        categories.append('missing_observation')
    if not qualified_pass and not categories:
        categories.append('runtime_issue')
    primary = None if qualified_pass else categories[0]
    return {
        'disposition': disposition,
        'unqualified_reasons': reasons,
        'qualified_pass': qualified_pass,
        'discrepancy_category': primary,
        'discrepancy_categories': [] if qualified_pass else categories,
    }


def aggregate_qualification(all_cases, records):
    """Aggregate Q01-Q22 strictly: a Q ID passes only when all required cases have qualified native passes."""
    by_id = {r['case_id']: r for r in records}
    planned_by_q = {}
    for c in all_cases:
        for q in c['qualification_cases']:
            planned_by_q.setdefault(q, []).append(c['id'])
    per_q = {}
    passed_selected, passed_full = [], []
    for q in sorted(planned_by_q):
        planned = planned_by_q[q]
        selected = [cid for cid in planned if cid in by_id]
        qual_passed = [cid for cid in selected if by_id[cid]['qualified_pass']]
        diag_passed = [cid for cid in selected if by_id[cid]['passed']]
        unqualified = [cid for cid in selected if by_id[cid]['disposition'] != 'qualified']
        sel_ok = bool(selected) and len(qual_passed) == len(selected)
        full_ok = len(selected) == len(planned) and len(qual_passed) == len(planned)
        if sel_ok:
            passed_selected.append(q)
        if full_ok:
            passed_full.append(q)
        per_q[q] = {
            'planned_cases': planned,
            'selected_cases': selected,
            'qualified_passed_cases': qual_passed,
            'diagnostic_passed_cases': diag_passed,
            'unqualified_cases': unqualified,
            'selected_complete': sel_ok,
            'full_complete': full_ok,
            'status': 'passed_full' if full_ok else ('passed_selected_partial' if sel_ok else 'not_passed'),
        }
    return per_q, passed_selected, passed_full


def build_discrepancy_breakdown(records):
    """Break all non-qualified-passing cases down by missing_setup, missing_observation, proven_contract_mismatch, runtime_issue."""
    buckets = {k: [] for k in ('missing_setup', 'missing_observation', 'proven_contract_mismatch', 'runtime_issue')}
    for r in records:
        cat = r.get('discrepancy_category')
        if cat in buckets:
            buckets[cat].append(r['case_id'])
    return {
        'counts': {k: len(v) for k, v in buckets.items()},
        'cases': buckets,
        'total_discrepancies': sum(len(v) for v in buckets.values()),
    }
