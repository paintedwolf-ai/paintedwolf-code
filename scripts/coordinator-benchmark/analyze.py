"""Analyze partial measurements and spending without computing release scores."""
from collections import defaultdict
from decimal import Decimal
import argparse
import contextlib
import html
import json
import math
import pathlib
import sqlite3
import report
import sampling as structure
from progress import save
from snapshot import file_hash
from capture_storage import closed_ledger
from ledger import instant

FIELDS = ['input', 'output', 'cache_read', 'cache_write', 'cache_write_1h']


def load(path):
    document=json.loads(path.read_text())
    if set(document)!={'version','rates'} or document['version']!=1:
        raise ValueError('unsupported operator rate card')
    rates={}
    for entry in document['rates']:
        if set(entry)!={'provider','model','source','observed_at','basis','usd_per_million'}:
            raise ValueError('operator rates require exact identities and source provenance')
        if not all(isinstance(entry[k],str) and entry[k] for k in ['provider','model','source','observed_at','basis']):
            raise ValueError('operator rate provenance is empty')
        values=entry['usd_per_million']
        if not isinstance(values,dict) or set(values)-set(FIELDS):
            raise ValueError('unknown pricing bucket')
        if any(type(v) not in [int,float] or not math.isfinite(v) or v<0 for v in values.values()):
            raise ValueError('rates must be finite, nonnegative USD amounts')
        key=(entry['provider'],entry['model'])
        if key in rates: raise ValueError('duplicate operator rate identity')
        rates[key]=entry
    return rates


def estimate(row, rate):
    prompt=row['prompt_tokens']; read=row['cache_read_tokens']; write=row['cache_write_tokens']; hour=row['cache_write_1h_tokens']
    buckets={'input':prompt-read-write,'output':row['completion_tokens'],'cache_read':read,
             'cache_write':write-hour,'cache_write_1h':hour}
    if any(type(v) is not int or v<0 for v in buckets.values()):
        raise ValueError('invalid normalized token accounting')
    prices=rate['usd_per_million']
    if any(count and key not in prices for key,count in buckets.items()):
        return None
    return float(sum(Decimal(count)*Decimal(str(prices.get(key,0))) for key,count in buckets.items())/Decimal(1000000))


def preflight_spending(run, rates=None):
    total = {'priced_usd':0.0,'unpriced_calls':0,'calls':0,'prompt_tokens':0,'completion_tokens':0,'repriced_calls':0}
    for attempt in (run/'provider-preflight').glob('*/attempt-*'):
        path = attempt/'probe/result.json'
        if not path.exists():
            if (attempt/'launched.json').exists():
                total['unpriced_calls'] += 1
            continue
        receipt = json.loads(path.read_text())
        rate = (rates or {}).get((receipt['provider'],receipt['model']))
        for stage in receipt['stages']:
            usage = stage.get('usage',{})
            row = {'prompt_tokens':usage.get('prompt_tokens',0), 'completion_tokens':usage.get('completion_tokens',0),
                   'cache_read_tokens':usage.get('cache_read_input_tokens',0),
                   'cache_write_tokens':usage.get('cache_creation_input_tokens',0),
                   'cache_write_1h_tokens':usage.get('cache_creation_1h_input_tokens',0)}
            total['calls'] += 1
            total['prompt_tokens'] += row['prompt_tokens']
            total['completion_tokens'] += row['completion_tokens']
            quote = estimate(row,rate) if rate and usage.get('present') and not usage.get('incomplete') else None
            if quote is not None:
                total['priced_usd'] += quote
                total['repriced_calls'] += 1
                continue
            captured = receipt.get('costs', {}).get(stage.get('stage'), {})
            amount = captured.get('estimated_usd')
            if amount is not None:
                if type(amount) not in (int, float) or not math.isfinite(amount) or amount < 0:
                    raise ValueError('invalid application probe cost')
                total['priced_usd'] += amount
            if amount is None or captured.get('complete') is not True or not usage.get('present') or usage.get('incomplete'):
                total['unpriced_calls'] += 1
    return total


def visual(data):
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


