"""Exercise an explicitly admitted independent SSH Nix store without local fallback."""
from __future__ import annotations

import base64
import errno
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import shlex
import stat
import subprocess
import sys
import tempfile
import urllib.parse
import uuid

from ..installed_runtime import nix_command
from .app import InstalledApp
from .approval import approve_host_execution
from .nix_fixture import derivation, load, path_presence, realized, store_file
from .provider import ProviderFixture
from .records import CaseUnavailable
from .workflow import ToolTurn, wait_settled


def endpoint(value: str) -> tuple[str, int]:
    if not isinstance(value, str):
        raise ValueError('Nix remote fixture requires an explicit SSH store URI')
    uri = urllib.parse.urlsplit(value)
    if (uri.scheme != 'ssh-ng' or uri.username != 'pw-nix-builder' or uri.password
            or uri.path or uri.query or uri.fragment or uri.hostname is None or uri.port is None
            or not ipaddress.ip_address(uri.hostname).is_private or not 1 <= uri.port <= 65535):
        raise ValueError('Nix remote fixture endpoint is not a fixed private SSH store')
    return uri.hostname, uri.port


def select(binding: dict) -> tuple[dict, dict]:
    fixture = load(binding)
    remote = fixture['remote_builder']
    if not isinstance(remote, dict):
        raise CaseUnavailable('independent SSH Nix builder fixture is not provisioned')
    host, port = endpoint(remote.get('uri'))
    server = remote.get('server')
    expected = {'format_version', 'purpose', 'account', 'forced_command', 'client', 'host_public_key',
                'client_public_key_sha256', 'machine_id_sha256', 'system', 'sandbox'}
    if (not isinstance(server, dict) or set(server) != expected or type(server['format_version']) is not int or server['format_version'] != 1
            or server['purpose'] != 'independent-nix-ssh-builder-fixture' or server['account'] != 'pw-nix-builder'
            or server.get('client') != fixture['client']
            or not isinstance(server.get('machine_id_sha256'), str)
            or len(server['machine_id_sha256']) != 64
            or server['sandbox'] is not True or server['system'] != fixture['system']
            or server['machine_id_sha256'] == hashlib.sha256(Path('/etc/machine-id').read_bytes()).hexdigest()
            or server['forced_command'] != str(Path(server['client']['path']).parent / 'nix-daemon') + ' --stdio'):
        raise ValueError('SSH builder lacks independent native sandboxed controller identity')
    key = Path.home() / '.local/share/paintedwolf-nix-builder/id_ed25519'
    if remote.get('ssh_key') != str(key) or key.is_symlink():
        raise ValueError('SSH fixture may use only its dedicated selected identity')
    info = key.stat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o600:
        raise ValueError('SSH fixture identity is not a private owned regular file')
    public_path = Path(str(key) + '.pub')
    public_info = public_path.lstat()
    if not stat.S_ISREG(public_info.st_mode) or public_info.st_uid != os.getuid() or public_info.st_size > 4096:
        raise ValueError('SSH fixture public identity is not a bounded owned physical file')
    public = public_path.read_bytes()
    if hashlib.sha256(public).hexdigest() != server['client_public_key_sha256']:
        raise ValueError('SSH fixture differs from the controller-authorized public key')
    keygen = next((path for path in (Path('/usr/bin/ssh-keygen'), Path('/run/current-system/sw/bin/ssh-keygen')) if path.exists()), None)
    if keygen is None:
        raise CaseUnavailable('fixed system SSH key observer is unavailable')
    derived = subprocess.run([str(keygen), '-y', '-f', str(key)], capture_output=True, timeout=10)
    if derived.returncode or derived.stdout.split()[:2] != public.split()[:2]:
        raise ValueError('private SSH fixture key differs from its admitted public identity')
    known = Path('/etc/paintedwolf-nix-builder-known-hosts')
    info = known.lstat()
    if remote.get('known_hosts') != str(known) or not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022 or info.st_size > 4096:
        raise ValueError('SSH fixture host keys require fixed bounded root-controlled input')
    raw = known.read_bytes()
    public_host = base64.b64decode(server['host_public_key'], validate=True).split()
    if len(public_host) != 3 or public_host[0] != b'ssh-ed25519':
        raise ValueError('SSH builder requires its controlled Ed25519 host identity')
    expected_host = f'[{host}]:{port} '.encode() + b' '.join(public_host[:2]) + b'\n'
    if raw != expected_host or hashlib.sha256(raw).hexdigest() != remote.get('known_hosts_sha256'):
        raise ValueError('SSH fixture host identity differs from the independent controller')
    store_file({'path': remote.get('derivation'), 'sha256': remote.get('derivation_sha256')}, {'path', 'sha256'})
    graph = json.loads(nix_command(Path(fixture['client']['path']), 'derivation', 'show', remote['derivation']))
    derivation(graph, fixture, 'remote_builder')
    if realized(Path(fixture['client']['path']), remote['output']):
        raise CaseUnavailable('remote fixture output already exists in the local store')
    return fixture, remote


