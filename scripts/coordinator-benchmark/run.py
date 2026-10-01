"""Run versioned coordinator operations unattended."""
from grading_identity import implementation_files
import argparse
import fcntl
from datetime import datetime, timezone
import json
import os
import pathlib
import random
import shutil
import signal
import subprocess
import sys
import threading
import tempfile
import time
import traceback
import uuid
import yaml
import completion
from capture import MODEL_CONTROLS, describe
from built_runtime import verify_application
from episode import episode, reattachment_order
from episode_runtime import assert_execution_quiescence
from progress import Progress, save
from provider_preflight import Admission as ProviderAdmission
from provider_policy import configure_availability
from preparation import inputs, validate_bank
from prepare import prepare_target, verify_prepared
from target_selection import add_arguments, select
from operation_selection import selected_operations
from recovery import GradingExhausted, grading, MAX_INFRASTRUCTURE_ATTEMPTS
from scheduler import dispatch, execution_policy, positive
from release_policy import decision as release_decision
from grade import IMAGE
from snapshot import ROOT, identity, file_hash

IMPLEMENTATION = identity(implementation_files(pathlib.Path(__file__).parent))

CREDENTIALS = ['providers.local.yaml', 'model-policy.yaml', 'credential-vault.age', '.credential-vault-development-identity']


def validate_repetitions(value):
    if type(value) is not int or value < 1:
        raise ValueError('repetitions must be a positive integer')


def episode_plan(models, operation_ids, repetitions, seed):
    validate_repetitions(repetitions)
    rng, episodes = random.Random(seed), []
    for repeat in range(repetitions):
        block = [{'model': model['id'], 'case': case, 'repeat': repeat}
                 for model in models for case in operation_ids]
        rng.shuffle(block)
        episodes.extend(block)
    for index, item in enumerate(episodes):
        item['index'] = index
    return episodes


def configure(source, destination, roster):
    if 'requested_settings' in roster:
        raise ValueError('benchmark controls come from the application, not the roster')
    for name in CREDENTIALS:
        path = source / name
        if path.exists():
            shutil.copyfile(path, destination / name)
            (destination / name).chmod(0o600)
    providers = yaml.safe_load((destination / 'providers.local.yaml').read_text())
    providers.pop('model_thinking', None)
    by_id = {p['id']: p for p in providers['providers']}
    policy = yaml.safe_load((destination / 'model-policy.yaml').read_text())
    refs = [*roster['models'], roster['worker']]
    if lite := policy.get('lite'):
        refs.append({'provider': lite['provider_id'], 'model': lite['model']})
    manifest = json.loads(pathlib.Path(__file__).with_name('benchmark.json').read_text())
    configure_availability(providers['providers'], {ref['provider'] for ref in refs}, ROOT,
                           manifest['infrastructure']['provider_availability'])
    for ref in refs:
        provider = by_id[ref['provider']]
        models = provider.setdefault('models', [])
        model = next((m for m in models if m['id'] == ref['model']), None)
        if model is None:
            model = {'id': ref['model']}
            models.append(model)
        for key in MODEL_CONTROLS:
            model.pop(key, None)
    (destination / 'providers.local.yaml').write_text(yaml.safe_dump(providers, sort_keys=False))
    return policy


def model_configurations(source, private, roster, application):
    policy = configure(private, private / 'resolved', roster)
    policy['agent_pool'] = {'selection': 'first', 'models': [{'provider_id': roster['worker']['provider'], 'model': roster['worker']['model']}]}
    models = []
    for model in roster['models']:
        policy['coordinator'] = {'provider_id': model['provider'], 'model': model['model']}
        (private / 'resolved/model-policy.yaml').write_text(yaml.safe_dump(policy, sort_keys=False))
        config = describe(private / 'resolved', source / '.bin/lycaon-dev', source / 'lycaon/config', roster['episode_timeout'], application)
        models.append({**model, 'configuration': config})
    return models


