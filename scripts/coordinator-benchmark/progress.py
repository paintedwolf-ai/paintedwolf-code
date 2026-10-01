"""Durable attempt receipts and operational progress, separate from public scores."""
from collections import Counter
from datetime import datetime, timezone
import json
import os
import pathlib
import tempfile
import time
from provider_backoff import observations


def timestamp():
    return datetime.now(timezone.utc).isoformat()


def save(path, value):
    fd, name = tempfile.mkstemp(prefix='.' + path.name, dir=path.parent)
    temporary = pathlib.Path(name)
    try:
        with os.fdopen(fd, 'w') as stream:
            json.dump(value, stream, indent=2, allow_nan=False)
            stream.write('\n')
            stream.flush()
            os.fsync(stream.fileno())
        temporary.replace(path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        temporary.unlink(missing_ok=True)


def execution_state(root, index):
    attempts = sorted((root / 'captures' / f'episode-{index:03}').glob('attempt-*'))
    if not attempts:
        return {'phase': 'preparing'}
    attempt = attempts[-1]
    if (attempt / 'exit.json').exists():
        return {'phase': 'execution_finished', 'exit': json.loads((attempt / 'exit.json').read_text())}
    if (attempt / 'execution.json').exists():
        return json.loads((attempt / 'execution.json').read_text())
    return {'phase': 'admitting'}


class Progress:
    def __init__(self, root, plan):
        self.root, self.plan = root, plan
        self.receipts = root / 'attempts'
        self.receipts.mkdir(exist_ok=True)
        self.records = {}
        self.blocked = {}
        self.active = {}
        self.last_update = 0
        for item in plan['episodes']:
            key = str(item['index'])
            receipt = self.receipts / (key + '.json')
            if receipt.exists():
                saved = json.loads(receipt.read_text())
                if saved['item'] != item:
                    raise ValueError('attempt receipt does not match its planned slot')
                if saved['state'] == 'finished':
                    self.records[key] = saved['result']
                elif saved['state'] == 'blocked':
                    self.blocked[key] = saved['result']
            # Unfinished slots reattach to their leased episode workers.
        save(root / 'results.json', self.records)
        save(root / 'blocked.json', self.blocked)

    def pending(self):
        return [item for item in self.plan['episodes']
                if str(item['index']) not in self.records and str(item['index']) not in self.blocked]

    def started(self, item):
        path = self.receipts / (str(item['index']) + '.json')
        receipt = json.loads(path.read_text()) if path.exists() else {'item': item, 'started_at': timestamp()}
        receipt['state'] = 'started'
        save(self.receipts / (str(item['index']) + '.json'), receipt)
        self.active[str(item['index'])] = receipt
        self.write(force=True)

    def finished(self, item, result):
        key = str(item['index'])
        receipt = self.active.pop(key)
        blocked = bool(result and result.get('blocked'))
        retry = bool(result and 'retry_at' in result)
        terminal = bool(result and 'error' not in result and not blocked and not retry)
        state = 'blocked' if blocked else 'finished' if terminal else 'waiting_for_retry' if retry else 'paused'
        save(self.receipts / (key + '.json'), {**receipt, 'state': state, 'finished_at': timestamp(), 'result': result})
        if terminal:
            self.records[key] = result
        if blocked:
            self.blocked[key] = result
        save(self.root / 'results.json', self.records)
        save(self.root / 'blocked.json', self.blocked)
        self.write(force=True)

    def write(self, phase='running', force=False):
        now = time.monotonic()
        if not force and now - self.last_update < 5:
            return
        self.last_update = now
        pending = Counter(item['model'] for item in self.pending() if str(item['index']) not in self.active)
        completed = Counter(item['model'] for item in self.plan['episodes'] if str(item['index']) in self.records)
        blocked = Counter(item['model'] for item in self.plan['episodes'] if str(item['index']) in self.blocked)
        save(self.root / 'status.json', {'phase': phase, 'updated_at': timestamp(), 'runner_pid': os.getpid(),
             'planned': len(self.plan['episodes']), 'completed': len(self.records),
             'completed_by_model': dict(completed), 'pending_by_model': dict(pending),
             'blocked': len(self.blocked), 'blocked_by_model': dict(blocked),
             'active': [{**receipt, 'runtime': execution_state(self.root, receipt['item']['index'])}
                        for receipt in self.active.values()], 'execution': self.plan['execution'],
             'provider_backoff': observations(self.root)})
