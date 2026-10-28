"""Materialize and validate exact frozen Python in a root-controlled capture.

Preparation never authenticates a person. The privileged bootstraps subsequently
verifies every captured Python byte before importing any project module.
"""
from __future__ import annotations

import base64
import hashlib
import io
import json
from pathlib import Path, PurePosixPath
import re
import subprocess
import tarfile

from . import protocol
from ..installed_cases.native_command import command as bounded_command

MAX_CAPTURE = 8 << 20
PREFIXES = ('scripts/linux_release/',)

PREPARE = '''import base64,fcntl,hashlib,json,os,re,stat,sys
from pathlib import Path,PurePosixPath
tree=sys.argv[1]
if os.geteuid()!=0 or not re.fullmatch('[a-f0-9]{40}',tree):
 raise SystemExit(2)
raw=sys.stdin.buffer.read((8<<20)+1)
if len(raw)>8<<20:
 raise SystemExit(2)
document=json.loads(raw)
if set(document)!={'format_version','driver_tree','files'} or type(document['format_version']) is not int or document['format_version']!=1 or document['driver_tree']!=tree or not isinstance(document['files'],dict) or not 1<=len(document['files'])<=384:
 raise SystemExit(2)
files={}
for name,value in document['files'].items():
 path=PurePosixPath(name)
 if str(path)!=name or path.is_absolute() or '..' in path.parts or path.suffix!='.py' or not (name=='scripts/__init__.py' or name.startswith('scripts/linux_release/')):
  raise SystemExit(2)
 body=base64.b64decode(value,validate=True)
 if len(body)>1048576:
  raise SystemExit(2)
 files[name]=body
manifest={'format_version':1,'driver_tree':tree,'files_sha256':{name:hashlib.sha256(body).hexdigest() for name,body in files.items()}}
encoded=(json.dumps(manifest,sort_keys=True,separators=(',',':'))+'\\n').encode()
base=Path('/var/lib/paintedwolf-qualification')
for parent in reversed(base.parents):
 info=parent.lstat()
 if not stat.S_ISDIR(info.st_mode) or info.st_uid!=0 or info.st_gid!=0 or info.st_mode&0o022:
  raise SystemExit(2)
if not base.exists():
 base.mkdir(mode=0o700)
info=base.lstat()
if not stat.S_ISDIR(info.st_mode) or info.st_uid!=0 or info.st_gid!=0 or info.st_mode&0o022:
 raise SystemExit(2)
fd=os.open(base/'.capture.lock',os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW,0o600)
try:
 info=os.fstat(fd)
 if not stat.S_ISREG(info.st_mode) or info.st_uid!=0 or info.st_gid!=0 or stat.S_IMODE(info.st_mode)!=0o600:
  raise SystemExit(2)
 fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
 root=base/tree
 if root.exists() or root.is_symlink():
  info=root.lstat()
  if not stat.S_ISDIR(info.st_mode) or info.st_uid!=0 or info.st_gid!=0 or info.st_mode&0o022:
   raise SystemExit(2)
  marker=root/'capture.json'
  info=marker.lstat()
  if not stat.S_ISREG(info.st_mode) or info.st_uid!=0 or info.st_gid!=0 or info.st_mode&0o222 or info.st_size>1048576 or marker.read_bytes()!=encoded:
   raise SystemExit(2)
 else:
  staging=base/('.partial-'+tree+'-'+os.urandom(8).hex())
  staging.mkdir(mode=0o700)
  for name,body in files.items():
   target=staging/name
   target.parent.mkdir(mode=0o700,parents=True,exist_ok=True)
   with target.open('xb') as stream:
    stream.write(body)
   target.chmod(0o444)
  with (staging/'capture.json').open('xb') as stream:
   stream.write(encoded)
  (staging/'capture.json').chmod(0o444)
  for directory in sorted((node for node in staging.rglob('*') if node.is_dir()),key=lambda node:len(node.parts),reverse=True):
   directory.chmod(0o555)
  staging.chmod(0o555)
  staging.rename(root)
 sys.stdout.write(json.dumps({'prepared':True,'driver_tree':tree,'manifest_sha256':hashlib.sha256(encoded).hexdigest()},sort_keys=True)+'\\n')
finally:
 os.close(fd)
'''
PREPARE = 'try:\n' + ''.join(' ' + line for line in PREPARE.splitlines(keepends=True)) + \
    'except BaseException:\n raise SystemExit(2) from None\n'


