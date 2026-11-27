"""Case-specific setup/oracle helpers for Q11, Q18, Q19, Q20, Q21, and Q22."""
import pathlib
import subprocess
import tarfile
import zipfile
from portable_command.contracts import bytes_value, fixture_path
from portable_command.delivery_oracles import extract_deliveries, inspect_edit_and_bounds
from portable_command.lifecycle_oracles import inspect_temp_handoff_and_jobs
from portable_command.live import MODEL_FIELDS, SOURCE_FIELDS, freeze_plan, load_live_manifest, summarize_slots
from portable_command.profile_session import inspect_readonly_authority


def _run_git(project_dir, *args):
    return subprocess.run(
        ['git', '-C', str(project_dir), '-c', 'core.fsmonitor=false', *args],
        capture_output=True, text=True, check=False,
    )


def _is_hex40(value):
    return isinstance(value, str) and len(value) == 40 and all(c in '0123456789abcdef' for c in value.lower())


def materialize_host_facts(host_facts, project_dir):
    """Materialize or verify file-based host_facts (e.g. archive_fixture zip) without altering pinned bytes."""
    if not host_facts:
        return True, {}
    bindings, unhandled = {}, []
    for key, spec in host_facts.items():
        if key == 'archive_fixture' and isinstance(spec, dict) and spec.get('kind') == 'zip':
            zpath = pathlib.Path(project_dir) / 'hostile.zip'
            entries = spec.get('entries') or ()
            if zpath.is_file():
                with zipfile.ZipFile(zpath, 'r') as zf:
                    matched = [(n, zf.read(n)) for n in zf.namelist()] == [(e['path'], bytes_value(e['bytes'])) for e in entries]
                if not matched:
                    raise ValueError('existing hostile.zip does not match archive_fixture host_facts')
            else:
                with zipfile.ZipFile(zpath, 'w') as zf:
                    for entry in entries:
                        zf.writestr(entry['path'], bytes_value(entry['bytes']))
            bindings[key] = {'injected': True, 'path': 'hostile.zip', 'entries': len(entries)}
        else:
            bindings[key] = {'injected': False}
            unhandled.append(key)
    return not unhandled, bindings


def materialize_git_setup(git_spec, project_dir, setup_files=None):
    """Initialize a real Git repository in project_dir from setup.git."""
    if not isinstance(git_spec, dict) or not git_spec:
        return None
    root = pathlib.Path(project_dir)
    if _run_git(root, 'init').returncode != 0:
        raise RuntimeError('git init failed for fixture')
    author = git_spec.get('author') or {}
    _run_git(root, 'config', 'user.name', author.get('name') or 'Fixture author')
    _run_git(root, 'config', 'user.email', author.get('email') or 'fixture@example.test')
    for rel, body in (git_spec.get('initial_commit_files') or {}).items():
        target = root.joinpath(*fixture_path(rel).parts)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(bytes_value(body))
    if _run_git(root, 'add', '-A').returncode != 0 or _run_git(root, 'commit', '-m', 'Initial fixture commit').returncode != 0:
        raise RuntimeError('git initial commit failed for fixture')
    initial_head = _run_git(root, 'rev-parse', 'HEAD').stdout.strip()
    for rel, body in (setup_files or {}).items():
        target = root.joinpath(*fixture_path(rel).parts)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(bytes_value(body))
    staged = [str(p).lstrip('./') for p in (git_spec.get('staged') or ())]
    if staged and _run_git(root, 'add', '--', *staged).returncode != 0:
        raise RuntimeError('git add failed for pre-staged fixture paths')
    if git_spec.get('fsmonitor'):
        _run_git(root, 'config', 'core.fsmonitor', str(git_spec['fsmonitor']))
    if git_spec.get('external_diff'):
        _run_git(root, 'config', 'diff.external', str(git_spec['external_diff']))
    return {
        'initialized': True, 'initial_head': initial_head, 'staged': staged,
        'session_authored': [str(p).lstrip('./') for p in (git_spec.get('session_authored') or ())],
    }


