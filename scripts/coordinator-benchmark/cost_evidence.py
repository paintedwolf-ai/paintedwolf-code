"""Publish captured monetary estimates without repricing or changing verdicts."""
import json
import os
import math
import subprocess
from decimal import Decimal, ROUND_HALF_UP
from snapshot import file_hash
from progress import save

COUNTS = ('calls', 'prompt_tokens', 'completion_tokens', 'cache_read_tokens', 'cache_write_tokens',
          'cache_write_1h_tokens', 'unpriced_tokens', 'unpriced_calls')
IDENTITY = ('provider', 'model', 'caller', 'status', 'no_charge', 'pricing_source', 'priced_as_of', 'usage_source', 'rate')


def summarize(buckets, missing_captures=0, capture_attempts=0):
    grouped = {}
    for bucket in buckets:
        key = json.dumps({k: bucket[k] for k in IDENTITY}, sort_keys=True)
        item = grouped.setdefault(key, {**{k: bucket[k] for k in IDENTITY},
                                       **{k: 0 for k in COUNTS}, 'known_nano_usd': None})
        counts = {name: bucket[name] for name in COUNTS}
        if bucket['known_nano_usd'] is None:
            counts['unpriced_calls'] = bucket['calls']
        for name in COUNTS:
            item[name] += counts[name]
        if bucket['known_nano_usd'] is not None:
            item['known_nano_usd'] = (item['known_nano_usd'] or 0) + bucket['known_nano_usd']
    buckets = [grouped[key] for key in sorted(grouped)]
    uncertain = sum(b['calls'] if b['status'] != 'reported' or b['usage_source'] == 'provider_partial' or b['known_nano_usd'] is None
                    else b['unpriced_calls'] for b in buckets if not b['no_charge'])
    known = [b['known_nano_usd'] for b in buckets if not b['no_charge'] and b['known_nano_usd'] is not None]
    complete = not (missing_captures or uncertain)
    amount = sum(known) if known else (0 if complete else None)
    return {'known_nano_usd': amount, 'complete': complete, 'unknown_calls': uncertain,
            'host_counted_calls': sum(b['calls'] for b in buckets if b['usage_source'] == 'host'),
            'missing_captures': missing_captures, 'capture_attempts': capture_attempts, 'buckets': buckets}


def export(capture, source):
    database = capture / 'store.db'
    before = file_hash(database)
    wal = capture / 'store.db-wal'
    environment = {**os.environ, 'LYCAON_DEV': '1', 'LYCAON_CONFIG_ROOT': str(source / 'lycaon')}
    result = subprocess.run([str(source / '.bin/lycaon-debug'), 'eval', 'cost', '--capture', str(capture)],
                            capture_output=True, text=True, check=True, cwd=source, env=environment)
    facts = json.loads(result.stdout)
    if facts['version'] != 1 or file_hash(database) != before or (wal.exists() and wal.stat().st_size):
        raise ValueError('captured cost changed during extraction')
    return facts['buckets']


def case_cost(run, episodes):
    buckets, missing, attempts = [], 0, 0
    for item in episodes:
        root = run / 'captures' / f"episode-{item['index']:03}"
        retained = sorted(root.glob('attempt-*'))
        if not retained:
            missing += 1
        for attempt in retained:
            attempts += 1
            try:
                buckets.extend(export(attempt / 'capture', run / 'source'))
            except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
                missing += 1
                save(attempt / 'cost-error.json', {'type': type(error).__name__, 'detail': str(error)})
    return summarize(buckets, missing, attempts)


def preflight_cost(run):
    buckets, missing, attempts = [], 0, 0
    for attempt in sorted((run / 'provider-preflight').glob('*/attempt-*')):
        attempts += 1
        path = attempt / 'probe/result.json'
        if not path.exists():
            missing += 1
            continue
        try:
            buckets.extend(probe_buckets(json.loads(path.read_text())))
        except (OSError, ValueError, KeyError, TypeError) as error:
            missing += 1
            save(attempt / 'cost-error.json', {'type': type(error).__name__, 'detail': str(error)})
    return summarize(buckets, missing, attempts)


def probe_buckets(receipt):
    buckets = []
    for stage in receipt['stages']:
        usage = stage.get('usage', {})
        cost = receipt.get('costs', {}).get(stage['stage'], {})
        amount = cost.get('estimated_usd')
        if amount is not None and (type(amount) not in (int, float) or not math.isfinite(amount) or amount < 0):
            raise ValueError('invalid captured preflight cost')
        nano = None if amount is None else int((Decimal(str(amount)) * 10**9).to_integral_value(rounding=ROUND_HALF_UP))
        complete = cost.get('complete') is True and usage.get('present') and not usage.get('incomplete')
        bucket = {k: 0 for k in COUNTS}
        bucket.update(provider=receipt['provider'], model=receipt['model'], caller='provider_preflight',
                      status='reported' if usage.get('present') else 'unknown', no_charge=False,
                      calls=1, unpriced_calls=0 if complete and nano is not None else 1,
                      known_nano_usd=nano, pricing_source=cost.get('source', ''), priced_as_of=cost.get('as_of') or '',
                      usage_source='provider_partial' if usage.get('incomplete') else 'provider', rate=cost.get('rate'))
        for target, source in [('prompt_tokens', 'prompt_tokens'), ('completion_tokens', 'completion_tokens'),
                               ('cache_read_tokens', 'cache_read_input_tokens'), ('cache_write_tokens', 'cache_creation_input_tokens'),
                               ('cache_write_1h_tokens', 'cache_creation_1h_input_tokens')]:
            bucket[target] = usage.get(source, 0)
        buckets.append(bucket)
    return buckets
