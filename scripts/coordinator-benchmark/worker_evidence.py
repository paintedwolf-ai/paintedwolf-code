"""Independent delegation expectations over typed application facts."""
import posixpath
import ledger


def worker_checks(capture, case, routed_workers, test_paths):
    facts=ledger.read(capture,case['session_id'])
    jobs=ledger.jobs(facts,case['session_id'])
    completed=[j for j in jobs if j['status']=='complete' and j['child_session_id'] in routed_workers]
    reviewers=[j for j in completed if j['mode']=='read']
    checks=[{'id':'worker-review','passed':bool(reviewers)}]
    if case['id']=='delegated-repair': return checks
    integrated={}
    for effect in facts['effects']:
        if effect['branch_id']=='' and effect['origin']=='agent' and effect['entry_kind']=='file' and effect['op'] in {'create','write','rename'}:
            integrated.setdefault(effect['job_id'],set()).add(posixpath.normpath(effect['path']))
    writers=[j for j in completed if j['mode']=='write' and j['merge_status']=='merged'
             and 'ranking.py' in integrated.get(j['id'],set()) and test_paths & integrated.get(j['id'],set())]
    return checks+[
        {'id':'independent-workers','passed':any(w['child_session_id']!=r['child_session_id'] for w in writers for r in reviewers)},
        {'id':'worker-contribution','passed':bool(writers)},
        {'id':'resolved-worker-jobs','passed':all(j['status'] in {'complete','failed','cancelled'}
         and j['merge_status'] in {'','merged','rejected','aborted'} for j in jobs)}]
