"""Normal accessible onboarding never resets an unknown vault or retains its password."""
from contextlib import nullcontext
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from linux_release.installed_cases import onboarding, desktop_ui
from linux_release.installed_cases.records import CaseUnavailable

API = SimpleNamespace(ROLE_PASSWORD_TEXT=1, ROLE_PUSH_BUTTON=2, ROLE_FRAME=3, ROLE_DIALOG=4,
                      STATE_SHOWING=5, STATE_ENABLED=6)
OWNER = (1000, '/home/owned-test-user')
IDENTITY = {'pid': 123, 'parent_pid': 100, 'start_time_ticks': 42}


class Node:
    def __init__(self, role, name='', children=(), enabled=True):
        self.role, self.name, self.children, self.enabled = role, name, list(children), enabled
        self.editor = SimpleNamespace(setTextContents=Mock(return_value=True))
        self.action = SimpleNamespace(nActions=1, doAction=Mock(return_value=True))
    @property
    def childCount(self):
        return len(self.children)
    def __getitem__(self, index):
        return self.children[index]
    def getRole(self):
        return self.role
    def getState(self):
        return SimpleNamespace(contains=lambda state: state == API.STATE_SHOWING or self.enabled)
    def queryEditableText(self):
        return self.editor
    def queryAction(self):
        return self.action


def form(create=True):
    fields = [Node(API.ROLE_PASSWORD_TEXT, 'App password')]
    if create:
        fields.append(Node(API.ROLE_PASSWORD_TEXT, 'Confirm app password'))
    button = Node(API.ROLE_PUSH_BUTTON, 'Create vault' if create else 'Unlock vault')
    return Node(API.ROLE_FRAME, children=[*fields, button]), fields, button


