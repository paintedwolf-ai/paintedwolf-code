"""Expose every failed Go leg retained by a compound task invocation."""
from datetime import datetime
import hashlib
import json


def events(path):
    with path.open(errors='replace') as stream:
        for line in stream:
            try:
                event = json.loads(line)
            except ValueError:
                continue
            if isinstance(event, dict):
                yield event


def package(event):
    value = event.get('Package') or event.get('ImportPath')
    return value.split(' [', 1)[0] if isinstance(value, str) else ''


def failures(root, record, stages):
    parents = {stage['stage'] for stage in stages
               if stage['subject'] == 'task' and stage['status'] == 'failed'}
    if not parents:
        return []
    found = []
    for path in sorted((root / 'last-run' / 'failures').glob('*/manifest.json')):
        try:
            manifest = json.loads(path.read_text())
            if not isinstance(manifest, dict) or not isinstance(manifest.get('name'), str):
                continue
            name = manifest['name']
            created = datetime.fromisoformat(manifest['created_at']).timestamp()
            if (manifest.get('version') != 1 or manifest['source_commit'] != record['source_commit']
                    or not record['started_at'] <= created <= record['finished_at']
                    or not any(name.startswith(parent + '-') for parent in parents)):
                continue
        except (KeyError, TypeError, ValueError):
            continue
        raw = path.with_name('go-test.json')
        if not raw.is_file():
            continue
        failed = {}
        for event in events(raw):
            subject = package(event)
            if subject and event.get('Action') in {'fail', 'build-fail'}:
                failed.setdefault(subject, set())
                if isinstance(event.get('Test'), str):
                    failed[subject].add(event['Test'])
        outputs = {subject: [] for subject in failed}
        for event in events(raw):
            subject = package(event)
            output = event.get('Output')
            if (subject in failed and isinstance(output, str)
                    and (not event.get('Test') or event['Test'] in failed[subject])):
                outputs[subject].append(output)
        directory = root / 'ci' / 'retained-go' / hashlib.sha256(str(path).encode()).hexdigest()[:16]
        for subject, tests in failed.items():
            directory.mkdir(parents=True, exist_ok=True)
            output = directory / (hashlib.sha256(subject.encode()).hexdigest()[:16] + '.log')
            output.write_text(''.join(outputs[subject]))
            found.append({'stage': name, 'subject': subject, 'status': 'failed', 'exit_code': None,
                          'tests': sorted(tests), 'output': str(output), 'log': str(raw),
                          'manifest': str(path)})
    return found