def inspect_git_facts(case, project_dir, git_setup, effects):
    """Verify Q18 Git facts directly against the repository HEAD, tree diff, and index."""
    git_spec = (case.get('setup') or {}).get('git')
    if not isinstance(git_spec, dict) or not git_spec:
        return {}
    root = pathlib.Path(project_dir)
    if not (root / '.git').exists():
        return {'foreign_index_preserved': False, 'session_authored_paths': False, 'immutable_ref_evidence': False}
    initial_head = (git_setup or {}).get('initial_head', '')
    new_head = _run_git(root, 'rev-parse', 'HEAD').stdout.strip()
    head_advanced = bool(_is_hex40(new_head) and initial_head and new_head != initial_head)
    committed = []
    if head_advanced:
        diff_tree = _run_git(root, 'diff-tree', '--no-commit-id', '--name-only', '-r', 'HEAD')
        if diff_tree.returncode == 0:
            committed = sorted(p for p in diff_tree.stdout.splitlines() if p)
    staged_proc = _run_git(root, 'diff', '--cached', '--name-only')
    staged_now = sorted(p for p in staged_proc.stdout.splitlines() if p) if staged_proc.returncode == 0 else []
    expected_staged = [str(p).lstrip('./') for p in (git_spec.get('staged') or ())]
    expected_authored = [str(p).lstrip('./') for p in (git_spec.get('session_authored') or ())]
    setup_files = (case.get('setup') or {}).get('files') or {}
    staged_bytes_ok = all(
        subprocess.run(
            ['git', '-C', str(root), '-c', 'core.fsmonitor=false', 'show', f':{p}'],
            capture_output=True, check=False,
        ).stdout == bytes_value(setup_files[p])
        for p in expected_staged if p in setup_files
    )
    foreign_preserved = bool(
        head_advanced and expected_staged and set(expected_staged) <= set(staged_now)
        and not (set(expected_staged) & set(committed)) and staged_bytes_ok
    )
    authored_only = bool(head_advanced and expected_authored and set(committed) == set(expected_authored))
    if head_advanced and committed:
        for eff in effects:
            if eff.get('operation') in {'git_add', 'git_commit'} and eff.get('outcome') == 'completed' and 'paths' not in eff:
                eff['paths'] = list(committed)
    return {
        'foreign_index_preserved': foreign_preserved,
        'session_authored_paths': authored_only,
        'immutable_ref_evidence': head_advanced,
    }


def verify_tar_bz2(project_dir, receipts, effects, publications, final_hashes):
    """Cross-decode bundle.tar.bz2 with Python tarfile/bz2 and check member confinement and publication rows."""
    tar_path = pathlib.Path(project_dir) / 'bundle.tar.bz2'
    if not (tar_path.is_file() and any(r.get('operation') == 'tar_create' and r.get('outcome') == 'completed' for r in receipts)):
        return {}
    try:
        with tarfile.open(tar_path, 'r:bz2') as tf:
            members = tf.getmembers()
    except (OSError, tarfile.TarError):
        return {}
    for eff in effects:
        if eff.get('operation') == 'tar_extract' and 'path' not in eff and members:
            cand = f"out/{members[0].name.lstrip('./')}"
            if cand in final_hashes:
                eff['path'] = cand
    return {
        'real_bzip2_cross_decoder': bool(members),
        'confined_archive_entries': bool(members) and all(
            not m.name.startswith('/') and '..' not in pathlib.PurePosixPath(m.name).parts and not m.issym() and not m.islnk()
            for m in members
        ),
        'recoverable_publication': any(p.get('state') == 'committed' and p.get('materialized') == 1 for p in publications),
    }


def verify_paired_manifest_contract(case, identity):
    """Zero-model verification of live.json via load_live_manifest, freeze_plan, and summarize_slots (Q22)."""
    if case.get('id') != 'paired-manifest-contract':
        return {}
    manifest = load_live_manifest()
    zero_sha = '0' * 64
    source = {k: zero_sha for k in SOURCE_FIELDS}
    models = [
        {
            'role': m['role'], 'selection': m['selection'],
            **{k: (zero_sha if k.endswith('sha256') else (m.get('resolved_id') or f"contract-{m['role']}")) for k in MODEL_FIELDS},
            'runtime_identity_verified': True, 'alias_dependent': False,
        }
        for m in manifest['models']
    ]
    auth = {
        'allow_live': True, 'approval_reference': 'contract-dry-run',
        'isolated_host_reference': 'contract-dry-run', 'maximum_cost_usd': 1.0,
        'allowed_resolved_models': [m['resolved_model_id'] for m in models],
    }
    plan = freeze_plan(manifest, source, models, auth)
    summary = summarize_slots(plan, [])
    arms = manifest['arms']
    alt_ok = bool(plan['episodes']) and all(
        ep['arm'] == (arms[ep['order']] if ep['attempt'] % 2 == 1 else arms[1 - ep['order']])
        for ep in plan['episodes']
    )
    prov = (identity or {}).get('engine_provenance') or {}
    return {
        'five_attempts_preregistered': manifest.get('attempts_per_task_model_arm') == 5 and manifest['budgets'].get('attempts') == 5,
        'alternating_arm_order': arms == ['real-shell-reference', 'production-native'] and alt_ok,
        'all_attempts_retained': summary['planned'] == len(plan['episodes']) and not summary['qualified'] and not summary['accepted'],
        'immutable_identity': bool((identity or {}).get('git_head') and (identity or {}).get('git_tree') and (identity or {}).get('suite_sha256') and prov.get('provenance_verified') is True),
    }


