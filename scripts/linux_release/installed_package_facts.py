"""Read-only fixed installed package, process and custodian facts for root QA controllers."""
import hashlib,json,os,re,stat,subprocess,time
from pathlib import Path

UNIT='paintedwolf-vault-identity.service'
INSTALL_ROOT=Path('/opt/painted-wolf-code')

def stable(s):return (s.st_dev,s.st_ino,s.st_mode,s.st_uid,s.st_gid,s.st_size,s.st_mtime_ns,s.st_ctime_ns)
def digest(path):
 h=hashlib.sha256()
 with os.fdopen(os.open(path,os.O_RDONLY|os.O_NOFOLLOW),'rb') as f:
  before=os.fstat(f.fileno());assert stat.S_ISREG(before.st_mode)
  for b in iter(lambda:f.read(1048576),b''):h.update(b)
  assert stable(os.fstat(f.fileno()))==stable(before)==stable(path.lstat())
 return h.hexdigest()

def kernel(pid):
 p=Path('/proc')/str(pid)
 try:
  fields=(p/'stat').read_text().rpartition(') ')[2].split()
  uids=next(r.split()[1:] for r in (p/'status').read_text().splitlines() if r.startswith('Uid:'))
  return {'pid':pid,'birth':int(fields[19]),'pgid':int(fields[2]),'sid':int(fields[3]),'uids':[int(x) for x in uids]}
 except (FileNotFoundError,ProcessLookupError):return None

class PackageObservationFailure(TimeoutError):
 def __init__(self,phase,arguments,*,timeout_seconds,deadline_exhausted,timed_out):
  self.facts={'format_version':1,'phase':phase,'arguments':list(arguments),
              'code':'package_observation_timeout' if timed_out else 'package_observation_deadline_exhausted',
              'exit_code':None,'timeout_seconds':timeout_seconds,
              'deadline_exhausted':deadline_exhausted,'timed_out':timed_out}
  super().__init__(self.facts['code'])


def query(args, *, deadline=None, phase='package-query'):
 timeout=5 if deadline is None else min(5, deadline-time.monotonic())
 if timeout<=0:
  raise PackageObservationFailure(phase,args,timeout_seconds=0,deadline_exhausted=True,timed_out=False)
 try:
  p=subprocess.run(args,stdin=subprocess.DEVNULL,capture_output=True,text=True,timeout=timeout,
                   env={'PATH':'/run/current-system/sw/bin:/usr/bin:/bin','LC_ALL':'C'})
 except subprocess.TimeoutExpired as error:
  raise PackageObservationFailure(phase,args,timeout_seconds=timeout,
      deadline_exhausted=deadline is not None and time.monotonic()>=deadline,timed_out=True) from error
 return dict(arguments=args,exit_code=p.returncode,stdout=p.stdout,stderr=p.stderr)

def unit_state(*, deadline=None):
 p=query(['/usr/bin/systemctl','show',UNIT,'-p','MainPID','-p','ControlGroup','-p','ActiveState','-p','SubState'], deadline=deadline,phase='unit-state')
 assert p['exit_code']==0
 fields=dict(line.split('=',1) for line in p['stdout'].splitlines() if '=' in line)
 assert set(fields)=={'MainPID','ControlGroup','ActiveState','SubState'}
 return fields

def authority(*, deadline=None):
 """Minimal pinned package authority; no process, service-state or dpkg inventory."""
 deadline=time.monotonic()+5 if deadline is None else deadline
 assert os.geteuid()==0, 'root package authority required'
 observation_deadline(deadline,'package-authority.begin')
 policy=Path('/etc/paintedwolf/launch.json')
 def root_file(path):
  info=path.lstat()
  assert stat.S_ISREG(info.st_mode) and info.st_uid==info.st_gid==0 and not info.st_mode&0o022
  assert path.resolve()==path
 root_file(policy)
 assert policy.stat().st_size<=1048576
 before=stable(policy.lstat());raw=policy.read_bytes()
 assert before==stable(policy.lstat()), 'package policy changed'
 selected=json.loads(raw)
 generation=Path(selected['generation_root'])
 assert generation.parent==INSTALL_ROOT/'generations' and re.fullmatch('[a-f0-9]{64}',generation.name)
 assert selected['manifest_sha256']==generation.name
 manifest=generation/'manifest.json';bootstrap=Path('/usr/libexec/painted-wolf-code/pw-launch')
 for path in (manifest,bootstrap): root_file(path)
 manifest_sha=digest(manifest);bootstrap_sha=digest(bootstrap)
 assert manifest_sha==generation.name and before==stable(policy.lstat())
 observation_deadline(deadline,'package-authority.complete')
 return dict(generation=generation.name,policy_sha256=hashlib.sha256(raw).hexdigest(),
             manifest_sha256=manifest_sha,bootstrap_sha256=bootstrap_sha)


