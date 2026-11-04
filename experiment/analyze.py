#!/usr/bin/env python3
"""Scores every run from its provider requests, tool rows and final files."""
import collections, json, pathlib, re, sys
sys.path.insert(0, str(pathlib.Path(__file__).parent))
import fixture as FX

R = pathlib.Path.home()/'.local/share/paintedwolf-markdown-citations'/'artifacts/numbered/runs'
PRICE = dict(i=0.15, o=0.5, c=0.03)  # USD per Mtok, as tools/mc14/analyze.py
LABEL = re.compile(r'^ *\d+: ')
WIDE = re.compile(r'^ *\d{1,6}: ')
TAB = re.compile(r'^ *\d+\t')
MARK = re.compile(r'\n\[capture-body ([\w-]+)\]$')
STUB = re.compile(r'^\[capture-body ([\w-]+) — identical')
CODE = re.compile(r'\bCode: ([A-Z][A-Z0-9_]+)|"code":"([A-Z][A-Z0-9_]+)"|"rejection_code":"([A-Z][A-Z0-9_]+)"')
CITE = re.compile(r'([\w./-]+\.(?:go|py|yaml))(?::|#L)(\d+)(?:[-–](?:L)?(\d+))?')


def rows(d):
    out, bodies = [], {}
    p = d/'llm-requests.jsonl'
    if not p.exists():
        return out
    for line in open(p):
        try:
            e = json.loads(line)
        except Exception:
            continue
        for m in e.get('messages') or []:
            c = m.get('content')
            if isinstance(c, str):
                hit = MARK.search(c)
                if hit:
                    bodies[hit.group(1)] = c[:hit.start()]
                    m['content'] = bodies[hit.group(1)]
                else:
                    hit = STUB.match(c)
                    if hit and hit.group(1) in bodies:
                        m['content'] = bodies[hit.group(1)]
        out.append(e)
    return out


def tool_calls(d, rs):
    """Every call by id, with its args, plus result text as the provider saw it."""
    calls, results, order = {}, {}, []
    sources = [rs]
    msgs = json.load(open(d/'messages.json'))
    for e in rs:
        for m in e.get('messages') or []:
            for tc in m.get('tool_calls') or []:
                if tc['id'] not in calls:
                    calls[tc['id']] = dict(tc, agent=e.get('agent_type')); order.append(tc['id'])
            if m.get('role') == 'tool':
                tid = (m.get('tool_result') or {}).get('tool_call_id') or m.get('tool_call_id')
                if tid:
                    results[tid] = m.get('content') or ''
    for m in msgs:
        for tc in m.get('tool_calls') or []:
            if tc['id'] not in calls:
                calls[tc['id']] = dict(tc, agent='stored'); order.append(tc['id'])
        tr = m.get('tool_result') or {}
        if tr.get('tool_call_id') and tr['tool_call_id'] not in results:
            results[tr['tool_call_id']] = tr.get('content') or m.get('content') or ''
    return [calls[i] for i in order], results, msgs


def copied(text, original_lines, expected_lines):
    """Lines of an edit argument that carry a line label rather than file text."""
    found = []
    for line in (text or '').split('\n'):
        if line in original_lines or line in expected_lines:
            continue
        if WIDE.match(line) or TAB.match(line):
            found.append(line)
    return found


def rejected(result):
    head = result[:3000]
    codes = [a or b or c for a, b, c in CODE.findall(head)]
    return codes


def score_citations(task, text, final):
    src = final.split('\n')
    t = FX.TASKS[task]
    truth = {k: next(i+1 for i, l in enumerate(src) if v in l) for k, v in t['truth'].items()}
    out = dict(items=len(truth), correct=0, off_by_one=0, wrong=0, missing=0, detail={})
    for k, line in truth.items():
        m = re.search(rf'(?m)^\W*\(?{k}\)?\b[^\n]*', text or '')
        hit = CITE.search(m.group(0)) if m else None
        if not hit or not hit.group(1).endswith(t['path'].split('/')[-1]):
            out['missing'] += 1; out['detail'][k] = None; continue
        lo, hi = int(hit.group(2)), int(hit.group(3) or hit.group(2))
        out['detail'][k] = [lo, hi, line]
        if lo <= line <= hi and hi-lo <= 2:
            out['correct'] += 1
        elif min(abs(lo-line), abs(hi-line)) == 1:
            out['off_by_one'] += 1
        else:
            out['wrong'] += 1
    return out


