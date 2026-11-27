"""Delivery, structured-edit, bounded-visit, and confined-launch oracles (Q05, Q06, Q20, Q21)."""
import json
import pathlib
from portable_command.contracts import ROOT, bytes_value


def _traversal_visits_limit():
    lang = ROOT / 'lycaon/config/packs/painted-wolf/platform/tools/command-language-v1/language.json'
    if not lang.is_file():
        return 0
    try:
        data = json.loads(lang.read_text())
    except (OSError, ValueError):
        return 0
    for lim in (data.get('limits') or (data.get('language') or {}).get('limits') or ()):
        if lim.get('id') == 'traversal_visits' and isinstance(lim.get('maximum'), int):
            return lim['maximum']
    return 0


def _line_span_from_bytes(raw_bytes, start_byte, end_byte):
    if not raw_bytes or end_byte <= start_byte or start_byte < 0:
        return None
    start_line = raw_bytes[:start_byte].count(b'\n') + 1
    end_slice = raw_bytes[:max(start_byte, end_byte - 1)]
    end_line = end_slice.count(b'\n') + 1
    return [start_line, max(start_line, end_line)]


def _resolve_source_path(src, project_dir):
    direct = str(src.get('Path') or src.get('path') or '').lstrip('./')
    if direct:
        return direct
    sid = str(src.get('SourceID') or src.get('source_id') or '')
    if not sid:
        return ''
    raw = sid[5:] if sid.startswith('disk:') else sid
    if project_dir and raw.startswith('/'):
        try:
            return pathlib.Path(raw).resolve().relative_to(pathlib.Path(project_dir).resolve()).as_posix()
        except ValueError:
            pass
    return raw.lstrip('./')


def extract_deliveries(case, audit, baseline_hashes, project_dir=None):
    """Extract source-owner-confirmed deliveries from invocation_program_deliveries."""
    rows = audit.get('invocation_program_deliveries') or []
    if not rows:
        return [], {}
    revisions = (case.get('setup') or {}).get('revisions') or {}
    setup_files = (case.get('setup') or {}).get('files') or {}
    grouped = {}
    all_owner_refs = True
    all_ranges_exact = True
    max_source_end_by_path = {}
    for row in rows:
        owner_ref = row.get('source_owner_ref') or ''
        if not owner_ref:
            all_owner_refs = False
        raw_map = row.get('mappings_json') or ''
        try:
            mappings = json.loads(raw_map) if isinstance(raw_map, str) and raw_map else []
        except ValueError:
            mappings = []
        if not isinstance(mappings, list) or not mappings:
            all_ranges_exact = False
            continue
        for m in mappings:
            if not isinstance(m, dict):
                all_ranges_exact = False
                continue
            src = m.get('Source') or m.get('source') or {}
            path = _resolve_source_path(src, project_dir)
            s_start = m.get('SourceStartByte', m.get('source_start_byte'))
            s_end = m.get('SourceEndByte', m.get('source_end_byte'))
            o_start = m.get('OutputStartByte', m.get('output_start_byte'))
            o_end = m.get('OutputEndByte', m.get('output_end_byte'))
            if not (isinstance(s_start, int) and isinstance(s_end, int) and isinstance(o_start, int) and isinstance(o_end, int)
                    and s_start >= 0 and s_end > s_start and (o_end - o_start) == (s_end - s_start)):
                all_ranges_exact = False
                continue
            if not path:
                all_ranges_exact = False
                continue
            max_source_end_by_path[path] = max(max_source_end_by_path.get(path, 0), s_end)
            printed = m.get('PrintedLine', m.get('printed_line', 0))
            if isinstance(printed, int) and printed > 0:
                span = [printed, printed]
            else:
                raw_file = bytes_value(setup_files[path]) if path in setup_files else b''
                span = _line_span_from_bytes(raw_file, s_start, s_end)
            sha = src.get('SHA256') or src.get('ContentSHA256') or src.get('content_sha256') or ''
            rev = revisions.get(path) if (path in revisions and (not baseline_hashes or not sha or sha == baseline_hashes.get(path))) else (sha or '')
            raw_rep = str(src.get('Representation') or src.get('representation') or '')
            rep = 'numbered-reversible' if (printed and printed > 0) or 'numbered' in raw_rep else 'literal'
            key = (path, rev, rep)
            entry = grouped.setdefault(key, {'path': path, 'revision': rev, 'lines': None, 'representation': rep, 'delivered': bool(owner_ref)})
            entry['delivered'] = entry['delivered'] and bool(owner_ref)
            if span:
                entry['lines'] = span if entry['lines'] is None else [min(entry['lines'][0], span[0]), max(entry['lines'][1], span[1])]
    deliveries = list(grouped.values())
    facts = {}
    if deliveries:
        facts['delivery_owner_identity'] = all_owner_refs
        facts['exact_mapped_ranges'] = all_ranges_exact
        outputs = audit.get('invocation_program_outputs') or []
        if any(o.get('stream') == 'stdout' and o.get('preview_truncated') == 1 for o in outputs):
            unseen_ok = all(
                max_source_end_by_path[p] < len(bytes_value(setup_files[p]))
                for p in max_source_end_by_path if p in setup_files
            ) and bool(max_source_end_by_path)
            facts['unseen_ranges_not_read_bases'] = unseen_ok
    return deliveries, facts