def prepare(args, private):
    selection = json.loads((args.out / 'selection.json').read_text())
    preparation = inputs(args, IMPLEMENTATION)
    manifest = json.loads(pathlib.Path(__file__).with_name('benchmark.json').read_text())
    operation_ids = selected_operations(manifest, preparation['mode'], preparation['tier'], preparation['cases'])
    prepared = prepare_target(args.out, selection, IMPLEMENTATION, operation_ids)
    roster = preparation['roster']
    validate_repetitions(roster['repetitions'])
    if not roster['models']:
        raise ValueError('roster requires models')
    if len({m['id'] for m in roster['models']}) != len(roster['models']):
        raise ValueError('duplicate model id')
    if roster['episode_timeout'] != '0s':
        raise ValueError('coordinator benchmark tasks must have no wall-clock deadline')
    source = args.out / 'source'
    application = prepared['application']
    cadence = release_decision(application['version'], preparation['cadence'])
    if not cadence['eligible']:
        raise ValueError('frozen application is no longer eligible for a benchmark; no model calls were made')
    frozen_implementation = source / 'scripts/coordinator-benchmark'
    if identity(implementation_files(frozen_implementation)) != IMPLEMENTATION:
        raise ValueError('runner source changed during preparation; restart before spending')
    manifest = json.loads((source / 'scripts/coordinator-benchmark/benchmark.json').read_text())
    validate_bank(manifest, json.loads((source / manifest['suite']).read_text()))
    preflight = prepared['preflight']
    verify_application(source)
    models = model_configurations(source, private, roster, application)
    providers = yaml.safe_load((private / 'resolved/providers.local.yaml').read_text())['providers']
    policy = execution_policy(models, providers, preparation['concurrency'], preparation['cloud_concurrency'],
                              provider_limits(preparation['provider_limit']))
    execution = {'task_allowance': json.loads((source / manifest['suite']).read_text())['task_allowance'], 'provider_preflight': 'conversation-shapes-v2', 'recovery': {'unclassified_failure_limit': MAX_INFRASTRUCTURE_ATTEMPTS, 'availability': 'until_cancelled'}, 'rate_control': {'implementation': 'application-provider-rate-v1',
                                  'state_directory': 'provider-rate-state'}, 'policy': policy, 'driver_sha256': file_hash(source / '.bin/lycaon-debug'),
                 'launcher_sha256': file_hash(source / 'scripts/eval-agent-live.sh')}
    implementation = source / 'scripts/coordinator-benchmark'
    benchmark = {'id': manifest['id'], 'revision': manifest['revision'],
                 'application_version': application['version'], 'evaluation': prepared['evaluation'],
                 'manifest_sha256': file_hash(implementation / 'benchmark.json'), 'suite_sha256': file_hash(source / manifest['suite']),
                 'implementation_sha256': identity(implementation_files(implementation)),
                 'grading_image': IMAGE, 'execution': execution}
    verify_application(source, models)
    episodes = episode_plan(models, operation_ids, roster['repetitions'], roster['seed'])
    common = {k: v for k, v in models[0]['configuration'].items() if k not in {'coordinator', 'configuration_sha256'}}
    for model in models[1:]:
        if {k: v for k, v in model['configuration'].items() if k not in {'coordinator', 'configuration_sha256'}} != common:
            raise ValueError('all candidates must share application and supporting model configuration')
    plan = {'mode': preparation['mode'], 'run_id': str(uuid.uuid4()), 'started_at': datetime.now(timezone.utc).isoformat(), 'roster': roster,
            'repetitions': roster['repetitions'], 'seed': roster['seed'], 'benchmark': benchmark,
            'comparison_id': identity({'benchmark': benchmark, 'application_configuration': common}),
            'models': models, 'episodes': episodes, 'execution': execution, 'release_decision': cadence,
            'lineage': prepared['lineage'], 'preflight':preflight}
    save(args.out / 'plan.json', plan)
    save(args.out / 'results.json', {})
    return plan


def provider_limits(values):
    result = {}
    for value in values or []:
        name, separator, limit = value.partition('=')
        if not separator or not name or name in result:
            raise ValueError('provider limits require unique PROVIDER=COUNT entries')
        result[name] = positive(int(limit))
    return result


def execute_attempt(root, plan, private, item, cancelled):
    try:
        return episode(root, plan, private, item, cancelled)
    except Exception:
        (root / f"runner-error-{item['index']:03}.txt").write_text(traceback.format_exc())
        raise


