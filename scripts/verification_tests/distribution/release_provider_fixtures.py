"""Disposable filesystem providers for the unchanged feed controller and real minisign boundary."""
import base64
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
from urllib.parse import urlsplit


class Providers:
    def __init__(self, root, source, registry, key):
        self.root, self.source = root, source
        self.store, self.controller, self.bin = root / 'objects', root / 'controller', root / 'commands'
        for directory in (self.store, self.bin, self.controller / 'scripts', self.controller / 'packaging',
                          self.controller / 'lycaon-den', root / 'home'):
            directory.mkdir(parents=True, exist_ok=True)
        names = ('release-halt.sh', 'release-r2-publish-pointer.sh', 'release-validate-updater-manifest.sh',
                 'release_distribution.py', 'release-r2-immutable-put.sh', 'release-r2-rest.sh',
                 'feed_signing.py', 'feed_signature.py', 'update_keys.py', 'updater_signature.py',
                 'release_semver.py', 'semver-compare.py', 'release_pointer_headers.py')
        for name in names:
            shutil.copyfile(source / 'scripts' / name, self.controller / 'scripts' / name)
        for name in ('release_distribution_contract', 'release_storage'):
            shutil.copytree(source / 'scripts' / name, self.controller / 'scripts' / name,
                            ignore=shutil.ignore_patterns('__pycache__'))
        for name in ('release-platforms.json', 'distribution-legacy-platforms.json', 'distribution-legacy-releases.json'):
            shutil.copyfile(source / 'packaging' / name, self.controller / 'packaging' / name)
        (self.controller / 'packaging/update-keys.json').write_text(json.dumps(registry))
        self.execute = subprocess.run
        for name in ('aws', 'curl', 'bun'):
            path = self.bin / name
            path.write_text('#!' + sys.executable + '\nimport runpy,sys\nsys.argv.insert(1,' + repr(name) + ')\n'
                            + 'runpy.run_path(' + repr(str(Path(__file__).resolve())) + ',run_name="__main__")\n')
            path.chmod(0o700)
        self.environment = {'PATH': str(self.bin) + os.pathsep + os.environ['PATH'], 'HOME': str(root / 'home'),
            'LC_ALL': 'C', 'TMPDIR': os.environ.get('TMPDIR', str(root)), 'RELEASE_R2_TRANSPORT': 's3',
            'R2_S3_ENDPOINT': 'https://storage.example.invalid', 'R2_BUCKET': 'fixture',
            'AWS_ACCESS_KEY_ID': 'fixture', 'AWS_SECRET_ACCESS_KEY': 'fixture',
            'DOWNLOAD_BASE_URL': 'https://downloads.paintedwolf.dev', 'PW_RELEASE_PROVIDER_ROOT': str(root),
            'PW_RELEASE_PROVIDER_KEY': str(key), 'FEED_SIGNING_KEYS_JSON': json.dumps({'format_version': 1,
                'generations': {'1': {'private_key': base64.b64encode(key.read_bytes()).decode(), 'password': ''}}})}

    def get(self, key):
        path = self.store / key
        return path.read_bytes() if path.is_file() else None

    def put(self, key, raw):
        path = self.store / key
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(raw)

    def mutations(self):
        path = self.root / 'writes.jsonl'
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def run(self, command, **options):
        command = list(map(str, command))
        if len(command) > 1 and Path(command[1]).parent == self.source / 'scripts':
            name = Path(command[1]).name
            command[1] = str(self.controller / 'scripts' / name)
            if name == 'release-halt.sh':
                channel = command[command.index('--channel') + 1]
                number = command[command.index('--generation') + 1]
                command += ['--current-file', str(self.store / f'updates/{channel}/key-{number}/latest.json'), '--dry-run']
                if '--last-good' in command:
                    good = command[command.index('--last-good') + 1]
                    command += ['--release-file', str(self.store / f'updates/releases/{good}.json')]
        options.setdefault('env', self.environment)
        options.setdefault('capture_output', True)
        options.setdefault('text', True)
        options.setdefault('timeout', 90)
        options.pop('check', None)
        result = self.execute(command, check=False, **options)
        if result.returncode:
            raise AssertionError(f'isolated release controller refused: {result.stderr}')
        return result