def scan(d):
    run = json.load(open(d/'run.json'))
    task = run['task']; t = FX.TASKS[task]; rs = rows(d)
    calls, results, msgs = tool_calls(d, rs)
    original = FX.files()[t['path']]
    final_path = d/'final'/t['path']
    final = final_path.read_bytes().decode() if final_path.exists() else ''
    expected = t['expected'](original) if t['kind'] == 'edit' else original
    ol, el = set(original.split('\n')), set(expected.split('\n'))
    r = dict(run=d.name, task=task, cls=FX.CLASSES[task], arm=run['arm'], model=d.parent.parent.name, settled=run['settled'], seconds=run['seconds'], kind=t['kind'])
    # delivery
    blocks = numbered_rows = target_blocks = 0
    seen = set()
    for e in rs:
        for m in e.get('messages') or []:
            c = m.get('content') or ''
            if m.get('role') != 'tool' or 'File contents' not in c or m.get('id') in seen:
                continue
            seen.add(m.get('id'))
            for b in re.finditer(r'File contents "([^"]+)" \(lines (\d+)–(\d+)\)[^\n]*\n(`{3,}|~{3,})\n(.*?)\n\4', c, re.S):
                blocks += 1
                target_blocks += b.group(1) == t['path']
                numbered_rows += sum(1 for l in b.group(5).split('\n') if re.match(r' {0,5}\d+: ', l))
    r.update(blocks=blocks, target_blocks=target_blocks, numbered_rows=numbered_rows)
    names = collections.Counter(c['name'] for c in calls)
    r['tools'] = dict(names)
    r['commands'] = [c['args'].get('command', '')[:160] for c in calls if c['name'] == 'command']
    # edits
    edits = [c for c in calls if c['name'] == 'edit']
    r['edit_calls'] = len(edits); r['replace_lines_calls'] = names.get('replace_lines', 0)
    copies, fails, ee, codes = 0, 0, [], collections.Counter()
    for c in edits:
        a = c.get('args') or {}
        co, cn = copied(a.get('old_string'), ol, el), copied(a.get('new_string'), ol, el)
        res = results.get(c['id'], '')
        code = rejected(res)
        ok = not code and res != '' and ('"status":"failed"' not in res)
        copies += bool(co or cn); fails += not ok
        for x in code:
            codes[x] += 1
        ee.append(dict(old=a.get('old_string'), new=a.get('new_string'), copied_old=co, copied_new=cn, codes=code, ok=ok, agent=c.get('agent'), result=res[:400] if not ok else ''))
    r.update(label_copies=copies, edit_failures=fails, codes=dict(codes), edits=ee)
    # final state
    st = (d/'git-status.txt').read_text() if (d/'git-status.txt').exists() else ''
    changed = [l[3:] for l in st.splitlines()]
    others = [p for p in changed if p != t['path']]
    r['final_correct'] = final == expected
    r['changed_paths'] = changed; r['stray_paths'] = others
    r['success'] = r['final_correct'] and not others and run['settled']
    # labels in final files, commit messages and notes
    leaks = []
    for line in final.split('\n'):
        if line not in ol and line not in el and (WIDE.match(line) or TAB.match(line)):
            leaks.append(('file', line))
    log = (d/'git-log.txt').read_text() if (d/'git-log.txt').exists() else ''
    for line in log.split('\n'):
        if re.match(r' {2,}\d+: ', line):
            leaks.append(('commit', line))
    for c in calls:
        if c['name'] in ('update_progress', 'record_finding', 'complete_leg', 'surface_note', 'write'):
            for line in json.dumps(c.get('args'), ensure_ascii=False).replace('\\n', '\n').split('\n'):
                if re.match(r' {2,}\d+: ', line) and line not in ol:
                    leaks.append((c['name'], line))
    r['label_leaks'] = leaks
    # answer
    answer = ''
    for m in msgs:
        if m.get('role') == 'assistant' and (m.get('content') or '').strip() and m.get('visibility', 'transcript') == 'transcript':
            answer = m['content']
    r['answer_tail'] = answer[-1200:]
    if t['kind'] == 'cite':
        r['citations'] = score_citations(task, answer, original)
    # usage
    u = dict(i=0, o=0, c=0); calls_n = 0
    for e in rs:
        x = e.get('usage') or {}
        if x.get('prompt_tokens'):
            calls_n += 1
            u['i'] += x.get('prompt_tokens', 0); u['o'] += x.get('completion_tokens', 0); u['c'] += x.get('cache_read_input_tokens') or 0
    r['llm_calls'] = calls_n; r['tokens'] = u
    r['usd'] = (u['i']-u['c'])*PRICE['i']/1e6 + u['c']*PRICE['c']/1e6 + u['o']*PRICE['o']/1e6
    return r


def all_runs():
    out = []
    for p in sorted(R.glob('*/*/*/run.json')):
        out.append(scan(p.parent))
    return out


def table(rs):
    lines = ['| Model | Arm | Class | Runs | Success | Final correct | Edit calls | Label copies | Edit failures | replace_lines | Label in final | Saw target block | Used command | Citations correct / off-by-one / wrong / missing | Mean tokens in | Cost |',
             '|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|']
    keys = sorted({(r['model'], r['arm'], r['cls']) for r in rs})
    for k in keys:
        s = [r for r in rs if (r['model'], r['arm'], r['cls']) == k]
        cit = [r['citations'] for r in s if 'citations' in r]
        cs = '/'.join(str(sum(c[f] for c in cit)) for f in ('correct', 'off_by_one', 'wrong', 'missing')) if cit else ''
        lines.append(f"| {k[0]} | {k[1]} | {k[2]} | {len(s)} | {sum(r['success'] for r in s)} | {sum(r['final_correct'] for r in s)} | {sum(r['edit_calls'] for r in s)} | {sum(r['label_copies'] for r in s)} | {sum(r['edit_failures'] for r in s)} | {sum(r['replace_lines_calls'] for r in s)} | {sum(1 for r in s if any(x[0]=='file' for x in r['label_leaks']))} | {sum(1 for r in s if r['target_blocks'])} | {sum(1 for r in s if r['commands'])} | {cs} | {sum(r['tokens']['i'] for r in s)//max(1,len(s))} | ${sum(r['usd'] for r in s):.4f} |")
    return '\n'.join(lines)


if __name__ == '__main__':
    rs = all_runs()
    (R.parent/'scored.json').write_text(json.dumps(rs, indent=1, ensure_ascii=False))
    print(table(rs))
    print('total usd', round(sum(r['usd'] for r in rs), 4), 'runs', len(rs))