def observation_deadline(deadline,phase):
 if time.monotonic()>=deadline:
  raise PackageObservationFailure(phase,[],timeout_seconds=0,deadline_exhausted=True,timed_out=False)


def image_inventory(*, deadline):
 """Capture sealed executable references under one unchanged package selection."""
 assert os.geteuid()==0, 'root package observer required'
 observation_deadline(deadline,'image-inventory.begin')
 selected_authority=authority(deadline=deadline)
 policy=Path('/etc/paintedwolf/launch.json');selected=json.loads(policy.read_bytes())
 generation=Path(selected['generation_root']);assert generation.parent==Path('/opt/painted-wolf-code/generations')
 assert generation.name==selected['manifest_sha256']==digest(generation/'manifest.json')
 assert generation.name==selected_authority['generation']
 images=[];generations=[];custodian_ids=set();sources={}
 for root in sorted(generation.parent.iterdir()):
  observation_deadline(deadline,'image-inventory.generations')
  assert root.is_dir() and not root.is_symlink()
  manifest=root/'manifest.json';info=manifest.lstat();assert info.st_uid==info.st_gid==0 and not info.st_mode&0o022
  raw=json.loads(manifest.read_bytes());generations.append({'path':str(root),'manifest_sha256':digest(manifest)})
  image_roots=[root,*([Path('/opt/painted-wolf-code')] if root==generation else [])]
  for entry in raw['payload']:
   observation_deadline(deadline,'image-inventory.files')
   relative=Path(entry['path']);assert not relative.is_absolute() and '..' not in relative.parts
   if entry['kind']!='file' or not entry['mode']&0o111:continue
   for image_root in image_roots:
    image=image_root/relative
    if not image.exists():continue
    s=image.lstat();assert stat.S_ISREG(s.st_mode) and s.st_uid==s.st_gid==0 and not s.st_mode&0o022
    sources[str(image)]=stable(s)
    row={'device':s.st_dev,'inode':s.st_ino,'path':str(image),'role':str(relative)};images.append(row)
    if str(relative)=='bin/pw-vault-identity':custodian_ids.add((s.st_dev,s.st_ino))
 assert authority(deadline=deadline)==selected_authority, 'package selection changed during image capture'
 return dict(generation=generation.name,policy=selected,generation_inventory=generations,
             manifest_images=images,custodian_image_ids=custodian_ids,image_sources=sources,
             authority=selected_authority)


def unchanged_images(inventory, identities):
 """A cached executable reference cannot survive replacement or retirement."""
 for row in inventory['manifest_images']:
  if (row['device'],row['inode']) not in identities:continue
  try: observed=(Path(row['path'])).lstat()
  except FileNotFoundError as error:raise ValueError('sealed executable reference retired') from error
  assert stable(observed)==inventory['image_sources'][row['path']], 'sealed executable reference changed'


