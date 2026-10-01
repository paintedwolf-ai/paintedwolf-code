"""Produce an operational result for every planned slot, including unavailable measurements."""
from collections import Counter
import html
import json
import pathlib
from snapshot import file_hash
from grading_state import report_lease


def result(root):
    plan = json.loads((root / 'plan.json').read_text())
    with report_lease(root):
        measurements, grading = selected_measurements(root, plan)
    trials = []
    for item in plan['episodes']:
        path = root / 'attempts' / f"{item['index']}.json"
        receipt = json.loads(path.read_text()) if path.exists() else {}
        if receipt and receipt['item'] != item:
            raise ValueError('completion receipt differs from the planned slot')
        record = receipt.get('result') or {}
        blocked = receipt.get('state') == 'blocked' or record.get('error') == 'execution_unresolved'
        if receipt.get('state') == 'finished':
            measurement = measurements.get(str(item['index']), {})
            outcome = measurement.get('outcome', 'retained')
        else:
            measurement = {}
            outcome = 'unmeasured' if blocked else 'pending'
        trials.append({**item, 'outcome': outcome, 'measurement': measurement,
                       'execution': record, 'receipt': str(path.relative_to(root))})
    counts = Counter(t['outcome'] for t in trials)
    models = []
    for model in plan['models']:
        rows = [t for t in trials if t['model'] == model['id']]
        models.append({'id': model['id'], 'label': model.get('label', model['id']),
                       'outcomes': dict(Counter(t['outcome'] for t in rows))})
    phase = ('incomplete' if counts['pending'] else 'grading_blocked' if (grading or {}).get('phase') == 'blocked'
             else 'grading' if (grading or {}).get('phase') == 'running'
             else 'completed_with_unmeasured' if counts['unmeasured'] or counts['retained'] else 'completed')
    return {'version': 1, 'kind': 'execution-completion', 'execution_id': plan['run_id'],
            'phase': phase,
            'planned': len(trials), 'outcomes': dict(counts), 'models': models, 'trials': trials,
            'grading': grading}


def selected_measurements(root, plan):
    analysis = root / 'analysis.json'
    measurements = {}
    status_path = root / 'grading-status.json'
    grading = json.loads(status_path.read_text()) if status_path.exists() else None
    if analysis.exists() and grading is None:
        data = json.loads(analysis.read_text())
        if data['execution_id'] != plan['run_id']:
            raise ValueError('analysis belongs to a different execution')
        measurements = {str(t['index']): t['measurement'] for t in data['trials']}
    published = root / ('public.json' if plan.get('mode') == 'release' else 'preview.json')
    if grading is not None and grading.get('report_sha256'):
        published = pathlib.Path(grading['report_path'])
        if not published.is_absolute() or published.is_symlink():
            raise ValueError('graded report location is invalid')
    if published.exists() and (grading is None or grading.get('report_sha256')):
        if grading is not None and file_hash(published) != grading['report_sha256']:
            raise ValueError('graded report differs from the selected grading receipt')
        data = json.loads(published.read_text())
        if grading is not None and data['run_id'] != grading['report_id']:
            raise ValueError('graded report differs from the selected grading revision')
        if data['execution_id'] != plan['run_id']:
            raise ValueError('graded report belongs to a different execution')
        if len(plan['models']) != len(data['models']):
            raise ValueError('graded report changed the model roster')
        for model, graded in zip(plan['models'], data['models']):
            if model['configuration'] != graded['configuration']:
                raise ValueError('graded report changed model configuration')
            for case in graded['cases']:
                items = [item for item in plan['episodes'] if item['model'] == model['id'] and item['case'] == case['id']]
                if len(items) != len(case['trials']):
                    raise ValueError('graded report changed the sampling plan')
                for item, trial in zip(items, case['trials']):
                    measurements[str(item['index'])] = trial
    return measurements, grading


def write(root, save):
    data = result(root)
    save(root / 'completion.json', data)
    escape = lambda value: html.escape(str(value), quote=True)
    columns = ['passed', 'failed', 'unmeasured', 'retained', 'pending']
    rows = ''.join('<tr><th>' + escape(m['label']) + '</th>' +
                   ''.join('<td>' + str(m['outcomes'].get(c, 0)) + '</td>' for c in columns) + '</tr>'
                   for m in data['models'])
    unavailable = ''.join('<tr><td>' + escape(t['model']) + '</td><td>' + escape(t['case']) +
                          '</td><td>' + str(t['repeat'] + 1) + '</td><td>' +
                          escape(json.dumps(t['execution'], sort_keys=True)) + '</td></tr>'
                          for t in data['trials'] if t['outcome'] in {'unmeasured', 'retained', 'pending'})
    page = '<!doctype html><html lang="en"><meta charset="utf-8"><title>Benchmark run report</title>'
    page += '<style>body{font:16px system-ui;margin:3rem auto;max-width:1100px;padding:0 1rem;color:#24352e}table{border-collapse:collapse;width:100%;margin:2rem 0}th,td{text-align:left;padding:.8rem;border-bottom:1px solid #ccd8d0}td{overflow-wrap:anywhere}h1{font-size:2rem}</style>'
    page += '<h1>Benchmark run report</h1><p>' + escape(data['phase'].replace('_', ' ')) + '</p><table><tr><th>Model</th>'
    page += ''.join('<th>' + c.capitalize() + '</th>' for c in columns) + '</tr>' + rows + '</table>'
    page += '<p>Unmeasured trials are neither model passes nor failures. Retained trials have execution evidence but no grade in this report. Pending trials still need execution. This operational report is not a benchmark score.</p>'
    if errors := (data.get('grading') or {}).get('errors'):
        page += '<h2>Grading errors</h2><table><tr><th>Episode</th><th>Model</th><th>Fixture</th><th>Error</th></tr>'
        for error in errors:
            page += '<tr>' + ''.join('<td>' + escape(error.get(key, '')) + '</td>'
                                     for key in ('index', 'model', 'case', 'detail')) + '</tr>'
        page += '</table>'
    if (data.get('grading') or {}).get('recovery', {}).get('exhausted'):
        page += '<p>Independent grading remained unavailable after infrastructure recovery. Execution evidence is retained; unavailable grades do not count as model failures.</p>'
    if (root / 'analysis.html').exists() and data['grading'] is None:
        page += '<p><a href="analysis.html">Detailed measurements and private spending diagnostics</a></p>'
    if unavailable:
        page += '<h2>Unavailable measurements</h2><table><tr><th>Model</th><th>Scenario</th><th>Repetition</th><th>Recorded execution facts</th></tr>' + unavailable + '</table>'
    page += '<p><a href="completion.json">Machine-readable run result</a></p></html>'
    (root / 'completion.html').write_text(page)
    return data
