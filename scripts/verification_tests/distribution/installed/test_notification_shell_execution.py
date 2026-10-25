"""Pre-dispatch refresh follows the pinned shell ancestry and cannot re-select a replacement banner."""
import time
import unittest
from types import SimpleNamespace
from unittest.mock import patch

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