def main():
    tool, arguments = sys.argv[1], sys.argv[2:]
    root = Path(os.environ['PW_RELEASE_PROVIDER_ROOT'])
    store = root / 'objects'
    def option(name):
        return arguments[arguments.index(name) + 1]
    def selected(key):
        target = store / key
        if not key or not target.resolve().is_relative_to(store.resolve()):
            raise ValueError('fixture provider key escapes its isolated store')
        return target
    def write(key, source):
        target = selected(key)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(source.read_bytes())
        with (root / 'writes.jsonl').open('a') as output:
            output.write(json.dumps({'tool': tool, 'key': key}) + '\n')
    if tool == 'aws':
        if option('--endpoint-url') != 'https://storage.example.invalid':
            raise ValueError('fixture provider cannot contact another endpoint')
        if arguments[:2] == ['s3', 'cp']:
            uri = urlsplit(arguments[3])
            if uri.scheme != 's3' or uri.netloc != 'fixture':
                raise ValueError('fixture provider cannot write another bucket')
            write(uri.path.lstrip('/'), Path(arguments[2]))
            return
        if arguments[0] != 's3api' or option('--bucket') != 'fixture':
            raise ValueError('fixture provider requires its explicit bucket')
        operation = arguments[1]
        if operation == 'list-objects-v2':
            prefix = option('--prefix')
            rows = [{'Key': path.relative_to(store).as_posix(), 'Size': path.stat().st_size,
                     'ETag': hashlib.md5(path.read_bytes()).hexdigest()} for path in sorted(store.rglob('*'))
                    if path.is_file() and path.relative_to(store).as_posix().startswith(prefix)]
            print(json.dumps({'Contents': rows, 'IsTruncated': False}))
        elif operation in ('get-object', 'head-object'):
            path = selected(option('--key'))
            raw = path.read_bytes()
            if operation == 'get-object':
                Path(arguments[-1]).write_bytes(raw)
            print(json.dumps({'ContentLength': len(raw), 'ETag': hashlib.md5(raw).hexdigest(),
                              'Metadata': {'sha256': hashlib.sha256(raw).hexdigest()}}))
        else:
            raise ValueError('fixture provider received an unsupported S3 operation')
    elif tool == 'curl':
        urls = [value for value in arguments if value.startswith('https://')]
        if len(urls) != 1 or urlsplit(urls[0]).netloc != 'downloads.paintedwolf.dev':
            raise ValueError('fixture public provider requires its selected historical origin')
        Path(option('-o')).write_bytes(selected(urlsplit(urls[0]).path.lstrip('/')).read_bytes())
        if '--dump-header' in arguments:
            Path(option('--dump-header')).write_text('HTTP/1.1 200 OK\r\nCache-Control: no-store\r\n\r\n')
    elif tool == 'bun':
        if arguments[:4] != ['run', 'tauri', 'signer', 'sign']:
            raise ValueError('fixture signer received an unsupported command')
        pointer = Path(arguments[-1])
        raw = root / (pointer.name + '.minisig')
        comment = f'timestamp:1800000001\tfile:{pointer.name}\tversion:{option("--app-version")}'
        subprocess.run(['minisign', '-S', '-s', os.environ['PW_RELEASE_PROVIDER_KEY'], '-m', str(pointer),
                        '-x', str(raw), '-t', comment], check=True, capture_output=True, timeout=30)
        Path(str(pointer) + '.sig').write_text(base64.b64encode(raw.read_bytes()).decode())
    else:
        raise ValueError('fixture provider received an unsupported executable')


if __name__ == '__main__':
    main()
