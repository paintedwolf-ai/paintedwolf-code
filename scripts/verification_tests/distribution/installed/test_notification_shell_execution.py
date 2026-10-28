"""Pre-dispatch refresh follows the pinned shell ancestry and cannot re-select a replacement banner."""
import time
import unittest
from types import SimpleNamespace
from unittest.mock import MagicMock, patch
from contextlib import nullcontext

from verification_tests.distribution.installed.test_notification_wire_execution import proof

from linux_release.installed_input import accessibility, notification_shell as shell
from linux_release.installed_cases.records import CaseFailed, CaseUnavailable


class Node:
    def __init__(self, path, parent=None):
        self.app=SimpleNamespace(bus_name=':1.0')
        self.path='/org/a11y/atspi/accessible/'+path
        self.parent,self.children=parent,[]
        if parent is not None:parent.children.append(self)
    def get_process_id(self):return 200
    def get_parent(self):return self.parent
    @property
    def childCount(self):return len(self.children)
    def __getitem__(self,index):return self.children[index]


class NotificationShellTests(unittest.TestCase):
    def setUp(self):
        self.app=Node('root')
        self.panel=Node('panel',self.app)
        self.banner=Node('banner',self.panel)
        self.expected=[accessibility.identity(node)for node in (self.banner,self.panel,self.app)]

    def capture(self, *, changed=False):
        before = proof()
        reader = MagicMock()
        reader.__enter__.return_value = reader
        names = {'org.freedesktop.Notifications': before['service'], shell.MUTTER: before['shell']}
        def call(path, interface, method, args, signature, **kwargs):
            if method == 'GetCurrentState':
                self.assertEqual(kwargs['destination'], before['shell']['bus_name'])
                self.assertEqual(signature, shell.STATE_SIGNATURE)
                return SimpleNamespace(get_type_string=lambda: signature, unpack=lambda: ())
            value = args[1][0]
            target = names[value] if method == 'GetNameOwner' else next(
                item for item in names.values() if item['bus_name'] == value)
            result = target['bus_name'] if method == 'GetNameOwner' else (
                shell.os.getuid() if method == 'GetConnectionUnixUser' else target['process']['pid'])
            return SimpleNamespace(unpack=lambda: (result,))
        reader.call.side_effect = call
        authority = {'unique_owner': before['shell']['bus_name'], 'pid': before['shell']['process']['pid'],
            'start_time_ticks': before['shell']['process']['start_time_ticks'] + int(changed)}
        processes = {item['pid']: item for item in (before['service']['process'], before['shell']['process'],
            before['surface']['launch'], before['surface']['engine'], before['surface']['main']['process'])}
        mapping = {key: value for key, value in before['geometry'].items() if key not in {'display_owner', 'shell_extent'}}
        with patch.object(shell, 'Reader', return_value=reader) as create, patch.object(
                shell.installed_gnome, 'owner', return_value=authority) as owner, patch.object(
                shell, 'process_identity', side_effect=processes.__getitem__), patch.object(
                shell.notification_geometry, 'mapping', return_value=mapping), patch.object(
                shell, 'atspi_client', SimpleNamespace(session_bus=lambda _: nullcontext())), patch.object(
                shell, 'load_api'), patch.object(shell, 'capture_target', return_value=before['target']):
            result = shell.observe(*(before[key] for key in ('surface', 'service', 'shell', 'delivery')),
                deadline=time.monotonic() + 10)
            self.assertTrue(create.call_args.kwargs['user_only'])
            self.assertEqual(owner.call_count, 2)
            return result

    def test_observation_reuses_canonical_shell_authority_and_bounded_private_reader(self):
        self.assertEqual(self.capture()['target'], proof()['target'])

    def test_changed_native_shell_birth_refuses_before_reading_notification(self):
        with self.assertRaises(CaseFailed):
            self.capture(changed=True)

    def test_refresh_walks_actual_known_ancestors(self):
        self.assertIs(shell.follow(self.app,self.expected,time.monotonic()+1),self.banner)
        chain,nodes=shell.ancestry(self.banner,self.app)
        self.assertEqual(chain,self.expected)
        self.assertEqual(nodes,[self.banner,self.panel,self.app])

    def test_replaced_banner_is_not_a_new_selection(self):
        self.panel.children=[]
        Node('replacement',self.panel)
        with self.assertRaises(CaseUnavailable):shell.follow(self.app,self.expected,time.monotonic()+1)

    def test_replaced_shell_application_is_terminal(self):
        self.app.path+='_replacement'
        with self.assertRaises(CaseFailed):shell.follow(self.app,self.expected,time.monotonic()+1)

    def test_no_ancestry_read_after_the_shared_deadline(self):
        with self.assertRaises(CaseUnavailable):shell.follow(self.app,self.expected,time.monotonic()-1)

    def test_null_live_application_refuses_refresh_without_reusing_metadata(self):
        self.banner.app=None
        with self.assertRaises(accessibility.InventoryChanged):shell.follow(self.app,self.expected,time.monotonic()+1)

    def test_rechecks_every_parent_link_before_publishing_chain(self):
        original=self.banner.get_parent
        calls=0
        def changed_parent():
            nonlocal calls
            calls+=1
            return original() if calls==1 else self.app
        with patch.object(self.banner,'get_parent',side_effect=changed_parent):
            with self.assertRaises(accessibility.InventoryChanged):shell.ancestry(self.banner,self.app)


if __name__=='__main__':unittest.main()
