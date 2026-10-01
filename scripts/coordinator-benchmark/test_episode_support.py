"""Typed evidence builders for independent acceptance-rule controls."""
import pathlib
import tempfile
import unittest
from unittest.mock import patch
import episode_evidence


def empty_facts():
    return {'version':1,'root_session_id':'root','project_id':'project','execution':None,'allowance':None,'workers':[],
            'effects':[],'promotions':{},'snapshots':{},'verification':[],'delivered_paths':[],
            'sessions':[{'id':'root','parent_id':'','messages':[],'invocations':[],'checkpoints':[],'workflows':[],'verdicts':[]}]}


class EvidenceCase(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup)
        self.root=pathlib.Path(self.temp.name)
        self.facts=empty_facts()
        mock=patch.object(episode_evidence,'path_for',return_value=self.root/'facts.json')
        mock.start();self.addCleanup(mock.stop)
        self.save()

    @property
    def session(self): return self.facts['sessions'][0]

    def save(self):
        import json
        (self.root/'facts.json').write_text(json.dumps(self.facts))

    def job_by_id(self, id_): return next(j for j in self.facts['workers'] if j['id']==id_)

    def tool_result(self, ord_): return next(m['tool_result'] for m in self.session['messages'] if m['ord']==ord_)
