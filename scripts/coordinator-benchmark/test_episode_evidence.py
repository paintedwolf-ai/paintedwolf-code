import json
import pathlib
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch
import episode_evidence
from test_episode_support import empty_facts


class EpisodeExportTests(unittest.TestCase):
    def test_export_uses_selected_driver_and_binds_the_requested_root(self):
        with tempfile.TemporaryDirectory() as temporary:
            capture=pathlib.Path(temporary)
            (capture/'store.db').write_bytes(b'closed application store')
            source=capture/'source';source.mkdir()
            project=capture/'delivery';project.mkdir()
            session='00000000-0000-0000-0000-000000000001'
            facts=empty_facts();facts['root_session_id']=session;facts['sessions'][0]['id']=session
            with patch.object(episode_evidence.subprocess,'run',return_value=SimpleNamespace(stdout=json.dumps(facts))) as invoke:
                episode_evidence.export(capture,{'session_id':session,'project_dir':str(project)},source)
            arguments=invoke.call_args.args[0]
            self.assertEqual(arguments[0],str(source/'.bin/lycaon-debug'))
            self.assertEqual(arguments[-2:],['--project',str(project)])
            self.assertEqual(invoke.call_args.kwargs['cwd'],source)
            self.assertEqual(episode_evidence.read(capture,session),facts)
            with self.assertRaises(ValueError):
                episode_evidence.export(capture,{'session_id':'not-a-session'},source)

    def test_mismatched_identity_or_concurrent_store_change_never_publishes_facts(self):
        for fault in ['root','version','store','wal']:
            with self.subTest(fault=fault),tempfile.TemporaryDirectory() as temporary:
                capture=pathlib.Path(temporary);database=capture/'store.db';database.write_bytes(b'closed')
                session='00000000-0000-0000-0000-000000000001'
                facts=empty_facts();facts['root_session_id']=session
                if fault=='root':facts['root_session_id']='other'
                if fault=='version':facts['version']=99
                def invoke(*args,**kwargs):
                    if fault=='store':database.write_bytes(b'changed')
                    if fault=='wal':(capture/'store.db-wal').write_bytes(b'pending write')
                    return SimpleNamespace(stdout=json.dumps(facts))
                with patch.object(episode_evidence.subprocess,'run',side_effect=invoke):
                    with self.assertRaises(ValueError):
                        episode_evidence.export(capture,{'session_id':session},capture)
                self.assertFalse(episode_evidence.path_for(capture,session).exists())