def extra_case_facts(case, *, session_id, identity, project_dir, git_setup, receipts, effects, audit, recovery, last_payload, final_hashes, baseline_hashes, sidecar=None, session_surface=None, last_raw=None, no_external_launch=False):
    """Collect Q05, Q06, Q11, Q18, Q19, Q20, Q21, and Q22 facts from SQLite audit rows, sidecar log, and repo state."""
    execs = audit['invocation_program_executions']
    pins = audit['invocation_program_revision_pins']
    deliveries, delivery_facts = extract_deliveries(case, audit, baseline_hashes, project_dir=project_dir)
    facts = dict(delivery_facts)
    if recovery and recovery.get('owner_code') == 'PROGRESS_MISSING':
        all_refused = bool(execs) and all(e.get('state') in {'refused', 'rejected'} and e.get('effect_certainty') in {None, '', 'none'} for e in execs)
        zero_ok = bool(all_refused and last_payload.get('completed_effects', 0) == 0 and last_payload.get('uncertain_effects', 0) == 0)
        ref_details = (last_payload.get('refusal') or {}).get('details') if isinstance(last_payload.get('refusal'), dict) else {}
        remedy = 'update_progress' if (
            isinstance(ref_details, dict) and 'progress' in str(ref_details.get('expected_form') or '').lower()
        ) else recovery.get('remedy', '')
        recovery = {**recovery, 'cause': 'missing_progress', 'remedy': remedy}
        if zero_ok:
            recovery = {**recovery, 'zero_program_effects': True, 'effect_certainty_required': True}
            facts['zero_program_effects'] = baseline_hashes is None or final_hashes == baseline_hashes
        if (last_payload.get('detail_ref') or '').startswith('output:program-source:') and last_payload.get('replay_available') is False:
            facts['validated_resubmission_reference'] = True
    if not (case.get('setup') or {}).get('files'):
        prog_sources = audit.get('invocation_program_sources') or []
        if prog_sources and all(len(s.get('sha256') or '') == 64 for s in prog_sources):
            facts['exact_source_binding'] = True
    ext_ids = {r['execution_id'] for r in receipts if r.get('operation') == 'external_exec'}
    if ext_ids and any(p.get('basis') == 'declared_at_launch' and p.get('execution_id') in ext_ids and len(p.get('disk_sha256') or '') == 64 for p in pins):
        facts['disk_provenance'] = True
    if recovery and recovery.get('owner_code'):
        no_escape = not (pathlib.Path(project_dir).parent / 'escape.txt').exists()
        all_refused = all(e.get('state') in {'refused', 'rejected'} and e.get('effect_certainty') in {None, '', 'none'} for e in execs)
        zero_ok = bool(recovery.get('zero_program_effects') or (
            all_refused and last_payload.get('completed_effects', 0) == 0 and last_payload.get('uncertain_effects', 0) == 0
        ))
        facts['no_outside_effect'] = bool(zero_ok and no_escape and (baseline_hashes is None or final_hashes == baseline_hashes))
        if any(tok in recovery['owner_code'].lower() for tok in ('zip', 'traversal', 'escape', 'archive', 'path')):
            facts['zip_slip_refused'] = True
    facts.update(verify_tar_bz2(project_dir, receipts, effects, audit['invocation_program_publications'], final_hashes))
    facts.update(inspect_temp_handoff_and_jobs(case, session_id=session_id, audit=audit, receipts=receipts, sidecar=sidecar, last_payload=last_payload))
    ro_facts, recovery = inspect_readonly_authority(
        case, session_surface=session_surface, project_dir=project_dir, audit=audit, recovery=recovery,
        last_payload=last_payload, last_raw=last_raw, baseline_hashes=baseline_hashes,
        final_hashes=final_hashes, no_external_launch=no_external_launch,
    )
    facts.update(ro_facts)
    facts.update(inspect_edit_and_bounds(case, audit, receipts, effects, deliveries, sidecar, baseline_hashes, final_hashes))
    facts.update(inspect_git_facts(case, project_dir, git_setup, effects))
    facts.update(verify_paired_manifest_contract(case, identity))
    return facts, recovery, deliveries
