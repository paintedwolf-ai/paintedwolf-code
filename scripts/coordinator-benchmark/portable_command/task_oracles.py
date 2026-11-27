"""Output-integrity task helpers, verification-snapshot extraction, and artifact/schema oracles (Q20)."""
import contextlib
import hashlib
import json
import pathlib
import sqlite3
from portable_command.artifacts import delivered_suite, grade_artifacts, project_hashes
from verification_evidence import matches as verification_matches

TASK_HELPER_SCRIPT = r"""#!/bin/sh
exec python3 -B - "$@" <<'PY'
import json, pathlib, sys

LOGSTAT_PY = '''def summarize(records):
    count = len(records)
    if not count:
        return {'count': 0, 'mean_ms': 0, 'levels': {}}
    total = sum(float(r['latency_ms']) for r in records)
    levels = {}
    for r in records:
        key = str(r['level'])
        levels[key] = levels.get(key, 0) + 1
    return {'count': count, 'mean_ms': total / count, 'levels': levels}
'''

TEST_LOGSTAT_PY = '''import unittest
from logstat import summarize


class LogstatTests(unittest.TestCase):
    def test_empty(self):
        self.assertEqual(summarize([]), {'count': 0, 'mean_ms': 0, 'levels': {}})

    def test_fractional_and_empty_level(self):
        recs = [{'latency_ms': 0.5, 'level': ''}, {'latency_ms': 1.25, 'level': 'info'}, {'latency_ms': 2.75, 'level': 'info'}]
        self.assertEqual(summarize(recs), {'count': 3, 'mean_ms': 1.5, 'levels': {'': 1, 'info': 2}})
'''

WORDLE_PY = '''def _valid(word):
    return isinstance(word, str) and len(word) == 5 and word.isascii() and word.isalpha() and word.islower()


def score(secret, guess):
    if not _valid(secret) or not _valid(guess):
        raise ValueError('lowercase five-letter words only')
    marks = ['X'] * 5
    remaining = {}
    for i, (s, g) in enumerate(zip(secret, guess)):
        if s == g:
            marks[i] = 'G'
        else:
            remaining[s] = remaining.get(s, 0) + 1
    for i, g in enumerate(guess):
        if marks[i] == 'X' and remaining.get(g, 0) > 0:
            marks[i] = 'Y'
            remaining[g] -= 1
    return ''.join(marks)


def play(secret, guesses):
    if not _valid(secret):
        raise ValueError('lowercase five-letter words only')
    history, status = [], 'playing'
    for guess in guesses:
        if status != 'playing':
            break
        marks = score(secret, guess)
        history.append({'word': guess, 'marks': marks})
        if marks == 'GGGGG':
            status = 'won'
        elif len(history) >= 6:
            status = 'lost'
    return {'secret': secret, 'guesses': history, 'attempts': len(history), 'status': status}
'''

TEST_WORDLE_PY = '''import unittest
from wordle import play


class WordleTests(unittest.TestCase):
    def test_duplicate_aware_and_explicit_history(self):
        res = play('apple', ['allee', 'apple'])
        self.assertEqual(res, {'secret': 'apple', 'guesses': [{'word': 'allee', 'marks': 'GYXXG'}, {'word': 'apple', 'marks': 'GGGGG'}], 'attempts': 2, 'status': 'won'})

    def test_stops_after_win_and_rejects_invalid(self):
        self.assertEqual(play('apple', ['apple', 'allee'])['attempts'], 1)
        self.assertEqual(play('apple', [])['status'], 'playing')
        with self.assertRaises(ValueError):
            play('apple', ['BAD'])
'''

task = sys.argv[1] if len(sys.argv) > 1 else ''
cwd = pathlib.Path.cwd()
if task == 'logstat':
    (cwd / 'logstat.py').write_text(LOGSTAT_PY)
    (cwd / 'test_logstat.py').write_text(TEST_LOGSTAT_PY)
    ns = {}
    exec(LOGSTAT_PY, ns)
    out = ns['summarize']([{'latency_ms': 0.5, 'level': ''}, {'latency_ms': 1.25, 'level': 'info'}, {'latency_ms': 2.75, 'level': 'info'}])
    (cwd / 'result.json').write_text(json.dumps(out, separators=(',', ':')) + '\n')
elif task == 'wordle':
    (cwd / 'wordle.py').write_text(WORDLE_PY)
    (cwd / 'test_wordle.py').write_text(TEST_WORDLE_PY)
    ns = {}
    exec(WORDLE_PY, ns)
    out = ns['play']('apple', ['allee', 'apple'])
    (cwd / 'history.json').write_text(json.dumps(out, separators=(',', ':')) + '\n')
else:
    sys.stderr.write(f'unknown fixture-task: {task}\n')
    sys.exit(2)
PY
"""


