"""Measure coordinator protocol obligations from host-authored transcript facts."""
from closeout_evidence import final_grounding
from episode_evidence import read, member


def closeout_checks(capture, case, required_paths):
    grounding = final_grounding(read(capture,case['session_id']),case['session_id'])
    valid = (grounding.get('traced') is True and not grounding.get('host_assembled',False)
             and all(c['status']!='failed' for c in grounding.get('checks',[])))
    cited = {c.get('path') for c in grounding.get('cited_evidence',[])
             if c.get('handle') and c.get('verdict') in {'matched','traced'}}
    return [{'id':'model-authored-closeout-citations','passed':valid and set(required_paths)<=cited}]


def progress_checks(capture, case, expected):
    entry = case['preparation']['transcript_seq']
    messages=member(read(capture,case['session_id']),case['session_id'])['messages']
    completed=[m['progress_complete'] for m in messages if m['origin']=='host' and m.get('kind')=='progress_complete'
               and m.get('seq',0)>entry and m.get('progress_complete')]
    updated=any(m['role']=='tool' and m['origin']=='tool' and m.get('seq',0)>entry
                and (m.get('tool_result') or {}).get('outcome')=='completed'
                and ((m.get('tool_result') or {}).get('invocation') or {}).get('tool')=='update_progress'
                and ((m.get('tool_result') or {}).get('invocation') or {}).get('invoked') is True for m in messages)
    steps=completed[-1].get('steps',[]) if completed else []
    actual = {s['label']:s['state'] for s in steps}
    allowed = {label: set(state) if isinstance(state, list) else {state} for label, state in expected.items()}
    valid = len(actual)==len(steps) and all(actual.get(label) in states for label,states in allowed.items())
    return [{'id':'reconciled-existing-progress','passed':bool(updated) and valid}]
