"""BB10 I18 systemic invariant tests: work limits measure performed work, not final candidate count."""
import json
import pathlib
import sqlite3
import tempfile
import unittest
from portable_command import delivery_oracles, grading, observe
from portable_command.contracts import bytes_value, load_suite, materialize
from portable_command.test_portable_command_harness import create_audit_db

SUITE = load_suite()
CASES = {c['id']: c for c in SUITE['cases']}
IDENT = {'git_head': 'a' * 40}


class BB10I18WorkLimitsTests(unittest.TestCase):
    def _seed_discovery(self, root, *, entries_log, exec_id='fe1', log_exec_id='fe1', stdout_lines=1):
        case = CASES['large-tree-discovery']
        proj, db_path, log_path = root / 'proj', root / 'store.db', root / 'sidecar.log'
        proj.mkdir(parents=True, exist_ok=True)
        create_audit_db(db_path)
        db = sqlite3.connect(db_path)
        db.execute("ALTER TABLE invocation_program_executions ADD COLUMN complete INTEGER DEFAULT 1")
        db.execute("CREATE TABLE sessions (id TEXT PRIMARY KEY, project_id TEXT)")
        db.execute("CREATE TABLE source_inventory_state (project_id TEXT, requested_generation INTEGER, completed_generation INTEGER, phase TEXT, file_count INTEGER, snapshot_id TEXT)")
        db.execute("CREATE TABLE source_snapshots (id TEXT PRIMARY KEY, file_count INTEGER, capture_quality TEXT)")
        db.execute("INSERT INTO sessions VALUES ('sess1234','proj1')")
        db.execute("INSERT INTO source_inventory_state VALUES ('proj1',1,1,'ready',10000,'snap1')")
        db.execute("INSERT INTO source_snapshots VALUES ('snap1',10000,'exact')")
        db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','completed',1)")
        db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',0,'')")
        db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,complete,effect_certainty) VALUES (?,'p1',1,'find','find','filesystem','completed',1,'none')", (exec_id,))
        stdout = ''.join(f'./tree/{i:05}.txt\n' for i in range(stdout_lines))
        db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (
            json.dumps({'outcome': 'completed', 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'completed', 'status': 0, 'stdout': stdout, 'stderr': ''})}),
        ))
        db.commit(); db.close()
        body = 'level=INFO msg="server listening"\n'
        if entries_log is not None:
            body += f'level=INFO msg="command discovery completed" execution_id={log_exec_id} entries={entries_log} directory_live_lists=1\n'
        body += 'level=INFO msg="shutdown signal received"\n'
        log_path.write_text(body)
        turns = [{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1']}]
        return case, proj, db_path, log_path, turns

    def test_traversal_below_at_over_limit_and_filtered_candidates_never_mask_visits(self):
        limit = delivery_oracles._traversal_visits_limit()
        self.assertGreater(limit, 0)
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            # Below limit and exact limit pass bounded_visits.
            for label, count in (('below', limit - 1), ('at_limit', limit)):
                with self.subTest(boundary=label):
                    case, proj, dbp, lp, turns = self._seed_discovery(root / label, entries_log=count, stdout_lines=1)
                    obs, audit = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=dbp, sidecar_log=lp, baseline_hashes={}, turn_records=turns, prelude_records=[])
                    self.assertEqual(audit['observation_gaps'], [])
                    self.assertTrue(obs['facts']['bounded_visits'])
            # Over limit MUST fail even when filtered stdout returns only 1 candidate and status == 0!
            case, proj, dbp, lp, turns = self._seed_discovery(root / 'over', entries_log=limit + 1, stdout_lines=1)
            obs_over, _ = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=dbp, sidecar_log=lp, baseline_hashes={}, turn_records=turns, prelude_records=[])
            self.assertFalse(obs_over['facts']['bounded_visits'])
            # Foreign execution_id in sidecar.log MUST NOT satisfy bounded_visits (unobserved -> gap).
            case, proj, dbp, lp, turns = self._seed_discovery(root / 'foreign', entries_log=10, exec_id='fe1', log_exec_id='fe-other', stdout_lines=1)
            obs_f, audit_f = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=dbp, sidecar_log=lp, baseline_hashes={}, turn_records=turns, prelude_records=[])
            self.assertNotIn('bounded_visits', obs_f['facts'])
            self.assertTrue(any('bounded_visits' in g for g in audit_f['observation_gaps']))

    def test_pin_segments_boundary_below_at_and_over_limit(self):
        limit = delivery_oracles._traversal_visits_limit()
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            for label, pin_count, expected in (('below', limit - 1, True), ('at_limit', limit, True), ('over', limit + 1, False)):
                with self.subTest(segment_boundary=label):
                    case, proj, dbp, lp, turns = self._seed_discovery(root / label, entries_log=None)
                    db = sqlite3.connect(dbp)
                    db.execute("CREATE TABLE invocation_program_revision_pin_segments (program_id TEXT, execution_id TEXT, first_ordinal INTEGER, pin_count INTEGER, body_sha256 TEXT)")
                    db.execute("INSERT INTO invocation_program_revision_pin_segments VALUES ('p1','fe1',0,?,?)", (pin_count, 'c' * 64))
                    db.commit(); db.close()
                    obs, _ = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=dbp, sidecar_log=lp, baseline_hashes={}, turn_records=turns, prelude_records=[])
                    self.assertEqual(obs['facts']['bounded_visits'], expected)

    def test_resource_profiles_enforce_exact_below_at_over_boundaries(self):
        profile = SUITE['resource_profiles'][0]
        binding = {'hardware_sha256': 'a' * 64, 'dataset_sha256': 'b' * 64, 'source_sha256': 'c' * 64, 'cache_state': 'cold'}
        at_limit = {**binding, **{k: profile[k] for k in ('wall_ms', 'peak_rss_bytes', 'spool_bytes', 'cancel_ms')}}
        below_limit = {**binding, **{k: max(0, profile[k] - 1) for k in ('wall_ms', 'peak_rss_bytes', 'spool_bytes', 'cancel_ms')}}
        self.assertTrue(grading.grade_resources(profile, below_limit, binding)['passed'])
        self.assertTrue(grading.grade_resources(profile, at_limit, binding)['passed'])
        for metric in ('wall_ms', 'peak_rss_bytes', 'spool_bytes', 'cancel_ms'):
            over = {**at_limit, metric: profile[metric] + 1}
            with self.subTest(over_metric=metric):
                self.assertFalse(grading.grade_resources(profile, over, binding)['passed'])

    def test_cancelled_or_over_limit_execution_blocks_publication_while_repeat_command_succeeds(self):
        case = CASES['first-mutation']
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            proj, db_path, log_path = root / 'proj', root / 'store.db', root / 'sidecar.log'
            base = materialize(case, proj)
            create_audit_db(db_path)
            log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')
            # Turn 1: cancelled/over-limit -> incomplete, ineligible, zero publication on disk.
            db = sqlite3.connect(db_path)
            db.execute("ALTER TABLE invocation_program_executions ADD COLUMN publication_eligible INTEGER DEFAULT 1")
            db.execute("INSERT INTO invocation_receipts VALUES ('r0','sess1234','c0','update_progress','progress','completed',1), ('r1','sess1234','c1','command','processes','interrupted',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','interrupted',130,'COMMAND_CANCELLED')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,publication_eligible) VALUES ('e1','p1',1,'write','','filesystem','interrupted','none',0)")
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (
                json.dumps({'outcome': 'interrupted', 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'interrupted', 'status': 130, 'host_stop': 'cancelled', 'stdout': '', 'stderr': ''})}),
            ))
            db.commit(); db.close()
            obs_cancel, _ = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes=base, turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c0', 'c1']}], prelude_records=[])
            g_cancel = grading.grade_case(case, obs_cancel, proj, IDENT)
            self.assertFalse(g_cancel['passed'])
            self.assertFalse(g_cancel['dimensions']['execution'])
            self.assertFalse((proj / 'out.txt').exists())

            # Unaffected fresh repeat command in a clean session succeeds with no stale carryover.
            proj2, db2, lp2 = root / 'proj2', root / 'store2.db', root / 'sidecar2.log'
            base2 = materialize(case, proj2)
            out_rel = next(iter(case['expected']['files']))
            (proj2 / out_rel).write_bytes(bytes_value(case['expected']['files'][out_rel]))
            create_audit_db(db2)
            lp2.write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')
            db = sqlite3.connect(db2)
            db.execute("INSERT INTO invocation_receipts VALUES ('r0','sess5678','c0','update_progress','progress','completed',1), ('r2','sess5678','c2','command','processes','completed',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p2','r2','completed',0,'')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,accepted_revision,published_revision) VALUES ('e0','p2',0,'printf','printf','processes','completed','none','',''), ('e2','p2',1,'write','','filesystem','completed','completed','rev1','rev2')")
            db.execute("INSERT INTO invocation_program_revision_pins VALUES ('p2','e2',0,'destination','absent',?)", (out_rel,))
            db.execute("INSERT INTO invocation_program_publications VALUES ('p2','sess5678','committed',1)")
            db.execute("INSERT INTO messages VALUES ('m2','sess5678',1,'tool','c2',?)", (
                json.dumps({'outcome': 'completed', 'content': '[command#2]\n' + json.dumps({'program_id': 'p2', 'state': 'completed', 'status': 0, 'stdout': '', 'stderr': ''})}),
            ))
            db.commit(); db.close()
            obs_ok, audit_ok = observe.build_observation(case, session_id='sess5678', identity=IDENT, project_dir=proj2, db_path=db2, sidecar_log=lp2, baseline_hashes=base2, turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c2', 'call_ids': ['c0', 'c2']}], prelude_records=[])
            self.assertEqual(audit_ok['observation_gaps'], [])
            self.assertTrue(grading.grade_case(case, obs_ok, proj2, IDENT)['passed'])
