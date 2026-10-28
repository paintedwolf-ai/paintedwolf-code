"""Pre-dispatch refresh follows the pinned shell ancestry and cannot re-select a replacement banner."""
import time
import hashlib
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


class ShellInventoryFactsTests(unittest.TestCase):
    def setUp(self):
        self.api = SimpleNamespace(ROLE_NOTIFICATION=1, ROLE_LABEL=2, STATE_DEFUNCT=10,
            STATE_SHOWING=11, STATE_VISIBLE=12, STATE_ENABLED=13, STATE_SENSITIVE=14, DESKTOP_COORDS=0)
        self.desktop = Node('desktop')
        self.app = self.node('app', self.desktop)
        self.api.Registry = SimpleNamespace(getDesktop=lambda _: self.desktop)
        self.delivery = {key: hashlib.sha256(value.encode()).hexdigest()
                        for key, value in [('title_sha256', 'public title'), ('body_sha256', 'public body')]}
        self.deadline = time.monotonic() + 10

    def node(self, path, parent, role=0, name='private text must not escape', states=(11, 12, 13, 14)):
        node = Node(path, parent)
        node.name = name
        node.getRole = MagicMock(return_value=role)
        node.getState = lambda: SimpleNamespace(contains=lambda state: state in states)
        node.queryComponent = lambda: SimpleNamespace(getExtents=lambda _: [0, 0, 100, 100])
        node.queryAction = MagicMock()
        return node

    def banner(self, path='banner', *, matched=True, states=(11, 12, 13, 14)):
        banner = self.node(path, self.app, 1, states=states)
        self.node(path+'-title', banner, 2, name='public title' if matched else 'private unmatched title')
        self.node(path+'-body', banner, 2, name='public body' if matched else 'private unmatched body')
        return banner

    def capture(self):
        return shell.capture_target(self.api, {'pid': 200}, self.delivery,
                                    {'logical_extent': [0, 0, 100, 100]}, self.deadline)

    def refusal(self):
        with self.assertRaises(CaseUnavailable) as refusal:
            self.capture()
        facts = refusal.exception.observations['notification_shell_observation']
        self.assertNotIn('private', repr(facts))
        self.assertNotIn('public title', repr(facts))
        self.assertNotIn('sha256', repr(facts))
        self.assertFalse(facts['target_verified'])
        return facts

    def test_complete_empty_and_ambiguous_snapshot_do_not_return_click_authority(self):
        empty = self.refusal()
        self.assertTrue(empty['notification_inventory_complete'])
        self.assertEqual(empty['match_count'], 0)
        first, second = self.banner(), self.banner('second')
        ambiguous = self.refusal()
        self.assertTrue(ambiguous['notification_inventory_complete'])
        self.assertEqual(ambiguous['match_count'], 2)
        self.assertTrue(all(row['ancestry_verified'] and row['label_ancestry_verified']
                            for row in ambiguous['candidates']))
        first.queryAction.assert_not_called()
        second.queryAction.assert_not_called()

    def test_state_and_exact_label_multiplicity_facts_never_export_other_notification_text(self):
        self.banner('hidden', states=(12, 13, 14))
        self.banner('defunct', states=(10, 11, 12, 13, 14))
        row = self.banner(matched=False)
        facts = self.refusal()
        self.assertEqual((facts['notification_count'], facts['defunct_count'], facts['not_showing_count']), (3, 1, 1))
        self.assertEqual(facts['candidates'][0]['title_matches'], 0)
        self.assertEqual(facts['candidates'][0]['body_matches'], 0)
        row.queryAction.assert_not_called()

    def test_partial_snapshot_remains_unverified_even_after_one_matching_banner(self):
        banner = self.banner()
        original = shell.accessibility.descendants
        def descendants(node, deadline):
            yield from original(node, deadline)
            if node is self.app:
                raise CaseUnavailable('partial inventory')
        with patch.object(shell.accessibility, 'descendants', side_effect=descendants):
            facts = self.refusal()
        self.assertFalse(facts['notification_inventory_complete'])
        self.assertTrue(facts['unverified'])
        self.assertEqual(facts['match_count'], 1)
        banner.queryAction.assert_not_called()

    def test_interrupted_label_traversal_preserves_only_observed_matching_counts(self):
        banner = self.banner()
        original = shell.accessibility.descendants
        def descendants(node, deadline):
            if node is banner:
                yield banner.children[0]
                raise CaseUnavailable('partial labels')
            yield from original(node, deadline)
        with patch.object(shell.accessibility, 'descendants', side_effect=descendants):
            facts = self.refusal()
        candidate = facts['candidates'][0]
        self.assertFalse(candidate['labels_complete'])
        self.assertEqual((candidate['label_count'], candidate['title_matches'], candidate['body_matches']), (1, 1, 0))
        self.assertTrue(facts['unverified'])
        banner.queryAction.assert_not_called()

    def test_diagnostic_clipping_cannot_turn_empty_matches_into_a_target(self):
        for index in range(17):
            self.banner(str(index), matched=False)
        facts = self.refusal()
        self.assertTrue(facts['notification_inventory_complete'])
        self.assertTrue(facts['clipped'])
        self.assertTrue(facts['unverified'])
        self.assertEqual(len(facts['candidates']), 16)


if __name__=='__main__':unittest.main()
