"""Run the bounded reference-model contracts for cloud provider integrations."""
import argparse
import concurrent.futures
import hashlib
import html
import json
import pathlib
import shutil
import sys
import tempfile
import threading
import time
import urllib.error

from progress import save, timestamp
from snapshot import ROOT, identity
from smoke import Application
sys.path.append(str(ROOT / 'scripts'))
from artifact_paths import build_dir

CLOUD_PROVIDER_KINDS=('openai','anthropic','together','azure','gemini','fireworks',
                      'bedrock','vertex','vertex-express','openrouter','cloudflare-workers-ai')
CONFIG_FILES=('providers.local.yaml','model-policy.yaml','credential-vault.age','.credential-vault-development-identity')


def load_plan(path):
    plan=json.loads(path.read_text())
    if set(plan)!={'schema','repetitions','providers'} or plan['schema']!=1 or type(plan['repetitions']) is not int or plan['repetitions']<1:
        raise ValueError('invalid provider-check plan')
    kinds=set()
    if not isinstance(plan['providers'],list) or not plan['providers']:raise ValueError('reference providers are required')
    for provider in plan['providers']:
        if set(provider)!={'kind','models'} or provider['kind'] not in CLOUD_PROVIDER_KINDS or provider['kind'] in kinds:
            raise ValueError('expected unique cloud provider kinds')
        kinds.add(provider['kind'])
        if not isinstance(provider['models'],list) or not 1<=len(provider['models'])<=2:
            raise ValueError('each provider needs one or two representative models')
        ids=set()
        for model in provider['models']:
            if set(model)!={'id','covers'} or not isinstance(model['id'],str) or not model['id'].strip() or model['id'] in ids:
                raise ValueError('reference models require unique exact IDs')
            ids.add(model['id'])
            if not isinstance(model['covers'],list) or not model['covers'] or any(not isinstance(v,str) or not v.strip() for v in model['covers']):
                raise ValueError('each model must name the integration behavior it covers')
    return plan


def passed_stages(result):
    stages=result.get('stages') or []
    return result.get('ok') is True and [s['stage'] for s in stages]==['tool_call','tool_result_replay'] and all(s['accepted'] is True for s in stages)


def check_model(application, provider, model, out, repetitions, retries, wait=time.sleep):
    directory=out/'observations'/identity({'provider':provider['id'],'model':model})
    directory.mkdir(parents=True,exist_ok=True)
    samples=[]
    for repetition in range(repetitions):
        final=directory/f'sample-{repetition:03}.json'
        if final.exists():
            sample=json.loads(final.read_text())
            if sample['model']!=model or sample['provider']!=provider['id']:
                raise ValueError('provider-check sample identity changed')
            samples.append(sample)
            continue
        for attempt in range(retries+1):
            attempt_file=directory/f'sample-{repetition:03}-attempt-{attempt:03}.json'
            if attempt_file.exists():
                sample=json.loads(attempt_file.read_text())
            else:
                try:
                    result=application.request('/harness/llm/provider-tools', {'allow_live':True,'provider':provider['id'],'model':model})
                    result.pop('provider',None)
                except urllib.error.HTTPError as error:
                    result={'ok':False,'failure':{'kind':'harness','code':'check_http_'+str(error.code),
                            'retryable':error.code in {408,429,500,502,503,504}},'error':'HTTP '+str(error.code)}
                except (OSError, urllib.error.URLError) as error:
                    result={'ok':False,'failure':{'kind':'harness','code':'check_transport_interrupted','retryable':True},
                            'error':type(error).__name__}
                sample={'provider':provider['id'],'model':model,'repetition':repetition,
                        'attempt':attempt,'completed_at':timestamp(),'result':result}
                save(attempt_file,sample)
            if not sample['result'].get('failure',{}).get('retryable') or attempt==retries:
                save(final,sample);samples.append(sample);break
            wait(min(300,5*2**attempt))
    return samples