SCRIPT = r'''import json, os, pathlib, shlex, socket, subprocess, sys
nonce, mode = sys.argv[1:]
fixture = json.loads(pathlib.Path('/etc/paintedwolf-nix-fixture.json').read_text())
remote = fixture['remote_builder']
from urllib.parse import urlsplit
uri = urlsplit(remote['uri'])
observation = {'nonce':nonce,'uid':os.getuid(),'pid_namespace':os.stat('/proc/self/ns/pid').st_ino}
with socket.socket() as connection:
    connection.settimeout(5)
    try: connection.connect((uri.hostname,uri.port))
    except OSError as error: observation['socket_errno'] = error.errno
    else: observation['socket_errno'] = 0
config = pathlib.Path.cwd() / ('nix-config-' + nonce)
config.mkdir(mode=0o700)
environment = {'HOME':str(config),'PATH':'/run/current-system/sw/bin:/usr/bin:/bin','LC_ALL':'C',
'NIX_CONF_DIR':str(config),'NIX_USER_CONF_FILES':'/dev/null',
'NIX_SSHOPTS':shlex.join(['-i',remote['ssh_key'],'-o','UserKnownHostsFile='+remote['known_hosts'],
'-o','StrictHostKeyChecking=yes','-o','IdentitiesOnly=yes','-o','BatchMode=yes'])}
base = [fixture['client']['path'],'--extra-experimental-features','nix-command','--option','substituters','','--option','builders','']
steps = []
def command(label,args):
    result = subprocess.run(base+args,env=environment,capture_output=True,timeout=45)
    if len(result.stdout)>65536 or len(result.stderr)>65536: raise ValueError('SSH fixture evidence exceeds bound')
    steps.append({'id':label,'exit_code':result.returncode,'stdout':result.stdout.decode()})
    return result
ping = command('ping',['--store',remote['uri'],'store','ping','--json'])
if mode == 'approved' and ping.returncode == 0:
    before = command('remote-before',['--store',remote['uri'],'path-info','--json','--json-format','1',remote['output']])
    if before.returncode == 0 and json.loads(before.stdout) == {remote['output']:None}:
        copied = command('copy',['copy','--derivation','--to',remote['uri'],remote['derivation']])
        if copied.returncode == 0:
            built = command('build',['--store',remote['uri'],'build','--no-link','--max-jobs','1',remote['derivation']+'^out'])
            if built.returncode == 0:
                command('content',['--store',remote['uri'],'store','cat',remote['output']+'/result'])
                command('remote-after',['--store',remote['uri'],'path-info','--json','--json-format','1',remote['output']])
                command('local-after',['--store','daemon','path-info','--json','--json-format','1',remote['output']])
observation.update({'exit_code':ping.returncode,'stdout':json.dumps(steps)})
temporary = pathlib.Path('nix-observation-' + nonce + '.tmp')
temporary.write_text(json.dumps(observation));temporary.replace('nix-observation.json')
'''


def successful(ordinary: dict, approved: dict, remote: dict, namespace: int) -> bool:
    try:
        steps = json.loads(approved['stdout'])
        if (not isinstance(steps, list) or [row['id'] for row in steps] != [
                'ping', 'remote-before', 'copy', 'build', 'content', 'remote-after', 'local-after']
                or any(row.get('exit_code') != 0 for row in steps)):
            return False
        rows = {row['id']: row for row in steps}
        return (ordinary['socket_errno'] in {errno.EPERM, errno.EACCES, errno.ENETUNREACH} and ordinary['exit_code'] != 0
                and ordinary['pid_namespace'] != namespace and approved['socket_errno'] == 0
                and approved['pid_namespace'] == namespace and approved['exit_code'] == 0
                and rows['content']['stdout'] == remote['token'] + '\n'
                and not path_presence(json.loads(rows['remote-before']['stdout']), remote['output'])
                and path_presence(json.loads(rows['remote-after']['stdout']), remote['output'])
                and not path_presence(json.loads(rows['local-after']['stdout']), remote['output']))
    except (ValueError, TypeError, KeyError):
        return False


def scenario(binding: dict) -> tuple[str, dict, dict]:
    from .nix_workflows import client, witness
    client(binding)
    fixture, remote = select(binding)
    namespace = Path('/proc/self/ns/pid').stat().st_ino
    observations = {'endpoint': remote['uri'], 'server': remote['server'], 'derivation': remote['derivation'], 'phases': []}
    with tempfile.TemporaryDirectory(prefix='paintedwolf-nix-remote-') as directory:
        project = Path(directory)
        script = project / 'nix-remote.py'
        script.write_text(SCRIPT)
        with InstalledApp(binding) as app:
            for approved in (False, True):
                nonce = uuid.uuid4().hex
                args = {'command': shlex.join([str(Path(sys.executable).resolve()), str(script), nonce,
                                               'approved' if approved else 'ordinary']), 'timeout_ms': 240000}
                if approved:
                    args['capability_request'] = {'host_execution': True}
                with ProviderFixture([{'name': 'command', 'arguments': args}]) as provider, ToolTurn(app, provider, project) as turn:
                    turn.submit()
                    if approved:
                        approve_host_execution(turn, args)
                    results = turn.wait(timeout=240)
                    calls = [row for row in results if row.get('tool') == 'command']
                    if len(calls) != 1 or calls[0].get('outcome') != 'completed' or calls[0].get('invocation', {}).get('invoked') is not True:
                        raise CaseUnavailable('installed SSH fixture command did not complete an actual invocation')
                    observed = witness(turn, nonce)
                    wait_settled(app, turn.session_id)
                    observations['phases'].append({'native': observed, 'turn': turn.observations, 'provider': provider.observations})
            first, second = [phase['native'] for phase in observations['phases']]
            passed = successful(first, second, remote, namespace)
            client(binding)
            if load(binding) != fixture:
                raise ValueError('SSH fixture controller changed during installed delegation')
            observations['app'] = app.observations
        return 'passed' if passed else 'failed', observations, app.streams()