def cost_observation(capture, rates=None):
    database=capture/'store.db'
    entry=None
    raw=capture/'report.json'
    if raw.exists():
        cases=json.loads(raw.read_text()).get('cases',[])
        if cases: entry=cases[0].get('preparation',{}).get('entry_at')
        if cases and not entry and cases[0].get('session_id'):
            import uuid
            session=str(uuid.UUID(cases[0]['session_id']))
            receipt=capture/'conversation-preparation'/(session+'.entry.json')
            if receipt.exists(): entry=json.loads(receipt.read_text())['entry_at']
    result={'priced_usd':0.0,'unpriced_calls':0,'calls':0,'prompt_tokens':0,'completion_tokens':0,'repriced_calls':0,'phases':{}}
    if not database.exists(): return result
    connection=(closed_ledger(capture) if (capture.parent/'exit.json').exists() else
                contextlib.closing(sqlite3.connect(database.resolve().as_uri()+'?mode=ro',uri=True)))
    with connection as db:
        db.row_factory=sqlite3.Row
        if not db.execute("SELECT 1 FROM sqlite_schema WHERE type='table' AND name='llm_calls'").fetchone():
            return result
        columns='provider_id,caller,started_at,no_charge,estimated_nano_usd,prompt_tokens,completion_tokens,unpriced_tokens'
        if rates: columns+=',model,cache_read_tokens,cache_write_tokens,cache_write_1h_tokens'
        for row in db.execute('SELECT '+columns+' FROM llm_calls'):
            phase='preparation' if row['provider_id']=='fixture' or (entry and instant(row['started_at']) < instant(entry)) else 'candidate'
            bucket=result['phases'].setdefault(phase,{'calls':0,'priced_usd':0.0,'unpriced_calls':0})
            result['calls']+=1;bucket['calls']+=1
            result['prompt_tokens']+=row['prompt_tokens'];result['completion_tokens']+=row['completion_tokens']
            if row['no_charge'] or row['provider_id']=='fixture': continue
            rate=(rates or {}).get((row['provider_id'],row['model'])) if rates else None
            quoted=estimate(row,rate) if rate else None
            if quoted is not None:
                result['priced_usd']+=quoted;bucket['priced_usd']+=quoted;result['repriced_calls']+=1
                continue
            if row['estimated_nano_usd'] is None or row['unpriced_tokens']:
                result['unpriced_calls']+=1;bucket['unpriced_calls']+=1
            if row['estimated_nano_usd'] is not None:
                amount=row['estimated_nano_usd']/1e9
                result['priced_usd']+=amount;bucket['priced_usd']+=amount
    return result


def spending(run, rates=None):
    totals={'priced_usd':0.0,'unpriced_calls':0,'calls':0,'prompt_tokens':0,'completion_tokens':0,'repriced_calls':0}
    for database in (run/'captures').glob('episode-*/attempt-*/capture/store.db'):
        cost=cost_observation(database.parent,rates)
        for key in totals: totals[key]+=cost[key]
    probes=preflight_spending(run,rates)
    for key in totals: totals[key]+=probes[key]
    return totals


def analyze(run, rates=None):
    source=run/'source'
    plan=json.loads((run/'plan.json').read_text())
    if file_hash(pathlib.Path(report.__file__).with_name('benchmark.json')) != plan['benchmark']['manifest_sha256']:
        raise ValueError('use the frozen analysis command from this execution')
    records=json.loads((run/'results.json').read_text())
    suite={c['id']:c for c in json.loads((source/report.MANIFEST['suite']).read_text())['cases']}
    models={m['id']:m for m in plan['models']}
    trials=[];seen=set()
    for item in plan['episodes']:
        record=records.get(str(item['index']))
        if not record or 'report' not in record: continue
        try:
            result=report.captured_trial(record,item,models[item['model']]['configuration'],suite,seen,source)
        except (ValueError,RuntimeError,OSError,sqlite3.Error) as exc:
            result={'outcome':'unmeasured','reason':str(exc)}
        costs=[cost_observation(path.parent,rates) for path in (run/'captures'/f"episode-{item['index']:03}").glob('attempt-*/capture/store.db')]
        trials.append({**item,'measurement':result,'cost':{
            key:sum(cost[key] for cost in costs) for key in ['priced_usd','unpriced_calls','calls','prompt_tokens','completion_tokens','repriced_calls']}})
    summaries=[]
    for operation in report.MANIFEST['operations']:
        rows=[t for t in trials if t['case']==operation['id']]
        if not rows: continue
        failures=defaultdict(int)
        for row in rows:
            for check in row['measurement'].get('failed_checks',[]): failures[check]+=1
        summaries.append({'id':operation['id'],'title':operation['title'],'tier':operation.get('tier','gate'),'family':operation['family'],'trials':len(rows),
                          'passed':sum(t['measurement']['outcome']=='passed' for t in rows),
                          'unmeasured':sum(t['measurement']['outcome']=='unmeasured' for t in rows),
                          'priced_usd':sum(t['cost']['priced_usd'] for t in rows),
                          'unpriced_calls':sum(t['cost']['unpriced_calls'] for t in rows),'failed_checks':dict(failures),
                          'structure':structure.summarize([t['measurement'].get('structure') for t in rows])})
    return {'kind':'exploratory-diagnostics','execution_id':plan['run_id'],'planned':len(plan['episodes']),
            'retained':len(trials),'score':None,'spending':spending(run,rates),'operator_rates':list((rates or {}).values()),
            'provider_preflight_spending':preflight_spending(run,rates),
            'diagnostics_sha256':file_hash(pathlib.Path(__file__)),'grading_sha256':report.GRADING_SHA,
            'scenarios':summaries,'trials':trials}


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run',type=pathlib.Path,required=True)
    parser.add_argument('--rates',type=pathlib.Path,help='explicit sourced rates for private estimates; never changes the application ledger')
    args=parser.parse_args();run=args.run.resolve()
    result=analyze(run,load(args.rates) if args.rates else None);save(run/'analysis.json',result)
    (run/'analysis.html').write_text(visual(result))
    print(f'Offline diagnostics: {run / "analysis.html"}')
