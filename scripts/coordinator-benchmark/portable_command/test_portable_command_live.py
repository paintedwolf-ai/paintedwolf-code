import copy
import unittest
from portable_command.live import SOURCE_FIELDS, freeze_plan, load_live_manifest, summarize_slots


class PortableCommandLivePlan(unittest.TestCase):
    def inputs(self):
        manifest = load_live_manifest()
        source = {key: 'a'*64 for key in SOURCE_FIELDS}
        models = [{'role': row['role'], 'selection': row['selection'],
                   'backend_kind':'synthetic-supported-control', 'backend_version':'fixture-1',
                   'backend_artifact_sha256':'b'*64, 'resolved_model_id':row.get('resolved_id','synthetic/ministral-3-3b'),
                   'model_artifact_sha256':'c'*64, 'request_controls_sha256':'d'*64,
                   'runtime_identity_verified':True, 'alias_dependent':False}
                  for row in manifest['models']]
        authorization = {'allow_live':True, 'approval_reference':'synthetic-control',
                         'isolated_host_reference':'synthetic-control', 'maximum_cost_usd':1,
                         'allowed_resolved_models':[m['resolved_model_id'] for m in models]}
        return manifest, source, models, authorization

    def plan(self): return freeze_plan(*self.inputs())

    def record(self, plan, slot=0):
        episode = plan['episodes'][slot]
        model = next(m for m in plan['models'] if m['role'] == episode['model_role'])
        return {'slot':slot, 'episode':episode, 'plan_sha256':plan['plan_sha256'], 'outcome':'passed',
                'source':plan['source'], 'model':model, 'runtime_before':model['model_artifact_sha256'],
                'runtime_after':model['model_artifact_sha256'], 'raw_capture_sha256':'e'*64,
                **{k:True for k in ('artifact_grade','delivered_tests_grade','authority_grade','recovery_grade','closeout_grade')},
                'usage':{g:{'input_tokens':10,'cached_tokens':0,'output_tokens':5,'requests':1} for g in ('agent_stream','auxiliary')},
                'wall_ms':100, 'interventions':0, 'cost_usd':None, 'pricing_state':'unpriced'}

    def test_five_paired_alternating_attempts_and_every_missing_slot_retained(self):
        plan = self.plan()
        self.assertEqual(len(plan['episodes']), len(plan['manifest']['tasks'])*3*5*2)
        episodes = plan['episodes'][:10]
        self.assertEqual([e['attempt'] for e in episodes], [1,1,2,2,3,3,4,4,5,5])
        self.assertEqual([e['arm'] for e in episodes[:4]], ['real-shell-reference','production-native','production-native','real-shell-reference'])
        report = summarize_slots(plan, [self.record(plan)])
        self.assertFalse(report['accepted'])
        self.assertEqual(len(report['trials']), len(plan['episodes']))
        self.assertEqual(sum(t['outcome']=='pending' for t in report['trials']),len(plan['episodes'])-1)
        self.assertEqual(report['trials'][0]['outcome'], 'passed')
        record = self.record(plan)
        record['outcome'] = 'infrastructure'
        self.assertEqual(summarize_slots(plan,[record])['trials'][0]['outcome'], 'infrastructure')

    def test_missing_or_changed_identity_is_unqualified_and_never_replaced(self):
        plan = self.plan()
        for key, value in [('source',{}),('runtime_after','changed'),('raw_capture_sha256',''),('usage',{}),('delivered_tests_grade',False),('pricing_state','missing')]:
            record = self.record(plan)
            record[key] = value
            with self.subTest(key=key):
                self.assertEqual(summarize_slots(plan,[record])['trials'][0]['outcome'],'unqualified')
        for mutate in ('duplicate','unknown','plan','episode'):
            record = self.record(plan)
            records = [record]
            if mutate == 'duplicate': records.append(copy.deepcopy(record))
            if mutate == 'unknown': record['slot'] = len(plan['episodes'])
            if mutate == 'plan': record['plan_sha256'] = 'changed'
            if mutate == 'episode': record['episode'] = {}
            with self.subTest(mutate=mutate), self.assertRaises(ValueError): summarize_slots(plan,records)

    def test_freeze_requires_actual_identity_explicit_authorization_and_full_roster(self):
        for mutation in ('authorization','source','model','alias','roster','task','attempts'):
            manifest, source, models, auth = self.inputs()
            if mutation == 'authorization': auth['allow_live'] = False
            if mutation == 'source': source.pop('hardware_sha256')
            if mutation == 'model': models[0]['model_artifact_sha256'] = ''
            if mutation == 'alias': models[0]['alias_dependent'] = True
            if mutation == 'roster': models.pop()
            if mutation == 'task': manifest['tasks'].append('unfrozen-task')
            if mutation == 'attempts': manifest['attempts_per_task_model_arm'] = 1
            with self.subTest(mutation=mutation), self.assertRaises(ValueError): freeze_plan(manifest,source,models,auth)
        original = self.plan()
        manifest, source, models, auth = self.inputs()
        source['configuration_sha256'] = 'f'*64
        self.assertNotEqual(original['plan_sha256'],freeze_plan(manifest,source,models,auth)['plan_sha256'])

    def test_frozen_plan_cannot_mutate_and_aggregate_cost_stays_visible(self):
        plan = self.plan()
        records = [self.record(plan, slot) for slot in range(len(plan['episodes']))]
        for row in records: row['cost_usd'] = 0.01
        report = summarize_slots(plan, records)
        self.assertTrue(report['qualified'])
        self.assertFalse(report['cost_within_authorization'])
        self.assertFalse(report['accepted'])
        plan['manifest']['budgets']['tokens'] += 1
        with self.assertRaisesRegex(ValueError, 'changed after preregistration'):
            summarize_slots(plan, [])