class InstalledOnboardingTests(unittest.TestCase):
    def setUp(self):
        self.password = patch.object(onboarding, '_password', None)
        self.owner = patch.object(onboarding, '_password_owner', None)
        identity = patch.object(onboarding, 'process_identity', return_value=IDENTITY)
        identity.start()
        self.addCleanup(identity.stop)
        self.password.start()
        self.owner.start()
        self.addCleanup(self.password.stop)
        self.addCleanup(self.owner.stop)

    def test_create_then_unlock_only_campaign_owned_password_through_normal_interfaces(self):
        for create in (True, False):
            window, fields, button = form(create)
            selected = onboarding.controls(window, API)
            facts = onboarding.complete((123, IDENTITY, selected), OWNER)
            self.assertEqual(facts['mode'], 'create' if create else 'unlock')
            self.assertFalse(facts['password_in_evidence'])
            self.assertNotIn(onboarding._password, repr(facts))
            for field in fields:
                field.editor.setTextContents.assert_called_once_with(onboarding._password)
            button.action.doAction.assert_called_once_with(0)

    def test_unknown_existing_password_vault_is_never_modified_or_reset(self):
        window, fields, button = form(False)
        with self.assertRaises(CaseUnavailable):
            onboarding.complete((123, IDENTITY, onboarding.controls(window, API)), OWNER)
        fields[0].editor.setTextContents.assert_not_called()
        button.action.doAction.assert_not_called()

    def test_ambiguous_controls_and_disabled_inputs_do_not_select_a_form(self):
        window, fields, _ = form()
        window.children.append(fields[0])
        with self.assertRaises(CaseUnavailable):
            onboarding.controls(window, API)
        window, fields, _ = form()
        fields[1].enabled = False
        with self.assertRaises(CaseUnavailable):
            onboarding.controls(window, API)
        self.assertIsNone(onboarding.controls(Node(API.ROLE_FRAME), API))

    def test_toolkit_error_cannot_copy_password_into_observations_or_exception(self):
        window, fields, _ = form()
        fields[0].editor.setTextContents.side_effect = RuntimeError('sensitive toolkit diagnostic')
        with self.assertRaises(CaseUnavailable) as error:
            onboarding.complete((123, IDENTITY, onboarding.controls(window, API)), OWNER)
        self.assertNotIn('sensitive', str(error.exception))
        self.assertNotIn(onboarding._password, str(error.exception))

    def test_changing_native_inventory_retries_but_does_not_establish_readiness(self):
        readiness = Mock(side_effect=[False, True])
        app = SimpleNamespace(timeout=1, observations={'session': {}}, try_ready=readiness)
        with patch.object(onboarding, 'accessibility_bus', return_value=nullcontext()), patch.object(
                onboarding, 'load_api', return_value=API), patch.object(
                onboarding, 'form', side_effect=IndexError('private native payload')), patch.object(onboarding.time, 'sleep'):
            onboarding.onboard(app)
        self.assertEqual(app.observations['native_accessibility_inventory_retries'], 1)
        self.assertEqual(readiness.call_count, 2)
        self.assertNotIn('private', repr(app.observations))
        window, _, _ = form()
        with self.assertRaises(CaseUnavailable):
            onboarding.controls(window, API, deadline=0)

    def test_structured_native_inventory_error_retries_before_owned_public_readiness(self):
        error = RuntimeError('private native message')
        error.domain, error.code = 'atspi_error', 0
        app = SimpleNamespace(timeout=1, observations={'session': {}}, try_ready=Mock(side_effect=[False, True]))
        with patch.object(onboarding, 'accessibility_bus', return_value=nullcontext()), patch.object(
                onboarding, 'load_api', return_value=API), patch.object(
                onboarding, 'form', side_effect=error), patch.object(onboarding.time, 'sleep'):
            onboarding.onboard(app)
        self.assertEqual(app.observations['native_accessibility_inventory_retries'], 1)
        self.assertEqual(app.observations['native_accessibility_last_retry']['domain'], 'atspi_error')
        self.assertNotIn('private', repr(app.observations))

    def test_inventory_failure_retains_only_phase_and_exception_class(self):
        app = SimpleNamespace(timeout=1, observations={'session': {}}, try_ready=lambda: False)
        with patch.object(onboarding, 'accessibility_bus', return_value=nullcontext()), patch.object(
                onboarding, 'load_api', return_value=API), patch.object(
                onboarding, 'form', side_effect=RuntimeError('private toolkit payload')):
            with self.assertRaises(CaseUnavailable) as refusal:
                onboarding.onboard(app)
        facts = app.observations['native_accessibility_error']
        self.assertEqual((facts['phase'], facts['exception_type']), ('vault-form-inventory', 'RuntimeError'))
        self.assertTrue(0 < len(facts['stack']) <= 8)
        self.assertTrue(all(set(frame) == {'file', 'function', 'line'} for frame in facts['stack']))
        self.assertNotIn('private', repr(app.observations) + str(refusal.exception))

    def test_native_error_metadata_excludes_message_locals_and_unbounded_identifiers(self):
        error = RuntimeError('private input must never be retained')
        error.domain, error.code = 'g-dbus-error-quark', 19
        facts = onboarding.native_error(error)
        self.assertEqual((facts['domain'], facts['code']), ('g-dbus-error-quark', 19))
        self.assertNotIn('private', repr(facts))
        error.domain, error.code = 'private input', True
        facts = onboarding.native_error(error)
        self.assertNotIn('domain', facts)
        self.assertNotIn('code', facts)

    def test_foreign_or_reused_application_pid_is_never_an_owned_window(self):
        app = SimpleNamespace(observations={'launch_identity': IDENTITY})
        with patch.object(desktop_ui, 'process_identity', return_value=dict(IDENTITY, start_time_ticks=43)):
            self.assertFalse(desktop_ui.owned_process(123, app))
        with patch.object(desktop_ui, 'process_identity', side_effect=[
                {'pid': 200, 'parent_pid': 123, 'start_time_ticks': 7}, IDENTITY]):
            self.assertTrue(desktop_ui.owned_process(200, app))

    def test_service_custody_ready_path_needs_no_accessible_form_or_password(self):
        app = SimpleNamespace(timeout=1, observations={'session': {}},
                              process=SimpleNamespace(poll=lambda: None), try_ready=lambda: True)
        with patch.object(onboarding, 'accessibility_bus', return_value=nullcontext()), \
                patch.object(onboarding, 'form') as inspect:
            onboarding.onboard(app)
            inspect.assert_not_called()
            self.assertIsNone(onboarding._password)

    def test_owned_readiness_ends_startup_inventory_without_touching_a_form(self):
        window, fields, button = form()
        application = Node(0, children=[window])
        application.get_process_id = lambda: 123
        api = SimpleNamespace(**vars(API), Registry=SimpleNamespace(getDesktop=lambda index: Node(0, children=[application])))
        app = SimpleNamespace(timeout=1, observations={'session': {}}, try_ready=Mock(side_effect=[False, True]))
        with patch.object(onboarding, 'accessibility_bus', return_value=nullcontext()), patch.object(
                onboarding, 'load_api', return_value=api), patch.object(onboarding, 'owned_process', return_value=True):
            onboarding.onboard(app)
        self.assertIsNone(onboarding._password)
        for field in fields:
            field.editor.setTextContents.assert_not_called()
        button.action.doAction.assert_not_called()

    def test_inventory_readiness_interrupt_preserves_traversal_bounds_when_not_ready(self):
        oversized = Node(0, children=[Node(0)] * 1025)
        def ready():
            raise onboarding._Ready()
        with self.assertRaises(onboarding._Ready):
            onboarding.controls(oversized, API, ready=ready)
        with self.assertRaisesRegex(ValueError, 'traversal bound'):
            onboarding.controls(oversized, API, ready=lambda: None)