def inspect_edit_and_bounds(case, audit, receipts, effects, deliveries, sidecar, baseline_hashes, final_hashes):
    """Verify structured edit anchoring (Q05), bounded find visits (Q01/Q20), and confined launch (Q21)."""
    facts = {}
    execs = audit['invocation_program_executions']
    pins = audit['invocation_program_revision_pins']
    segments = audit.get('invocation_program_revision_pin_segments') or []
    # 1) Q05 anchored_edit: top-level edit receipt in invocation_receipts backed by delivered path + changed bytes.
    edit_receipts = [r for r in audit.get('invocation_receipts') or () if r.get('tool_name') == 'edit' and r.get('status') == 'completed']
    if edit_receipts:
        turn_paths = [
            str((t.get('arguments') or {}).get('path') or '').lstrip('./')
            for t in (case.get('turns') or ()) if t.get('structured_tool') == 'edit'
        ]
        delivered_paths = {d['path'] for d in deliveries if d.get('delivered') is True}
        anchored = False
        for er, rel_path in zip(edit_receipts, turn_paths):
            changed = bool(rel_path and rel_path in final_hashes and (baseline_hashes is None or final_hashes.get(rel_path) != baseline_hashes.get(rel_path)))
            row = {'execution_id': er['id'], 'operation': 'write', 'outcome': 'completed', 'path': rel_path, 'accepted': True, 'published': True}
            if not any(r.get('execution_id') == er['id'] for r in receipts):
                receipts.append(row)
                effects.append({k: v for k, v in row.items() if k != 'execution_id'})
            if rel_path in delivered_paths and changed:
                anchored = True
        facts['anchored_edit'] = anchored
    # 2) Q01/Q20 bounded_visits: completed find pin rows + pin_segments within language.json traversal_visits limit.
    find_execs = [e for e in execs if e.get('native_command') == 'find' and e.get('state') == 'completed' and e.get('complete') == 1]
    if find_execs:
        limit = _traversal_visits_limit()
        total_pins = 0
        for fe in find_execs:
            total_pins += sum(1 for p in pins if p.get('execution_id') == fe['id'])
            total_pins += sum(int(s.get('pin_count') or 0) for s in segments if s.get('execution_id') == fe['id'] and len(s.get('body_sha256') or '') == 64)
        if total_pins > 0 and limit > 0:
            facts['bounded_visits'] = total_pins <= limit
    # 3) Q21 confined_launch: sidecar.log process boundary selected with component=sandbox & host_execution=false.
    if any(r.get('operation') == 'external_exec' and r.get('outcome') == 'completed' for r in receipts):
        if isinstance(sidecar, dict) and sidecar.get('available') and sidecar.get('complete') and sidecar.get('lines'):
            facts['confined_launch'] = any(
                'component=sandbox' in (m.get('excerpt') or '') and 'host_execution=false' in (m.get('excerpt') or '')
                for m in sidecar['lines']
            )
    return facts