def document(checkout, tree):
    if not isinstance(tree, str) or not re.fullmatch(r'[a-f0-9]{40}', tree):
        raise ValueError('Polkit root capture requires its exact frozen source tree')
    # Git selects committed bytes, including namespace initializers, rather than
    # whatever another worker may currently have in its checkout.
    names = subprocess.check_output(['git', 'ls-tree', '-r', '-l', '-z', tree,
        '--', 'scripts/__init__.py', 'scripts/linux_release'], cwd=checkout)
    selected, total = [], 0
    for entry in names.split(b'\0'):
        if not entry:
            continue
        header, name = entry.split(b'\t', 1)
        if not name.endswith(b'.py'):
            continue
        mode, kind, _, size = header.split()
        if mode not in {b'100644', b'100755'} or kind != b'blob' or not size.isdigit() or int(size) > 1 << 20:
            raise ValueError('Polkit source capture requires bounded regular Python Git objects')
        selected.append(name.decode('utf-8'))
        total += int(size)
    if not 1 <= len(selected) <= 384 or total > 6 << 20:
        raise ValueError('Polkit root source capture exceeds its fixed source inventory or byte bound')
    archive = subprocess.check_output(['git', 'archive', '--format=tar', tree, '--', *selected], cwd=checkout)
    files = {}
    with tarfile.open(fileobj=io.BytesIO(archive), mode='r:') as stream:
        for member in stream:
            if not member.isfile():
                continue
            path = PurePosixPath(member.name)
            if (str(path) != member.name or path.is_absolute() or '..' in path.parts or path.suffix != '.py'
                    or not (member.name == 'scripts/__init__.py' or member.name.startswith(PREFIXES))
                    or member.name in files or member.size > 1 << 20):
                raise ValueError('Polkit root capture contains an unsupported source member')
            files[member.name] = stream.extractfile(member).read()
    if not 1 <= len(files) <= 384:
        raise ValueError('Polkit root source capture exceeds its fixed inventory')
    if set(files) != set(selected):
        raise ValueError('Polkit root archive differs from its exact selected Python Git objects')
    manifest = {'format_version': 1, 'driver_tree': tree,
                'files_sha256': {name: hashlib.sha256(body).hexdigest() for name, body in files.items()}}
    encoded = (json.dumps(manifest, sort_keys=True, separators=(',', ':')) + '\n').encode()
    payload = json.dumps({'format_version': 1, 'driver_tree': tree, 'files': {
        name: base64.b64encode(body).decode('ascii') for name, body in files.items()}},
        sort_keys=True, separators=(',', ':')).encode()
    if len(payload) > MAX_CAPTURE:
        raise ValueError('Polkit root source capture exceeds its fixed byte bound')
    return payload, hashlib.sha256(encoded).hexdigest()


def prepare(actor, target, execution, checkout):
    from ..campaign.guests import command
    protocol.binding(execution)
    if (actor.nonce != execution['execution_nonce']
            or actor.value['ssh'] != {key: target[key] for key in ('host', 'port', 'user')}):
        raise ValueError('Polkit root capture differs from the physical actor guest lease')
    payload, expected = document(Path(checkout), execution['driver_tree'])
    arguments = ['/usr/bin/sudo', '-n', '/usr/bin/env', '-i', 'PATH=/usr/bin:/bin',
                 'LANG=C.UTF-8', 'HOME=/root', '/usr/bin/python3', '-I', '-B', '-c', PREPARE, execution['driver_tree']]
    code, stdout, _ = bounded_command(command(target, arguments), timeout=30, limit=MAX_CAPTURE, input_bytes=payload)
    if code != 0:
        raise ValueError('Polkit root source preparation did not complete')
    from .mailbox import decode
    result = decode(stdout)
    if result != {'prepared': True, 'driver_tree': execution['driver_tree'], 'manifest_sha256': expected}:
        raise ValueError('Polkit root source preparation differs from its exact frozen bytes')
    return result


# This runs in isolated Python before importing any captured project code. The
# capture is prepared separately by the trusted controller, never by the QA UID.
CAPTURE_IMPORT = '''import hashlib,json,stat,sys
from pathlib import Path
tree=sys.argv.pop(1)
root=Path('/var/lib/paintedwolf-qualification')/tree
for node in [*reversed(root.parents),root]:
 info=node.lstat()
 if not stat.S_ISDIR(info.st_mode) or info.st_uid!=0 or info.st_gid!=0 or info.st_mode&0o022:
  raise SystemExit(2)
marker=root/'capture.json'
info=marker.lstat()
if not stat.S_ISREG(info.st_mode) or info.st_uid!=0 or info.st_gid!=0 or info.st_mode&0o222 or info.st_size>1048576:
 raise SystemExit(2)
capture=json.loads(marker.read_bytes())
if set(capture)!={'format_version','driver_tree','files_sha256'} or type(capture['format_version']) is not int or capture['format_version']!=1 or capture['driver_tree']!=tree:
 raise SystemExit(2)
actual={}
for index,node in enumerate(root.rglob('*')):
 info=node.lstat()
 if index>=512 or info.st_uid!=0 or info.st_gid!=0 or info.st_mode&0o022 or not (stat.S_ISDIR(info.st_mode) or stat.S_ISREG(info.st_mode)):
  raise SystemExit(2)
 if stat.S_ISDIR(info.st_mode):
  if node.name=='__pycache__':
   raise SystemExit(2)
 elif node==marker:
  continue
 elif node.suffix=='.py' and not info.st_mode&0o222 and info.st_size<=1048576:
  actual[node.relative_to(root).as_posix()]=hashlib.sha256(node.read_bytes()).hexdigest()
 else:
  raise SystemExit(2)
if actual!=capture['files_sha256']:
 raise SystemExit(2)
sys.path.insert(0,str(root))
'''
OBSERVER_BOOTSTRAP = CAPTURE_IMPORT + 'from scripts.linux_release.installed_input.polkit_root import main\nmain()\n'
ACCOUNT_BOOTSTRAP = CAPTURE_IMPORT + 'from scripts.linux_release.installed_input.unlock_account import main\nmain()\n'
OBSERVER_BOOTSTRAP = 'try:\n' + ''.join(' ' + line for line in OBSERVER_BOOTSTRAP.splitlines(keepends=True)) + \
    'except BaseException:\n raise SystemExit(2) from None\n'
ACCOUNT_BOOTSTRAP = 'try:\n' + ''.join(' ' + line for line in ACCOUNT_BOOTSTRAP.splitlines(keepends=True)) + \
    'except BaseException:\n raise SystemExit(2) from None\n'