def _git_blob_sha1(data):
    return hashlib.sha1(f'blob {len(data)}\0'.encode() + data).hexdigest()


def _table_exists(db, name):
    return db.execute("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", (name,)).fetchone() is not None


def _load_jsonl_verifications(evidence_dir, session_id):
    records = []
    if not evidence_dir.is_dir():
        return records
    for path in sorted(evidence_dir.rglob('*.jsonl')):
        for line in path.read_text(errors='replace').splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                rec = json.loads(line)
            except ValueError:
                continue
            if (isinstance(rec, dict) and rec.get('type') == 'verify'
                    and rec.get('slot') == 'tests' and rec.get('run_id') == session_id):
                records.append(rec)
    return records


def extract_verification_facts(db_path, session_id, project_dir):
    """Build verification_facts from closed store.db snapshots and host-recorded JSONL verify evidence."""
    db_p, proj_p = pathlib.Path(db_path), pathlib.Path(project_dir)
    if not db_p.is_file() or not proj_p.is_dir():
        return {}
    delivered_hashes = project_hashes(proj_p)
    delivered_git = {rel: _git_blob_sha1((proj_p / rel).read_bytes()) for rel in delivered_hashes}
    uri = db_p.resolve().as_uri() + '?mode=ro'
    with contextlib.closing(sqlite3.connect(uri, uri=True)) as db:
        db.row_factory = sqlite3.Row
        if not _table_exists(db, 'sessions'):
            return {}
        sess = db.execute('SELECT id, project_id FROM sessions WHERE id=?', (session_id,)).fetchone()
        if not sess:
            return {}
        project_id = sess['project_id']
        snapshots = {}
        if _table_exists(db, 'source_snapshots') and _table_exists(db, 'source_snapshot_roots'):
            has_bounds = _table_exists(db, 'source_snapshot_boundaries')
            has_entries = _table_exists(db, 'source_manifest_layout') and _table_exists(db, 'source_manifest_entries')
            for snap in db.execute('SELECT id, roots_key, capture_quality FROM source_snapshots ORDER BY id').fetchall():
                sid = snap['id']
                roots = [r['root_path'] for r in db.execute('SELECT root_path FROM source_snapshot_roots WHERE snapshot_id=? ORDER BY ordinal', (sid,)).fetchall()]
                matched = False
                if snap['capture_quality'] == 'exact' and len(roots) == 1 and has_entries:
                    bounds = db.execute('SELECT reason FROM source_snapshot_boundaries WHERE snapshot_id=?', (sid,)).fetchall() if has_bounds else []
                    budgeted = any(b['reason'] in {'directory_cap', 'subtree_cap', 'walk_budget', 'unreadable'} for b in bounds)
                    if not budgeted:
                        entries = db.execute(
                            'SELECT e.path, e.sha256, e.git_oid, e.identity FROM source_snapshot_roots r '
                            'CROSS JOIN source_manifest_layout l ON l.manifest_id=r.manifest_id '
                            'CROSS JOIN source_manifest_entries e ON e.chunk_id=l.chunk_id '
                            'WHERE r.snapshot_id=? AND r.root_path=?', (sid, roots[0]),
                        ).fetchall()
                        if len(entries) == len(delivered_hashes):
                            matched = all(
                                (e['identity'] == 'hashed' and e['sha256'] and e['sha256'] == delivered_hashes.get(e['path']))
                                or (e['identity'] == 'index' and e['git_oid'] and e['git_oid'] == delivered_git.get(e['path']))
                                for e in entries
                            )
                snapshots[sid] = {'id': sid, 'roots_key': snap['roots_key'], 'quality': snap['capture_quality'], 'roots': roots, 'matches_delivered': matched}
    verification = _load_jsonl_verifications(db_p.parent / 'projects' / project_id / 'evidence', session_id)
    return {
        'session_id': session_id, 'sessions': [{'id': session_id}],
        'delivered_paths': sorted(delivered_hashes), 'snapshots': snapshots, 'verification': verification,
    }


