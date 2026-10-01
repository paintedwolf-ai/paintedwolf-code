import json
import pathlib
import plistlib
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import service
from progress import save


class ServiceTests(unittest.TestCase):
    def job(self, root):
        return {'prepared':str(root/'prepared'), 'out':str(root/'run'), 'source_config':str(root/'credentials'),
                'python':sys.executable, 'service_python':sys.executable, 'controller_bootstrap':True, 'path':'/usr/bin:/bin', 'selection':['--models','candidate'], 'desired':'running'}

    def test_supervisor_restart_policy_and_argv_are_not_shell_commands(self):
        job=self.job(pathlib.Path('/work/with spaces/$literal%'))
        config=plistlib.loads(service.definition(job,'darwin'))
        self.assertEqual(config['KeepAlive'],{'SuccessfulExit':False})
        self.assertEqual(config['ThrottleInterval'],60)
        self.assertEqual(config['ProgramArguments'][-1],job['out'])
        unit=service.definition(job,'linux').decode()
        self.assertIn('Restart=on-failure',unit)
        self.assertIn('StartLimitIntervalSec=0',unit)
        self.assertIn('$$literal%%',unit)
        self.assertNotIn('/bin/sh',unit)
        with self.assertRaises(ValueError):service.definition(job,'unsupported')

    def test_restart_uses_retained_sampling_instead_of_initial_options(self):
        with tempfile.TemporaryDirectory() as directory:
            root=pathlib.Path(directory);job=self.job(root);out=pathlib.Path(job['out'])
            initial=service.controller_command(job)
            self.assertIn('--models',initial)
            out.mkdir();(out/'selection.json').write_text('{}')
            resumed=service.controller_command(job)
            self.assertIn('--resume',resumed)
            self.assertNotIn('--models',resumed)
            (out/'plan.json').write_text('{}')
            frozen=service.controller_command(job)
            self.assertIn(str(out/'source/scripts/coordinator-benchmark/run.py'),frozen)

    def test_controller_failure_then_recovery_does_not_repeat_initial_admission(self):
        with tempfile.TemporaryDirectory() as directory:
            root=pathlib.Path(directory);job=self.job(root)
            scripts=root/'prepared/source/scripts/coordinator-benchmark';scripts.mkdir(parents=True)
            (scripts/'python_runtime.py').write_text('import os,sys\nos.execv(sys.executable,[sys.executable,*sys.argv[1:]])\n')
            (scripts/'run.py').write_text('''import pathlib,sys
out=pathlib.Path(sys.argv[sys.argv.index('--out')+1]);out.mkdir(exist_ok=True)
with (out/'calls').open('a') as f:f.write('resume\\n' if '--resume' in sys.argv else 'initial\\n')
if '--resume' not in sys.argv:
 (out/'selection.json').write_text('{}')
 raise SystemExit(17)
''')
            state=service.job_directory(root/'run');state.mkdir();save(state/'job.json',job)
            self.assertEqual(service.execute(job),1)
            self.assertEqual(json.loads((state/'status.json').read_text())['phase'],'waiting_for_restart')
            self.assertEqual(service.execute(job),0)
            self.assertEqual((root/'run/calls').read_text(),'initial\nresume\n')
            self.assertEqual(service.execute(job),0)
            self.assertEqual((root/'run/calls').read_text(),'initial\nresume\n')

    def test_explicit_stop_never_relaunches_the_controller(self):
        with tempfile.TemporaryDirectory() as directory:
            root=pathlib.Path(directory);job=self.job(root);job['desired']='stopped'
            state=service.job_directory(root/'run');state.mkdir();save(state/'job.json',job)
            with patch.object(service.subprocess,'Popen') as launch:
                self.assertEqual(service.execute(job),0)
                launch.assert_not_called()

    def test_installation_targets_only_its_named_user_service(self):
        with tempfile.TemporaryDirectory() as directory:
            root=pathlib.Path(directory);job=self.job(root)
            with patch.object(service.pathlib.Path,'home',return_value=root), patch.object(service.subprocess,'run') as execute:
                service.install(job,'linux')
                self.assertEqual(execute.call_args.args[0],['systemctl','--user','enable','--now',service.service_name(job['out'])+'.service'])
                service.uninstall(job,'linux')
                self.assertFalse(list(root.glob('.config/systemd/user/*.service')))

    def test_adoption_retains_the_admitted_interpreter_without_bootstrapping(self):
        import fcntl
        with tempfile.TemporaryDirectory() as directory:
            root=pathlib.Path(directory);job=self.job(root);out=pathlib.Path(job['out']);out.mkdir()
            interpreter=out/'source/.bin/runtime/bin/python3';interpreter.parent.mkdir(parents=True)
            interpreter.symlink_to(sys.executable)
            attempt=out/'captures/episode-000/attempt-0000';attempt.mkdir(parents=True)
            save(attempt/'command.json',{'command':[str(interpreter),'application_environment.py']})
            self.assertEqual(service.adopted_interpreter(out),str(interpreter))
            with (out/'runner.lock').open('a') as lock:
                fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
                with self.assertRaises(BlockingIOError):service.adopted_interpreter(out)
            save(out/'selection.json',{});save(out/'plan.json',{})
            job.update(python=str(interpreter),controller_bootstrap=False)
            command=service.controller_command(job)
            self.assertEqual(command[0],str(interpreter))
            self.assertEqual(command[1],str(out/'source/scripts/coordinator-benchmark/run.py'))
            self.assertIn('--resume',command)
            other=out/'captures/episode-001/attempt-0000';other.mkdir(parents=True)
            save(other/'command.json',{'command':['/different/python']})
            with self.assertRaises(ValueError):service.adopted_interpreter(out)
