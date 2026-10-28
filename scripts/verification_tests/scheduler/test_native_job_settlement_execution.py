"""Shared settlement invariants over fixture and actual native process facts."""
from pathlib import Path
import subprocess
import sys
import unittest


SCRIPT = r'''
import fcntl,json,os,pathlib,subprocess,sys,tempfile
from types import SimpleNamespace
checkout=pathlib.Path(sys.argv[1]).resolve()
sys.path[:0]=[str(checkout/'scripts'),str(checkout)]
from managed_job_settlement import JobSettlement
import verification_process

def child(path=None):
    body='import fcntl,sys\n'
    if path is not None:
        body+='held=open(sys.argv[1],"rb");fcntl.flock(held,fcntl.LOCK_EX)\n'
    body+='print("ready",flush=True);sys.stdin.buffer.read()\n'
    process=subprocess.Popen([sys.executable,'-c',body,*([str(path)] if path else [])],
        stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    assert process.stdout.readline()==b'ready\n'
    return process

def close(process):
    if process.stdin is not None:
        process.stdin.close();process.stdin=None
    try:process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        process.kill();process.wait(timeout=10)
    for stream in (process.stdout,process.stderr):stream.close()

for mode in ('fixture','native'):
  for case in ('match','wrong-ticket','unreleased','missing','before-status','after-status','during-cancel'):
    with tempfile.TemporaryDirectory() as temporary:
        root=pathlib.Path(temporary).resolve()
        os.environ['PW_TEST_ARTIFACT_ROOT']=str(root)
        directory=root/'jobs'/('a'*32);directory.mkdir(parents=True,mode=0o700)
        lock=directory/'completion.lock';lock.touch(mode=0o600)
        job={'job_id':'a'*32,'pid':os.getpid(),'state':'submitted',
             'wait':'./task --wait '+'a'*32,'log':str(directory/'output.log')}
        (directory/'job.json').write_text(json.dumps({'job_id':job['job_id'],'arguments':[],
            'submitted_at':1,'on_complete':None}))
        own,foreign=child(lock),child()
        try:
            identity=verification_process.identity(own.pid)
            row={'kind':'invocation','pid':own.pid,'owner_identity':identity,'ticket':'1-'+'b'*32}
            other={**row,'pid':foreign.pid,'owner_identity':verification_process.identity(foreign.pid),'ticket':'2-'+'c'*32}
            native=verification_process.observer()
            birth=native.birth(own.pid)
            observer=None if mode=='native' else SimpleNamespace(
                birth=lambda pid:{**birth,'pid':pid},
                files=lambda pid,limit:[(lock.stat().st_dev,lock.stat().st_ino)] if pid==own.pid else [])
            records=[];calls=[]
            value=JobSettlement(checkout=checkout,arguments=[],save=lambda name,row:records.append((name,row)),
                command=None,observer=observer)
            selected=value.invocation(job,{'runs':[other,row]})
            assert selected['entry']==row and selected['process']['uid']==os.getuid()
            assert selected['completion_lock']=={'device':lock.stat().st_dev,'inode':lock.stat().st_ino}
            assert value.invocation(job,{'runs':[other]}) is None
            try:value.invocation(job,{'runs':[row,{**row,'ticket':other['ticket']}]})
            except ValueError:pass
            else:raise AssertionError('multiple completion-lock holders were admitted')
            try:native.files(own.pid,0)
            except ValueError:pass
            else:raise AssertionError('native descriptor inventory ignored its bound')
            try:value.invocation(job,{'runs':[{**row,'owner_identity':'replaced'}]})
            except ValueError:pass
            else:raise AssertionError('replaced scheduler birth was admitted')
            terminal={'job_id':job['job_id'],'state':'completed','exit_code':201,'finished_at':1.0,'log':job['log']}
            def complete():
                (directory/'result.json').write_text(json.dumps(terminal))
                close(own)
            def command(arguments,name,timeout):
                calls.append(arguments)
                if arguments==['./task','test:status']:
                    if case=='after-status':complete()
                    return SimpleNamespace(returncode=0,stdout=json.dumps({'runs':[other] if case in ('missing','after-status') else [row]}))
                assert arguments[:4]==['./task','test:cancel','--',row['ticket']]
                if case=='during-cancel':
                    complete()
                    return SimpleNamespace(returncode=2,stdout='completion raced cancellation')
                return SimpleNamespace(returncode=0,stdout=json.dumps({
                    'ticket':'foreign' if case=='wrong-ticket' else row['ticket'], 'released':False}))
            value.command=command
            (directory/'result.json').write_text(json.dumps(terminal))
            assert value.completed(job) is None
            if case=='before-status':complete()
            if case=='match':
                complete()
                assert value.completed(job)==terminal
                continue
            if case in ('missing','wrong-ticket','unreleased'):
                try:value.settle(job)
                except ValueError:pass
                else:raise AssertionError('unproved cleanup was admitted')
                assert len(calls)=={'missing':1,'wrong-ticket':2,'unreleased':3}[case]
            else:
                assert value.settle(job) is True
                assert len(calls)=={'before-status':0,'after-status':1,'during-cancel':2}[case]
                assert records[-1]==('owned-cleanup-completion-race.json',terminal)
        finally:
            close(own);close(foreign)
print('fixture and native settlement controls passed')
'''


class NativeJobSettlementTests(unittest.TestCase):
    def test_settlement_keeps_its_invariants_with_fixture_and_native_observers(self):
        checkout = Path(__file__).resolve().parents[3]
        result = subprocess.run([sys.executable, '-I', '-B', '-c', SCRIPT, str(checkout)],
                                capture_output=True, text=True, timeout=120)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('fixture and native settlement controls passed', result.stdout)
