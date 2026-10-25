"""Expected-chat observation reads a stable pair of owned renderer documents, not an API selection."""
from contextlib import contextmanager
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_cases import notification_observer as observer
from linux_release.installed_cases.records import CaseFailed, CaseUnavailable
from verification_tests.distribution.installed.test_notification_wire_execution import proof

PROJECT='11111111-1111-4111-8111-111111111111'
SESSION='22222222-2222-4222-8222-222222222222'


class NotificationObserverTests(unittest.TestCase):
    def test_stable_renderer_pair_binds_project_session_and_revision(self):
        result=self.read()
        self.assertEqual(result['project_id'],PROJECT)
        self.assertEqual(result['session_id'],SESSION)
        self.assertEqual(result['workspace_path'],'/owned/project')
        self.assertEqual(len(result['renderer_revision_sha256']),64)

    def test_changed_renderer_documents_are_not_a_selected_chat(self):
        self.assertIsNone(self.read(changed=True))

    def test_mismatched_current_project_is_not_the_expected_scope(self):
        self.assertIsNone(self.read(active='33333333-3333-4333-8333-333333333333'))

    def test_confirmed_action_with_stale_stable_selection_is_a_retained_failure(self):
        before=proof()
        current={key:before[key]for key in ('surface','service','shell','delivery')}
        current.update(activation={'notification_id':1,'action':'default','sequence':4,
            'token':{'sha256':'c'*64,'sequence':3}},selected_session_id=None)
        selected={'project_id':PROJECT,'session_id':SESSION,'renderer_revision_sha256':'e'*64}
        instance=object.__new__(observer.NotificationObserver)
        instance.last_selected=selected
        instance.after=Mock(return_value=current)
        with patch.object(observer.time,'monotonic',side_effect=[0,2]),patch.object(observer.time,'sleep'):
            with self.assertRaises(CaseFailed)as raised:instance.settle(before,1)
        self.assertEqual(raised.exception.observations['notification_selected_scope'],selected)

    def test_unobserved_action_remains_unverified(self):
        before=proof()
        current={key:before[key]for key in ('surface','service','shell','delivery')}
        current.update(activation=None,selected_session_id=None)
        instance=object.__new__(observer.NotificationObserver)
        instance.last_selected=None
        instance.after=Mock(return_value=current)
        with patch.object(observer.time,'monotonic',side_effect=[0,2]),patch.object(observer.time,'sleep'):
            with self.assertRaises(CaseUnavailable):instance.settle(before,1)

    def read(self,changed=False,active=PROJECT):
        snapshot={'scope':{'projectId':PROJECT,'sessionId':SESSION},
            'session':{'id':SESSION,'project_id':PROJECT,'workspace_path':'/owned/project'}}
        with tempfile.TemporaryDirectory()as directory:
            root=Path(directory);(root/'app-state-v1').mkdir()
            @contextmanager
            def profile(app):yield root
            values=[(active,'a'*64),(snapshot,'b'*64),(active,'c'*64 if changed else 'a'*64),(snapshot,'b'*64)]
            with patch.object(observer,'profile',profile),patch.object(observer,'physical'),patch.object(
                    observer,'slice_document',side_effect=values):
                return observer.selected_chat(object())


if __name__=='__main__':unittest.main()
