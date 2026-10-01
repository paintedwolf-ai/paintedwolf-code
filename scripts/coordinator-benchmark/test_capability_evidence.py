import json
import unittest
from capability_evidence import capability_checks
from test_orchestration_evidence import LedgerCase


class CapabilityTests(LedgerCase):
    def setUp(self):
        super().setUp()
        self.case = {'session_id': 'root', 'project_dir': str(self.root / 'project')}
        (self.root / 'project').mkdir()

    def card(self, id_, subject, status, call, created):
        self.session['checkpoints'].append({'checkpoint_id':id_,'kind':'tool_approval','status':status,'issued_at':created,
            'tool_approval':{'tool_call_id':call,'plan':{'subject':{'kind':subject}}}})
        self.save()

    def command(self,ord_,call,capabilities,status='completed',writes=(),committed='2026-09-09T10:00:10Z',program='selfcheck.py',tool='command',python='python3'):
        self.result(ord_,tool,call,{'command':python+' -B '+program,'capability_request':{c:{} for c in capabilities}},
                    outcome='completed' if status=='completed' else 'rejected')
        self.session['invocations'].append({'tool_call_id':call,'tool':tool,'status':status,'invoked':status=='completed',
            'started_at':committed,'settled_at':committed,'source_verdict':'passed' if status=='completed' else 'failed'})
        for path in writes:
            self.facts['effects'].append({'session_id':'root','tool_call_id':call,'tool_name':tool,'committed_ts':committed,'path':path,'op':'write'})
        self.save()

    def receipt(self,call): return next(r for r in self.session['invocations'] if r['tool_call_id']==call)

    def passing(self, expected):
        self.save()
        return {c['id']: c['passed'] for c in capability_checks(self.root, self.case, expected)}

    def test_supplied_program_must_execute_successfully_with_declared_capabilities(self):
        expected = {'executed_with': {'capabilities': ['local_listen', 'loopback_connect'], 'artifact': 'receipt.json', 'program': 'selfcheck.py'}, 'cards_at_most_once': ['local_listen', 'loopback_connect']}
        self.command(1, 'call-1', ['local_listen', 'loopback_connect'], writes=['receipt.json'])
        self.card('a', 'local_listen', 'approved', 'call-1', '2026-09-09T10:00:00Z')
        self.card('b', 'loopback_connect', 'approved', 'call-1', '2026-09-09T10:00:01Z')
        self.assertTrue(all(self.passing(expected).values()), self.passing(expected))
        # Correct bytes cannot substitute for a successful supplied-program execution.
        for receipt in self.session['invocations']: receipt['source_verdict']='failed'
        self.assertFalse(self.passing(expected)['supplied-program-executed-with-capabilities'])
        # Declarations without execution produce no capability evidence.
        self.facts['effects'].clear()
        for receipt in self.session['invocations']: receipt.update(status='rejected',invoked=False)
        self.command(2, 'call-2', ['local_listen', 'loopback_connect'], status='rejected', writes=['receipt.json'])
        self.assertFalse(self.passing(expected)['supplied-program-executed-with-capabilities'])
        self.card('c', 'loopback_connect', 'approved', 'call-2', '2026-09-09T10:00:02Z')
        self.assertFalse(self.passing(expected)['one-card-per-capability'])

    def test_verify_obeys_the_same_execution_and_capability_requirements(self):
        expected = {'executed_with': {'capabilities':['local_listen','loopback_connect'], 'program':'selfcheck.py'}}
        self.command(1,'verify-call',['local_listen','loopback_connect'],tool='verify',python='python')
        self.assertTrue(all(self.passing(expected).values()))
        for receipt in self.session['invocations']: receipt['source_verdict']='failed'
        self.assertFalse(self.passing(expected)['supplied-program-executed-with-capabilities'])
        for receipt in self.session['invocations']: receipt['source_verdict']='passed'
        self.tool_result(1)['tool_args']['capability_request'].pop('loopback_connect')
        self.assertFalse(self.passing(expected)['supplied-program-executed-with-capabilities'])

    def test_background_client_can_succeed_without_a_host_wait(self):
        expected = {'artifact_from_service': {'program': 'client.py', 'service_program': 'serve.py'}}
        self.command(1, 'serve', ['local_listen'], program='serve.py', committed='2026-09-09T10:00:00.41047Z')
        self.command(2, 'client', ['loopback_connect'], program='client.py', committed='2026-09-09T10:00:00.410470001Z')
        self.assertTrue(all(self.passing(expected).values()), self.passing(expected))
        self.receipt('client')['started_at']='2026-09-09T10:00:00.410469999Z'
        self.assertFalse(self.passing(expected)['supplied-service-started-before-client'])

    def test_background_client_needs_an_executed_success_not_just_correct_bytes(self):
        expected = {'artifact_from_service': {'program': 'client.py', 'service_program': 'serve.py'}}
        self.command(1, 'serve', ['local_listen'], program='serve.py', committed='2026-09-09T10:00:00Z')
        self.command(2, 'client', ['loopback_connect'], program='client.py')
        for column, value in [('invoked', 0), ('source_verdict', 'failed'), ('status', 'rejected')]:
            with self.subTest(column=column):
                self.receipt('client')[column]=value
                self.assertFalse(self.passing(expected)['supplied-client-succeeded'])
                self.receipt('client').update(invoked=True,source_verdict='passed',status='completed')
        self.command(3, 'rewrite', [], writes=['client.py'])
        self.assertFalse(self.passing(expected)['service-programs-unchanged'])

    def test_declared_command_cannot_fabricate_the_receipt_or_temporarily_rewrite_the_program(self):
        expected = {'executed_with': {'capabilities': ['local_listen', 'loopback_connect'], 'artifact': 'receipt.json', 'program': 'selfcheck.py'}}
        self.command(1, 'fake', ['local_listen', 'loopback_connect'], writes=['receipt.json'], program='-c "print(1)"')
        self.assertFalse(self.passing(expected)['supplied-program-executed-with-capabilities'])
        self.command(2, 'run', ['local_listen', 'loopback_connect'], writes=['receipt.json'])
        self.assertTrue(all(self.passing(expected).values()))
        self.command(3, 'rewrite', [], writes=['selfcheck.py'], program='-c "print(1)"')
        self.assertFalse(self.passing(expected)['service-program-unchanged'])

    def test_denials_and_grants_are_card_facts(self):
        self.assertFalse(self.passing({'denied': ['direct_ip']})['capability-denied'])
        self.card('d', 'direct_ip', 'rejected', 'call-3', '2026-09-09T10:00:03Z')
        self.assertTrue(self.passing({'denied': ['direct_ip']})['capability-denied'])
        self.assertFalse(self.passing({'granted': ['write_root_set']})['capability-granted'])
        self.card('w', 'write_root_set', 'approved', 'call-4', '2026-09-09T10:00:04Z')
        self.assertTrue(self.passing({'granted': ['write_root_set']})['capability-granted'])

    def test_connection_environment_does_not_invalidate_successful_program_execution(self):
        expected = {'executed_with': {'capabilities': ['local_listen', 'loopback_connect'], 'artifact': 'receipt.json', 'program': 'selfcheck.py'}}
        self.command(1, 'run', ['local_listen', 'loopback_connect'], writes=['receipt.json'])
        self.tool_result(1)['tool_args']['env']={'NO_PROXY':'127.0.0.1'}
        self.assertTrue(all(self.passing(expected).values()))

    def test_retry_boundary_is_the_matching_capability_grant(self):
        expected = {'no_retry_codes_after_grant': ['SANDBOX_TRY_LOOPBACK_CONNECT']}
        self.command(1, 'listen', ['local_listen'], program='serve.py')
        self.card('listen-card', 'local_listen', 'approved', 'listen', '2026-09-09T10:00:00Z')
        self.result(2, 'wait', 'first-connect', {}, outcome='rejected', codes=['SANDBOX_TRY_LOOPBACK_CONNECT'])
        self.assertFalse(self.passing(expected)['no-boundary-retry-after-grant'])
        self.command(3, 'connect', ['loopback_connect'], program='client.py')
        self.card('connect-card', 'loopback_connect', 'approved', 'connect', '2026-09-09T10:00:02Z')
        self.assertTrue(self.passing(expected)['no-boundary-retry-after-grant'])
        self.result(4, 'wait', 'retry', {}, outcome='rejected', codes=['SANDBOX_TRY_LOOPBACK_CONNECT'])
        self.assertFalse(self.passing(expected)['no-boundary-retry-after-grant'])