def verify_execution(source, plan, args):
    verify_application(source, plan['models'], plan=plan)
    receipt=args.out/plan['preflight']['receipt']
    if not receipt.resolve().is_relative_to(args.out.resolve()) or file_hash(receipt)!=plan['preflight']['sha256']:
        raise ValueError('application preflight receipt changed')
    if 'execution' not in plan:
        raise ValueError('resume this execution with its frozen runner')
    execution = plan['execution']
    frozen = source / 'scripts/coordinator-benchmark'
    if identity(implementation_files(frozen)) != IMPLEMENTATION:
        raise ValueError('runner differs from the frozen execution; use its frozen runner')
    if plan['benchmark']['implementation_sha256'] != IMPLEMENTATION or plan['benchmark']['execution'] != execution:
        raise ValueError('execution plan identity changed')
    for key, path in [('driver_sha256', '.bin/lycaon-debug'), ('launcher_sha256', 'scripts/eval-agent-live.sh')]:
        if file_hash(source / path) != execution[key]:
            raise ValueError('frozen execution runtime changed')
    if args.concurrency is not None or args.cloud_concurrency is not None or args.provider_limit:
        raise ValueError('resume uses its frozen execution limits; omit concurrency overrides')


def enter_frozen_runtime(root, source_config):
    entry = root / 'source/scripts/coordinator-benchmark/run.py'
    frozen = pathlib.Path(__file__).resolve() == entry.resolve()
    save(root / 'controller.json', {'pid': os.getpid(), 'runtime': str(entry),
         'phase': 'running' if frozen else 'handoff', 'updated_at': datetime.now(timezone.utc).isoformat()})
    if not frozen:
        os.execv(sys.executable, [sys.executable, str(entry.with_name('python_runtime.py')), str(entry), '--allow-live', '--resume',
                 '--out', str(root), '--source-config', str(source_config)])