def render_report(report, out):
    rows=[]
    for row in report['models']:
        passed=sum(passed_stages(sample['result']) for sample in row['samples'])
        count=len(row['samples'])
        failures=sorted({sample['result'].get('failure',{}).get('code','inconclusive')
                         for sample in row['samples'] if not passed_stages(sample['result'])})
        policies=[sample['result'].get('reasoning_policy') for sample in row['samples'] if sample['result'].get('reasoning_policy')]
        reasoning='; '.join(sorted({p['intent']+' / '+p['style']+(' / always on' if p['always_on'] else '') for p in policies})) or 'Not established'
        rows.append('<tr><td>'+html.escape(row['provider'])+'</td><td>'+html.escape(row['model'])+
                    f'</td><td><meter min="0" max="{count}" value="{passed}"></meter> {passed}/{count}</td><td>'+
                    html.escape(', '.join(failures) or 'All checks passed')+'</td><td>'+html.escape(reasoning)+'</td></tr>')
    issues=''.join('<li>'+html.escape(' / '.join(str(f[key]) for key in ['provider','model','error','detail'] if key in f))+'</li>' for f in report.get('failures',[]))
    document='''<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Provider integration checks</title><style>body{font:17px system-ui;margin:3rem auto;max-width:1100px;padding:0 1rem;color:#242923;background:#faf8f1}table{border-collapse:collapse;width:100%}td,th{text-align:left;padding:1rem;border-bottom:1px solid #ddd}meter{width:7rem}p{max-width:75ch;color:#52604f}code{overflow-wrap:anywhere}</style>
<h1>Provider integration checks</h1><p>Observed tool calling and result replay through Painted Wolf Code. These are compatibility observations, not coordinator benchmark scores. Failures do not automatically exclude models.</p>
<p>Status: '''+html.escape(report.get('status','completed'))+'''</p><table><thead><tr><th>Provider</th><th>Model</th><th>Passed samples</th><th>Observed result</th><th>Requested reasoning</th></tr></thead><tbody>'''+''.join(rows)+'''</tbody></table><ul>'''+issues+'''</ul><p>Application binary: <code>'''+html.escape(report['binary_sha256'])+'''</code></p><p>report.json and observations retain request policy, failures and retries. These checks never update application metadata or exclusion rules.</p></html>'''
    (out/'report.html').write_text(document)


def select_references(providers, plan, provider_ids):
    references={p['kind']:p['models'] for p in plan['providers']}
    wanted=set(provider_ids.split(',')) if provider_ids else set()
    selected=[p for p in providers if p['kind'] in references and (not wanted or p['id'] in wanted)]
    if wanted and {p['id'] for p in selected}!=wanted:raise ValueError('selected provider is missing or has no reference plan')
    if not selected:raise ValueError('no configured reference providers selected')
    selection=[]
    for provider in selected:
        if not provider['configured']:raise ValueError('reference provider is not configured: '+provider['id'])
        available={m['id'] for m in provider['models']}
        missing={m['id'] for m in references[provider['kind']]}-available
        if missing:raise ValueError('reference models unavailable from '+provider['id']+': '+', '.join(sorted(missing)))
        selection.append((provider,references[provider['kind']]))
    if not wanted and {p['kind'] for p in selected}!=set(references):
        raise ValueError('a required reference provider is missing; select a provider subset explicitly')
    return selection


def persist_report(report, out):
    report['updated_at']=timestamp()
    report['models'].sort(key=lambda row:(row['provider'],row['model']))
    save(out/'report.json',report)
    render_report(report,out)


def disposition(report):
    results=[sample['result'] for row in report['models'] for sample in row['samples']]
    failed=[result for result in results if not passed_stages(result)]
    if any(result.get('failure',{}).get('kind')=='compatibility' for result in failed):return 'failed'
    if report['failures'] or failed or not results:return 'inconclusive'
    return 'passed'


