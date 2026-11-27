"""Git fixture setup/oracle (Q18) and zero-model paired manifest verification (Q22)."""
import pathlib
import subprocess
from portable_command.contracts import bytes_value, fixture_path
from portable_command.live import MODEL_FIELDS, SOURCE_FIELDS, freeze_plan, load_live_manifest, summarize_slots


def _run_git(project_dir, *args):
    return subprocess.run(
        ['git', '-C', str(project_dir), '-c', 'core.fsmonitor=false', *args],
        capture_output=True, text=True, check=False,
    )


def _is_hex40(value):
    return isinstance(value, str) and len(value) == 40 and all(c in '0123456789abcdef' for c in value.lower())


def materialize_git_setup(git_spec, project_dir, setup_files=None):
    """Initialize a real Git repository in project_dir from setup.git without breaking setup."""
    if not isinstance(git_spec, dict) or not git_spec:
        return None
    root = pathlib.Path(project_dir)
    if _run_git(root, 'init').returncode != 0:
        raise RuntimeError('git init failed for fixture')
    author = git_spec.get('author') or {}
    name = author.get('name') or 'Fixture author'
    email = author.get('email') or 'fixture@example.test'
    _run_git(root, 'config', 'user.name', name)
    _run_git(root, 'config', 'user.email', email)
    for rel, body in (git_spec.get('initial_commit_files') or {}).items():
        target = root.joinpath(*fixture_path(rel).parts)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(bytes_value(body))
    if _run_git(root, 'add', '-A').returncode != 0:
        raise RuntimeError('git add -A failed for fixture initial commit')
    if _run_git(root, 'commit', '-m', 'Initial fixture commit').returncode != 0:
        raise RuntimeError('git commit failed for fixture initial commit')
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
        'initialized': True,
        'initial_head': initial_head,
        'staged': staged,
        'session_authored': [str(p).lstrip('./') for p in (git_spec.get('session_authored') or ())],
    }


def inspect_git_facts(case, project_dir, git_setup, receipts, effects, audit):
    """Verify Q18 Git facts directly against the real repository state after execution."""
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


def verify_paired_manifest_contract(case, identity):
    """Zero-model verification of the frozen live.json paired qualification contract (Q22)."""
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
