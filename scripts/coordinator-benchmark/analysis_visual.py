"""Render private pilot diagnostics without suggesting a release score."""
import html


def render(data):
    escape=html.escape
    models=sorted({t['model'] for t in data['trials']})
    matrix=[]; costs=[]
    maximum=max((s['priced_usd'] for s in data['scenarios']),default=1) or 1
    for scenario in data['scenarios']:
        cells=[]
        for model in models:
            trials=[t for t in data['trials'] if t['case']==scenario['id'] and t['model']==model]
            measured=[t for t in trials if t['measurement']['outcome'] in ['passed','failed']]
            passed=sum(t['measurement']['outcome']=='passed' for t in measured)
            label=f'{passed}/{len(measured)}' if measured else 'Pending'
            shade=round(10+65*passed/len(measured)) if measured else 0
            extra=f' · {len(trials)-len(measured)} unmeasured' if len(trials)!=len(measured) else ''
            cells.append(f'<td style="background:hsl(145 20% {100-shade/2}%)">{label}<small>{escape(extra)}</small></td>')
        matrix.append('<tr><th scope="row">'+escape(scenario['title'])+'<small>'+escape(scenario.get('tier','gate'))+' · '+escape(scenario.get('family',''))+'</small></th>'+''.join(cells)+'</tr>')
    for scenario in sorted(data['scenarios'],key=lambda s:-s['priced_usd']):
        failures=', '.join(f'{name}: {count}' for name,count in scenario['failed_checks'].items()) or 'None'
        costs.append(f'<tr><th scope="row">{escape(scenario["title"])}</th><td><div class="bar" style="width:{100*scenario["priced_usd"]/maximum:.1f}%"></div>${scenario["priced_usd"]:.3f}</td><td>{scenario["unpriced_calls"]}</td><td>{escape(failures)}</td></tr>')
    sources=''.join(f'<li>{escape(rate["model"])}: {escape(rate["basis"])}. Source: {escape(rate["source"])} (observed {escape(rate["observed_at"])}).</li>' for rate in data.get('operator_rates',[]))
    failures=''.join(f'<li>{escape(t["model"])} / {escape(t["case"])} / repetition {t["repeat"]+1}: {escape(t["measurement"].get("reason", ", ".join(t["measurement"].get("failed_checks",[]))))}</li>' for t in data['trials'] if t['measurement']['outcome']!='passed')
    return '''<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Benchmark pilot diagnostics</title><style>
body{font:16px/1.6 system-ui;background:#fbfaf7;color:#25342d;max-width:1200px;margin:40px auto;padding:0 24px}h1{font:40px Georgia}h2{margin-top:40px;font-size:24px}table{width:100%;border-collapse:collapse}td,th{padding:12px;text-align:left;border-bottom:1px solid #d8dfd8}td small{display:block;font-size:12px}.costs td:nth-child(2){min-width:160px}.bar{height:9px;background:#426e59;border-radius:3px}.scroll{overflow:auto}a{color:#315c7f}.stats{display:flex;gap:32px;flex-wrap:wrap}.stats strong{font-size:28px;display:block}details{margin-top:24px}summary{cursor:pointer}li{overflow-wrap:anywhere}</style>
<h1>Benchmark pilot diagnostics</h1>'''+f'''
<div class="stats"><div><strong>{data['retained']} / {data['planned']}</strong>Retained trials</div><div><strong>${data['spending']['priced_usd']:.3f}</strong>Estimated spending</div><div><strong>{data['spending']['unpriced_calls']}</strong>Unpriced calls</div></div>
<p>Exploration validates the challenges and their checks. No release score is assigned. Costs are private diagnostics and do not affect model outcomes.</p>
<h2>Outcomes by model</h2><p>Each cell shows passed / measured trials. Pending and unmeasured work earn no pass or fail.</p>
<div class="scroll"><table><thead><tr><th scope="col">Scenario</th>{''.join('<th scope="col">'+escape(m)+'</th>' for m in models)}</tr></thead><tbody>{''.join(matrix)}</tbody></table></div>
<h2>Where the spending goes</h2><p>Scenario totals include retained trials and their infrastructure attempts. The total above also includes work still running. Missing prices make amounts lower bounds; estimates are not invoices.</p>
<div class="scroll"><table class="costs"><thead><tr><th scope="col">Scenario</th><th scope="col">Estimated spending</th><th scope="col">Unpriced calls</th><th scope="col">Failed checks</th></tr></thead><tbody>{''.join(costs)}</tbody></table></div>
<details><summary>Pricing sources and assumptions</summary><p>{data['spending'].get('repriced_calls',0)} calls use explicit operator rates; other estimates retain the application's recorded prices. Original ledgers remain unchanged.</p><ul>{sources}</ul></details>
<details><summary>Failed and unmeasured trials</summary><ul>{failures or '<li>None in the retained measurements.</li>'}</ul></details>
<p><a href="analysis.json">Download the trial data</a></p></html>'''
