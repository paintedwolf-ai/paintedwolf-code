#!/usr/bin/env python3
"""One-shot runs for the numbered source experiment: fresh owned sidecar per run, both arms
interleaved over the same fixture and task order. A run that already has run.json is never
rerun. Never prints or stores a key."""
import argparse, importlib.util, json, os, pathlib, shutil, socket, subprocess, sys, tempfile, time, uuid

R = pathlib.Path.home()/'.local/share/paintedwolf-markdown-citations'
HERE = pathlib.Path(__file__).parent
W = R/'worktrees/numbered'
sys.path.insert(0, str(HERE))
import fixture as FX

os.environ['MC14_WORKTREE'] = str(W)
os.environ['MC14_CWD'] = str(W/'lycaon')
BUILD = subprocess.run(['python3', 'scripts/artifact_paths.py', 'build'], cwd=W, capture_output=True, text=True, check=True).stdout.strip()
os.environ['MC14_BIN'] = str(pathlib.Path(os.environ.get('NUMBERED_BIN') or (pathlib.Path(BUILD)/'lycaon-dev')))
spec = importlib.util.spec_from_file_location('live', R/'tools/mc14/live.py')
live = importlib.util.module_from_spec(spec); spec.loader.exec_module(live)

MODELS = {
    'glm': dict(provider='fireworks-1', kind='fireworks', model='accounts/fireworks/models/glm-5p3-flash', timeout=420),
    # Local runs stop once the file is exactly right: closing turns take most of the wall time and cannot affect edit measures.
    'e4b': dict(provider='ollama-1', kind='ollama', model='gemma4:e4b-paintedwolf', timeout=900, stop_on_correct=True),
    'e2b': dict(provider='ollama-1', kind='ollama', model='gemma4:e2b', timeout=900, stop_on_correct=True),
}

def make_repo(root):
    root = pathlib.Path(root)
    for rel, body in FX.files().items():
        p = root/rel; p.parent.mkdir(parents=True, exist_ok=True); p.write_text(body)
    ov = root/'.paintedwolf'; ov.mkdir(exist_ok=True)
    (ov/'approvals.yaml').write_text('rules:\n  - category: tool\n    pattern: write\n    effect: allow\n')
    for cmd in (['git', 'init', '-q'], ['git', 'add', '-A'], ['git', '-c', 'user.name=t', '-c', 'user.email=t@example.test', 'commit', '-qm', 'init']):
        subprocess.run(cmd, cwd=root, check=True)
    return root

def setup(e, m, secret):
    body = {'id': m['provider'], 'kind': m['kind']}
    e.req('POST', '/v1/providers', body)
    if secret:
        e.req('PUT', f"/v1/providers/{m['provider']}/credential", {'api_key': secret})
    p = e.req('GET', '/v1/settings/model-policy')
    ref = {'provider_id': m['provider'], 'model': m['model']}
    p['coordinator'] = ref
    p['agent_pool'] = {**p.get('agent_pool', {}), 'selection': 'first', 'models': [ref]}
    e.req('PATCH', '/v1/settings/model-policy', p)

one_retry = 'void'

def provider_down(dest):
    """The provider was unreachable at some call, so the run measured nothing about the model."""
    return 'unreachable after' in (dest/'llm-requests.jsonl').read_text(errors='replace')

def wait_provider(m):
    if m['kind'] != 'fireworks':
        return
    while True:
        try:
            socket.gethostbyname('api.fireworks.ai'); return
        except OSError:
            print('waiting for provider DNS', flush=True); time.sleep(60)

def check_arm(dest, arm):
    """Refuse to record a run whose requests do not carry its arm's clause."""
    p = dest/'llm-requests.jsonl'
    for line in open(p):
        for t in json.loads(line).get('tools') or []:
            body = json.dumps(t)
            if '"edit"' in body and 'old_string' in body:
                if ('not part of the file' in body) != (arm == 'numbered'):
                    raise SystemExit(f'arm check failed for {dest}')