def run(args):
    if not args.allow_live:
        raise ValueError('paid runs require --allow-live')
    if args.resume:
        if any((args.application_ref, args.application_worktree, args.prepared, args.repair_from,
                args.harness_ref, args.harness_worktree, args.reason)):
            raise ValueError('resume preserves its target and evaluator; omit target selectors')
    else:
        selection = select(args)
        selected = verify_prepared(pathlib.Path(selection['prepared'])) if 'prepared' in selection else None
        if selected:
            entry = pathlib.Path(selection['prepared']) / 'source/scripts/coordinator-benchmark/run.py'
            if pathlib.Path(__file__).resolve() != entry.resolve():
                os.execv(sys.executable, [sys.executable, str(entry.with_name('python_runtime.py')), str(entry), *sys.argv[1:]])
                raise RuntimeError('prepared runner handoff returned')
        version = selected['application']['version'] if selected else selection['version']
        if args.mode == 'release' and selected and selected['application']['target']['kind'] == 'worktree':
            raise ValueError('release scores require an application tag or full commit identity')
        cadence = release_decision(version, args.cadence)
        if not cadence['eligible']:
            print(f"Skipping coordinator benchmark for {cadence['version']}: {cadence['reason']}. "
                  'Use --cadence selected to explicitly benchmark this target.', flush=True)
            return
    for limit in [args.concurrency, args.cloud_concurrency]:
        if limit is not None:
            positive(limit)
    provider_limits(args.provider_limit)
    if args.resume:
        if not (args.out / 'plan.json').exists() and not (args.out / 'selection.json').exists():
            raise ValueError('resume requires a retained preparation or execution plan')
        if args.models or args.cases or args.repetitions is not None or getattr(args, 'mode', None) is not None or getattr(args, 'tier', None) is not None:
            raise ValueError('resume preserves the original sampling selection')
    else:
        args.out.parent.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(dir=args.out.parent, prefix='.benchmark-admission-') as temporary:
            staged = pathlib.Path(temporary) / 'run'
            staged.mkdir(mode=0o700)
            save(staged / 'selection.json', selection)
            if args.out.exists():
                raise FileExistsError(args.out)
            staged.rename(args.out)
    lock = (args.out / 'runner.lock').open('a')
    try:
        fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        lock.close()
        raise ValueError('another runner holds this matrix')
    cancelled = threading.Event()
    progress = None
    handlers = {sig: signal.signal(sig, lambda *_: cancelled.set()) for sig in (signal.SIGINT, signal.SIGTERM)}
    try:
        assert_execution_quiescence(args.out)
        if not (args.out / 'plan.json').exists():
            selection = json.loads((args.out / 'selection.json').read_text())
            options = inputs(args, IMPLEMENTATION)
            manifest = json.loads(pathlib.Path(__file__).with_name('benchmark.json').read_text())
            cases = selected_operations(manifest, options['mode'], options['tier'], options['cases'])
            prepare_target(args.out, selection, IMPLEMENTATION, cases)
        private = args.out / 'credentials'
        private.mkdir(exist_ok=True, mode=0o700)
        for name in CREDENTIALS:
            if (args.source_config / name).exists():
                shutil.copyfile(args.source_config / name, private / name)
                (private / name).chmod(0o600)
        (private / 'resolved').mkdir(exist_ok=True, mode=0o700)
        if args.resume and (args.out / 'plan.json').exists():
            plan = json.loads((args.out / 'plan.json').read_text())
            verify_execution(args.out / 'source', plan, args)
            application = json.loads((args.out / 'source/application.json').read_text())
            progress = Progress(args.out, plan)
            if progress.pending() and model_configurations(args.out / 'source', private, plan['roster'], application) != plan['models']:
                raise ValueError('resume configuration differs from the frozen plan')
        else:
            plan = prepare(args, private)
        if cancelled.is_set():
            raise SystemExit(130)
        enter_frozen_runtime(args.out, args.source_config)
        (args.out / 'captures').mkdir(exist_ok=True)
        progress = Progress(args.out, plan)
        if progress.pending():
            subprocess.run(['docker', 'image', 'inspect', IMAGE], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
        admission = ProviderAdmission(args.out, private, plan, cancelled) if progress.pending() else None
        completed = dispatch(reattachment_order(args.out, progress.pending()), plan['execution']['policy'],
                 lambda item: admission.check(item['model']) or execute_attempt(args.out, plan, private, item, cancelled), cancelled,
                 progress.started, progress.finished, progress.write)
        if cancelled.is_set() or not completed:
            progress.write('paused', force=True)
            raise SystemExit(130)
        if progress.blocked:
            progress.write('grading', force=True)
            command = [sys.executable, str(args.out / 'source/scripts/coordinator-benchmark/analyze.py'), '--run', str(args.out)]
            with (args.out / 'completion-analysis.log').open('a') as log:
                subprocess.run(command, stdout=log, stderr=subprocess.STDOUT, check=False)
        command = [sys.executable, str(args.out / 'source/scripts/coordinator-benchmark/report.py'), '--run', str(args.out)]
        try:
            try:
                if not grading(command, args.out, progress, cancelled):
                    raise SystemExit(130)
            except GradingExhausted:
                pass
            state = json.loads((args.out / 'grading-status.json').read_text())
            progress.write(state['phase'], force=True)
        finally:
            shutil.rmtree(private)
    except Exception:
        if progress is not None:
            progress.write('failed', force=True)
        raise
    finally:
        try:
            if progress is not None:
                completion.write(args.out, save)
        finally:
            for sig, handler in handlers.items():
                signal.signal(sig, handler)
            lock.close()


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--allow-live', action='store_true')
    parser.add_argument('--cadence', choices=['selected', 'major-minor'], default='selected')
    add_arguments(parser)
    parser.add_argument('--source-config', type=pathlib.Path, required=True)
    parser.add_argument('--roster', type=pathlib.Path, default=ROOT / 'lycaon/test/fixtures/eval/coordinator-roster.json')
    parser.add_argument('--out', type=pathlib.Path, required=True)
    parser.add_argument('--resume', action='store_true')
    parser.add_argument('--models', help='comma-separated roster ids for a diagnostic run')
    parser.add_argument('--cases', help='comma-separated exact fixture IDs for a selected run')
    parser.add_argument('--repetitions', type=int)
    parser.add_argument('--mode', choices=['exploration','release','calibration'], help='sampling plan (default: exploration)')
    parser.add_argument('--tier', choices=[*json.loads(pathlib.Path(__file__).with_name('benchmark.json').read_text())['tiers'], 'all'], help='scored tiers to run (default: all)')
    parser.add_argument('--concurrency', type=int, help='maximum active episodes (default: 5)')
    parser.add_argument('--cloud-concurrency', type=int, help='coordinators per cloud endpoint (default: 2)')
    parser.add_argument('--provider-limit', action='append', help='PROVIDER=COUNT active episodes using this provider in any role')
    args = parser.parse_args()
    args.out = args.out.resolve()
    run(args)
