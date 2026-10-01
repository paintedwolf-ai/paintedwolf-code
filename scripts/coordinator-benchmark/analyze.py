"""Analyze partial measurements and spending without computing release scores."""
from decimal import Decimal
import argparse
import contextlib
import json
import pathlib
import sqlite3
from collections import defaultdict
import report
import structure
import analysis_rates
import preflight_cost
from analysis_visual import render as visual
from progress import save
from snapshot import file_hash
from closed_store import closed_ledger
from timestamps import instant




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
            quoted=analysis_rates.estimate(row,rate) if rate else None
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
    probes=preflight_cost.spending(run,rates)
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
            'provider_preflight_spending':preflight_cost.spending(run,rates),
            'diagnostics_sha256':file_hash(pathlib.Path(__file__)),'grading_sha256':report.GRADING_SHA,
            'scenarios':summaries,'trials':trials}



if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run',type=pathlib.Path,required=True)
    parser.add_argument('--rates',type=pathlib.Path,help='explicit sourced rates for private estimates; never changes the application ledger')
    args=parser.parse_args();run=args.run.resolve()
    result=analyze(run,analysis_rates.load(args.rates) if args.rates else None);save(run/'analysis.json',result)
    (run/'analysis.html').write_text(visual(result))
    print(f'Offline diagnostics: {run / "analysis.html"}')