def image_processes(inventory, *, deadline):
 """Refresh only kernel process facts against the retained sealed image inventory."""
 ids={(row['device'],row['inode']) for row in inventory['manifest_images']}
 processes=[]
 for proc in Path('/proc').iterdir():
  observation_deadline(deadline,'image-processes.inventory')
  if not proc.name.isdigit():continue
  try:
   before=kernel(int(proc.name))
   if before is None:continue
   exe=proc/'exe';observed=exe.stat();identity=(observed.st_dev,observed.st_ino)
   if identity not in ids:continue
   target=os.readlink(exe);after=kernel(int(proc.name))
   repeated=exe.stat();repeated_target=os.readlink(exe)
  except (FileNotFoundError,ProcessLookupError):continue
  assert before==after, 'package process changed during executable capture'
  assert stable(repeated)==stable(observed) and repeated_target==target, 'package executable changed during capture'
  unchanged_images(inventory,{identity})
  assert kernel(int(proc.name))==before, 'package process changed during image validation'
  assert stable(exe.stat())==stable(observed) and os.readlink(exe)==target, 'package executable changed during image validation'
  assert kernel(int(proc.name))==before, 'package process changed after executable validation'
  processes.append({**before,'exe':target,'device':observed.st_dev,'inode':observed.st_ino})
 return processes


def snapshot(*, deadline=None):
 deadline=time.monotonic()+30 if deadline is None else deadline
 sealed_inventory=image_inventory(deadline=deadline)
 policy=Path('/etc/paintedwolf/launch.json');selected=sealed_inventory['policy']
 generation=Path(selected['generation_root']);images=sealed_inventory['manifest_images']
 generations=sealed_inventory['generation_inventory'];custodian_ids=sealed_inventory['custodian_image_ids']
 desktop=[];custodians=[]
 unit=unit_state(deadline=deadline);main=int(unit['MainPID'])
 for row in image_processes(sealed_inventory,deadline=deadline):
  if (row['device'],row['inode']) in custodian_ids:
   try:cgroups=(Path('/proc')/str(row['pid'])/'cgroup').read_text().splitlines()
   except (FileNotFoundError,ProcessLookupError):continue
   assert row['pid']==main and unit['ControlGroup']=='/system.slice/'+UNIT
   assert any(value.split(':',2)[-1]==unit['ControlGroup'] for value in cgroups)
   expected={key:row[key] for key in ('pid','birth','pgid','sid','uids')}
   if kernel(row['pid'])!=expected:return snapshot(deadline=deadline)
   row['unit']=unit;row['cgroups']=cgroups;custodians.append(row)
  else:desktop.append(row)
 if unit_state(deadline=deadline)!=unit:return snapshot(deadline=deadline)
 assert (main==0 and not custodians) or (main>0 and len(custodians)==1)
 services={}
 for name in ('paintedwolf-vault-identity.socket',UNIT):
  services[name]={key:query(['/usr/bin/systemctl',key,name],deadline=deadline,phase='snapshot.service-status') for key in ('is-enabled','is-active')}
  services[name]['show']=query(['/usr/bin/systemctl','show',name,'-p','LoadState','-p','UnitFileState','-p','ActiveState','-p','SubState','-p','MainPID'],deadline=deadline,phase='snapshot.service-details')
 package_inventory=query(['dpkg-query','-W','-f=${binary:Package}\t${Version}\n'],deadline=deadline,phase='snapshot.package-inventory');assert package_inventory['exit_code']==0
 selected_snapshot={'kind':'package-inventory','channel':None,'value':hashlib.sha256(json.dumps(package_inventory,sort_keys=True).encode()).hexdigest()}
 assert authority(deadline=deadline)==sealed_inventory['authority'], 'package selection changed during snapshot'
 v=os.statvfs(INSTALL_ROOT)
 return dict(generation=generation.name,policy=selected,policy_sha256=digest(policy),manifest_sha256=digest(generation/'manifest.json'),generation_inventory=generations,manifest_images=images,live_product_processes=desktop,custodian_processes=custodians,services=services,package_inventory=package_inventory,qualification_snapshot=selected_snapshot,available_bytes=v.f_bavail*v.f_frsize,bootstrap_sha256=digest(Path('/usr/libexec/painted-wolf-code/pw-launch')),package=query(['/usr/bin/dpkg-query','--show','--showformat=${Package}\t${Version}\t${Architecture}\t${db:Status-Status}\n','painted-wolf-code'],deadline=deadline,phase='snapshot.selected-package'))
