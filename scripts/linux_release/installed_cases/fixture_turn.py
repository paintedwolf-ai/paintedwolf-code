"""Bind a normal fixed GUI turn to its durable reply and exact acquired tool receipts."""
from __future__ import annotations

import time

from .profile_observer import project_for_owned_root, sessions_for_project
from .provider import MODEL
from .provider_acquisition import fixed_call
from .reply_observer import committed_reply
from .fixture_receipt import committed_fixture_tool
from .records import CaseUnavailable


def await_fixture_reply(app, project, provider_id, acquisition, *, selected_session=None):
    target = acquisition.tool
    fixed_call(target)
    deadline = time.monotonic() + 90
    while time.monotonic() < deadline:
        witness = project_for_owned_root(app, project)
        if witness:
            sessions = [row for row in sessions_for_project(app, witness['project_id'])
                        if row['provider_id'] == provider_id and row['model'] == MODEL]
            if selected_session is not None:
                sessions = [row for row in sessions if row['id'] == selected_session]
            if len(sessions) > 1:
                raise ValueError('fixed reply project has ambiguous session identity')
            if sessions:
                session = sessions[0]
                if session['status'] == 'error':
                    raise ValueError('protected mock turn failed before its durable reply')
                result = committed_reply(app, witness['project_id'], session['id'], provider_id)
                if result is not None:
                    if not acquisition.completed or acquisition.witness is None or acquisition.tool_witness is None:
                        raise ValueError('fixed provider phases did not establish both host call identities')
                    acquired = committed_fixture_tool(app, session['id'], 'request_tools', acquisition.witness['tool_call_id'], target=target)
                    invoked = committed_fixture_tool(app, session['id'], target, acquisition.tool_witness['tool_call_id'], target=target)
                    if acquired is not None and acquired['invocation_id'] != acquisition.witness['invocation_id']:
                        raise ValueError('acquisition durable receipt differs from the projected receipt')
                    if invoked is not None and invoked['invocation_id'] != acquisition.tool_witness['invocation_id']:
                        raise ValueError('tool durable receipt differs from the projected receipt')
                    if acquired is None or invoked is None:
                        raise ValueError('fixed acquisition and tool fixture did not complete successfully')
                    return {**result, 'tool_message_id': invoked['message_id'],
                            'schema_acquisition': acquired, 'tool_invocation': invoked}
        time.sleep(0.1)
    raise CaseUnavailable('protected-mock-reply-or-restart-witness-timeout')

