#!/usr/bin/env python3
"""Applies the brief's decision rule to the scored runs. Task success is read per class,
the strict reading, fixed before results; the pooled figure is printed beside it."""
import json, pathlib

P = pathlib.Path.home()/'.local/share/paintedwolf-markdown-citations/artifacts/numbered/scored.json'
rs = json.load(open(P))
EDIT = ['whitespace', 'tabs', 'multiline', 'colon_digits', 'duplicate']


def rate(n, d):
    return n/d if d else None


for model in sorted({r['model'] for r in rs}):
    arm = {a: [r for r in rs if r['model'] == model and r['arm'] == a] for a in ('unlabeled', 'numbered')}
    e = {a: [r for r in arm[a] if r['kind'] == 'edit'] for a in arm}
    c = {a: [r['citations'] for r in arm[a] if r['kind'] == 'cite'] for a in arm}
    calls = {a: sum(r['edit_calls'] for r in e[a]) for a in e}
    fails = {a: sum(r['edit_failures'] for r in e[a]) for a in e}
    copies = sum(r['label_copies'] for r in e['numbered'])
    leak = sum(1 for r in arm['numbered'] if any(x[0] == 'file' for x in r['label_leaks']))
    copy_rate, fail_rate = rate(copies, calls['numbered']), rate(fails['unlabeled'], calls['unlabeled'])
    structural = {a: sum(r['codes'].get('EDITOR_DOCUMENT_READ_REQUIRED', 0) > 0 for r in e[a]) for a in e}
    print(f'== {model}: edit runs {len(e["unlabeled"])}/{len(e["numbered"])}, edit calls {calls}, failures {fails}')
    print(f'  numbered label in final file: {leak} runs -> {"pass" if leak == 0 else "FAIL"}')
    ok_copy = copy_rate is not None and (copy_rate <= 0.05 or (fail_rate is not None and copy_rate <= fail_rate))
    print(f'  label copies {copies}/{calls["numbered"]} = {copy_rate}; unlabeled edit failure rate {fail_rate} -> {"pass" if ok_copy else "FAIL"}')
    ok_success = True
    for cls in EDIT:
        s = {a: [r for r in e[a] if r['cls'] == cls] for a in e}
        ru, rn = rate(sum(r['success'] for r in s['unlabeled']), len(s['unlabeled'])), rate(sum(r['success'] for r in s['numbered']), len(s['numbered']))
        good = ru is not None and rn is not None and rn >= ru-0.125
        ok_success &= good
        print(f'  success {cls}: unlabeled {sum(r["success"] for r in s["unlabeled"])}/{len(s["unlabeled"])} numbered {sum(r["success"] for r in s["numbered"])}/{len(s["numbered"])} -> {"pass" if good else "FAIL"}')
    pu, pn = rate(sum(r['success'] for r in e['unlabeled']), len(e['unlabeled'])), rate(sum(r['success'] for r in e['numbered']), len(e['numbered']))
    print(f'  success pooled: unlabeled {pu} numbered {pn}; strict per class -> {"pass" if ok_success else "FAIL"}')
    cu = {a: (sum(x['correct'] for x in c[a]), sum(x['items'] for x in c[a])) for a in c}
    au, an = rate(*cu['unlabeled']), rate(*cu['numbered'])
    ok_cite = au is not None and an is not None and an >= au
    print(f'  citations correct: {cu} -> {"pass" if ok_cite else "FAIL"}')
    print(f'  rule for {model}: {"RESTORE" if leak == 0 and ok_copy and ok_success and ok_cite else "KEEP STRIPPED"}')
    print(f'  runs with structural EDITOR_DOCUMENT_READ_REQUIRED: {structural}')
    print(f'  numbered runs that received numbered rows: {sum(1 for r in arm["numbered"] if r["numbered_rows"])}/{len(arm["numbered"])}; unlabeled with numbered rows: {sum(1 for r in arm["unlabeled"] if r["numbered_rows"])}')
    print(f'  cost {sum(r["usd"] for r in arm["unlabeled"]+arm["numbered"]):.4f} usd list price')