def one(model, arm, task, i, out, config_roots, secret):
    m = MODELS[model]; t = FX.TASKS[task]
    dest = out/model/arm/f'{task}-{i}'
    if (dest/'run.json').exists():
        return
    scratch = pathlib.Path(tempfile.mkdtemp(prefix='numx-', dir=os.environ['MC14_SCRATCH']))
    env = {'PW_EXP_NUMBERED_SOURCE': '1' if arm == 'numbered' else '0'}
    e = live.Engine(scratch, config_root=config_roots[arm], env=env)
    try:
        try:
            setup(e, m, secret)
        except RuntimeError as ex:
            if 'provider_catalog_unavailable' in str(ex):
                print('provider catalog unavailable; slot not used', flush=True); time.sleep(30)
                return one_retry
            raise
        repo = make_repo(scratch/'repo')
        proj = e.req('POST', '/v1/projects', {'roots': [{'path': str(repo)}], 'name': f'numx-{task}-{i}'})
        sess = e.req('POST', '/v1/sessions', {'project_id': proj['id'], 'posture': 'build'})
        sid = sess['id']
        for _ in range(120):
            s = e.req('GET', f'/v1/sessions/{sid}')
            if s.get('workspace_status', s.get('status')) != 'preparing':
                break
            time.sleep(1)
        t0 = time.time()
        e.req('POST', f'/v1/sessions/{sid}/prompts', {'operation_id': str(uuid.uuid4()), 'text': t['prompt']})
        time.sleep(10)
        early = False
        if m.get('stop_on_correct') and t['kind'] == 'edit':
            want = t['expected'](FX.files()[t['path']]); deadline = time.time()+m['timeout']; calm = 0; end = None
            while time.time() < deadline:
                if (repo/t['path']).read_text() == want:
                    time.sleep(5); early = True; end = e.req('GET', f'/v1/sessions/{sid}'); break
                s = e.req('GET', f'/v1/sessions/{sid}')
                calm = calm+1 if s.get('status') in ('idle', 'error') else 0
                if calm >= 4:
                    end = s; break
                time.sleep(5)
        else:
            end = live.wait_idle(e, sid, time.time()+m['timeout'], calm_needed=4)
        secs = int(time.time()-t0)
        live.save(e, sid, dest, secret or '\0never\0')
        final = dest/'final'; final.mkdir(exist_ok=True)
        for rel in FX.files():
            src = repo/rel
            if src.exists():
                (final/rel).parent.mkdir(parents=True, exist_ok=True); shutil.copy(src, final/rel)
        # every untracked or changed path, so stray written files are seen
        st = subprocess.run(['git', 'status', '--porcelain'], cwd=repo, capture_output=True, text=True).stdout
        log = subprocess.run(['git', 'log', '--format=%B'], cwd=repo, capture_output=True, text=True).stdout
        diff = subprocess.run(['git', 'diff'], cwd=repo, capture_output=True, text=True).stdout
        (dest/'git-status.txt').write_text(st); (dest/'git-log.txt').write_text(log); (dest/'git.diff').write_text(diff)
        check_arm(dest, arm)
        if provider_down(dest):
            void = out.parent/'void'/model/arm; void.mkdir(parents=True, exist_ok=True)
            shutil.move(str(dest), str(void/f'{task}-{i}-{int(time.time())}'))
            print(f'{model}/{arm}/{task}-{i}: void, provider unreachable', flush=True)
            return one_retry
        (dest/'run.json').write_text(json.dumps({'model': m['model'], 'arm': arm, 'task': task, 'run': i, 'settled': end is not None, 'stopped_on_correct': early, 'seconds': secs, 'session_id': sid,
            'head': subprocess.run(['git', 'rev-parse', 'HEAD'], cwd=W, capture_output=True, text=True).stdout.strip(), 'prompt': t['prompt']}, indent=1))
        print(f'{model}/{arm}/{task}-{i}: settled={end is not None} seconds={secs}', flush=True)
    finally:
        e.stop()
        print(f'stopped owned pid {e.pid}', flush=True)
        for n in ('credential-vault.age', '.credential-vault-development-identity', 'api.token'):
            (e.cfg/n).unlink(missing_ok=True)
        shutil.rmtree(scratch/'repo', ignore_errors=True)
        for n in ('store.db', 'store.db-wal', 'store.db-shm'):
            (scratch/n).unlink(missing_ok=True)

def main():
    a = argparse.ArgumentParser()
    a.add_argument('model'); a.add_argument('--out', default='artifacts/numbered/runs'); a.add_argument('--start', type=int, default=1); a.add_argument('--end', type=int, default=8)
    a.add_argument('--tasks', default=','.join(FX.TASKS)); a.add_argument('--arms', default='unlabeled,numbered')
    args = a.parse_args()
    roots = {'unlabeled': os.environ['NUMBERED_CFG_UNLABELED'], 'numbered': os.environ['NUMBERED_CFG_NUMBERED']}
    secret = None
    if MODELS[args.model]['kind'] == 'fireworks':
        secret = live.key()
    out = R/args.out
    arms = args.arms.split(',')
    for i in range(args.start, args.end+1):
        for task in args.tasks.split(','):
            order = arms if i % 2 else list(reversed(arms))
            for arm in order:
                while True:
                    wait_provider(MODELS[args.model])
                    if one(args.model, arm, task, i, out, roots, secret) != one_retry:
                        break

if __name__ == '__main__':
    main()