def _reference_wordle_marks(secret, guess):
    marks, rem = ['X'] * 5, {}
    for i, (s, g) in enumerate(zip(secret, guess)):
        if s == g:
            marks[i] = 'G'
        else:
            rem[s] = rem.get(s, 0) + 1
    for i, g in enumerate(guess):
        if marks[i] == 'X' and rem.get(g, 0) > 0:
            marks[i] = 'Y'
            rem[g] -= 1
    return ''.join(marks)


def inspect_task_integrity(case, *, db_path, session_id, project_dir, receipts, effects, baseline_hashes, final_hashes):
    """Verify Q20 output-integrity cases (fractional-logstat, wordle-explicit-history) via independent oracles."""
    req = set(case.get('required_evidence') or ())
    if not (req & {'independent_artifact_oracle', 'duplicate_aware_scoring', 'explicit_history_schema', 'verification_source_match'}):
        return {}
    proj = pathlib.Path(project_dir)
    exp_effects = (case.get('expected') or {}).get('effects') or []
    ext_ok = any(r.get('operation') == 'external_exec' and r.get('outcome') == 'completed' for r in receipts)
    reconciled_paths = [e['path'] for e in exp_effects if e.get('provenance') == 'external-reconciled' and e.get('path')]
    if ext_ok and reconciled_paths:
        reconciled = [
            {'operation': 'write', 'outcome': 'completed', 'path': p, 'accepted': True, 'published': True, 'provenance': 'external-reconciled'}
            for p in reconciled_paths
            if p in final_hashes and (baseline_hashes is None or final_hashes.get(p) != baseline_hashes.get(p))
        ]
        if reconciled:
            effects[:] = [e for e in effects if e.get('operation') != 'external_exec'] + reconciled
    facts = {}
    artifacts_ok = grade_artifacts(proj, case)
    if case['id'] == 'fractional-logstat':
        res_p = proj / 'result.json'
        try:
            payload = json.loads(res_p.read_text()) if res_p.is_file() else None
        except (OSError, ValueError):
            payload = None
        schema_ok = (isinstance(payload, dict) and set(payload) == {'count', 'mean_ms', 'levels'}
                     and payload['count'] == 3 and payload['mean_ms'] == 1.5
                     and isinstance(payload['levels'], dict) and payload['levels'].get('') == 1 and payload['levels'].get('info') == 2)
        facts['independent_artifact_oracle'] = bool(artifacts_ok and schema_ok)
    if case['id'] == 'wordle-explicit-history':
        hist_p = proj / 'history.json'
        try:
            payload = json.loads(hist_p.read_text()) if hist_p.is_file() else None
        except (OSError, ValueError):
            payload = None
        guesses = payload.get('guesses') if isinstance(payload, dict) else None
        schema_ok = (
            isinstance(payload, dict) and set(payload) == {'secret', 'guesses', 'attempts', 'status'}
            and isinstance(guesses, list) and payload['attempts'] == len(guesses) and len(guesses) >= 1
            and all(isinstance(g, dict) and set(g) == {'word', 'marks'} and isinstance(g['word'], str) and isinstance(g['marks'], str) for g in guesses)
            and payload['status'] in {'playing', 'won', 'lost'}
            and (payload['status'] != 'won' or guesses[-1]['marks'] == 'GGGGG')
        )
        scoring_ok = bool(
            schema_ok and artifacts_ok
            and all(_reference_wordle_marks(payload['secret'], g['word']) == g['marks'] for g in guesses)
            and any(g['word'] == 'allee' and g['marks'] == 'GYXXG' for g in guesses)
        )
        facts['explicit_history_schema'] = bool(schema_ok and artifacts_ok)
        facts['duplicate_aware_scoring'] = scoring_ok
    if 'delivered_tests_separate' in req:
        bfiles = list((case.get('expected') or {}).get('behavior_files') or ())
        test_files = [f for f in bfiles if pathlib.PurePosixPath(f).name.startswith('test')]
        impl_files = [f for f in bfiles if f not in test_files]
        separate = bool(test_files and impl_files and all((proj / f).is_file() and (proj / f).stat().st_size > 0 for f in bfiles))
        facts['delivered_tests_separate'] = bool(separate and delivered_suite(proj))
    if 'verification_source_match' in req:
        vf = extract_verification_facts(db_path, session_id, proj)
        lineage = {row['id'] for row in vf.get('sessions', [])}
        matched = bool(vf and any(verification_matches(vf, row, session_id, lineage) for row in vf.get('verification', [])))
        facts['verification_source_match'] = matched
        if vf:
            facts['_verification_facts'] = vf
    return facts