def run_checks(application, plan, provider_ids, out, binary_sha256):
    report={'schema':1,'binary_sha256':binary_sha256,'plan':plan,'status':'running','models':[],'failures':[]}
    persist_report(report,out)
    try:
        selection=select_references(application.request('/v1/providers')['providers'],plan,provider_ids)
        selected=[{'provider':p['id'],'kind':p['kind'],'base_url':p['base_url'],'models':models} for p,models in selection]
        selection_path=out/'selection.json'
        if selection_path.exists() and json.loads(selection_path.read_text())!=selected:
            raise ValueError('provider-check selection changed; use a fresh output directory')
        save(selection_path,selected)
        report['selection']=selected
        lock=threading.Lock()
        def provider_job(provider, models):
            for model in models:
                try:
                    samples=check_model(application,provider,model['id'],out,plan['repetitions'],3)
                    row={'provider':provider['id'],'kind':provider['kind'],'base_url':provider['base_url'],
                         'model':model['id'],'covers':model['covers'],'samples':samples}
                    with lock:
                        report['models'].append(row)
                        persist_report(report,out)
                except Exception as error:
                    with lock:
                        report['failures'].append({'provider':provider['id'],'model':model['id'],'error':type(error).__name__})
                        persist_report(report,out)
        with concurrent.futures.ThreadPoolExecutor(max_workers=len(selection)) as pool:
            list(pool.map(lambda item:provider_job(*item),selection))
    except Exception as error:
        report['failures'].append({'error':type(error).__name__,'detail':str(error)})
    report['status']=disposition(report)
    persist_report(report,out)
    return report


def source_fingerprint(source):
    return identity({name:hashlib.sha256((source/name).read_bytes()).hexdigest()
                     for name in CONFIG_FILES if (source/name).is_file()})


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--allow-live',action='store_true')
    parser.add_argument('--source-config',type=pathlib.Path,required=True)
    parser.add_argument('--providers',default='',help='Explicit configured provider instance subset')
    parser.add_argument('--plan',type=pathlib.Path,default=pathlib.Path(__file__).with_name('provider-checks.json'))
    parser.add_argument('--out',type=pathlib.Path,required=True)
    args=parser.parse_args()
    if not args.allow_live:parser.error('these checks send paid requests; --allow-live is required')
    plan=load_plan(args.plan)
    args.out=args.out.resolve();args.out.mkdir(parents=True,exist_ok=True)
    args.source_config=args.source_config.resolve()
    binary_sha256=hashlib.sha256((build_dir(ROOT)/'lycaon-dev').read_bytes()).hexdigest()
    binding={'binary_sha256':binary_sha256,'plan':plan,'providers':args.providers,
             'source_config_sha256':source_fingerprint(args.source_config)}
    ledger=args.out/'binding.json'
    if ledger.exists() and json.loads(ledger.read_text())!=binding:parser.error('provider-check configuration changed; use a fresh output directory')
    save(ledger,binding)
    report={'schema':1,'binary_sha256':binary_sha256,'plan':plan,'status':'starting','models':[],'failures':[]}
    persist_report(report,args.out)
    with tempfile.TemporaryDirectory(prefix='paintedwolf-provider-checks-') as scratch:
        scratch=pathlib.Path(scratch)
        application=None
        try:
            application=Application(scratch/'application',args.source_config)
            application.ready()
            report=run_checks(application,plan,args.providers,args.out,binary_sha256)
        except Exception as error:
            report['status']='inconclusive';report['failures'].append({'error':type(error).__name__})
            persist_report(report,args.out)
        finally:
            if application:
                application.close()
                shutil.copyfile(application.directory/'sidecar.log',args.out/'sidecar.log')
    print('Provider integration checks: '+report['status']+'; '+str(args.out/'report.html'))
    raise SystemExit({'passed':0,'failed':1,'inconclusive':2}[report['status']])


if __name__=='__main__':main()
