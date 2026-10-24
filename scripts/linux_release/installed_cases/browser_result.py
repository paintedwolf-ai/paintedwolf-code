"""Validate only the fixed owned page result from a durable invoked browser receipt."""
from __future__ import annotations

from .profile_observer import identifier
from .provider_acquisition import fixed_call, host_call_id


def opened(result):
    row = result.get('tool_invocation') if isinstance(result, dict) else None
    if (not isinstance(row, dict) or row.get('tool') != 'page_open' or row.get('owner') != 'browser'
            or row.get('invoked') is not True or row.get('status') != 'completed'
            or row.get('arguments') != fixed_call('page_open')['arguments']):
        raise ValueError('browser fixture has no exact successful durable page invocation')
    identifier(row.get('message_id'))
    identifier(row.get('invocation_id'))
    host_call_id(row.get('tool_call_id'))
    document = row.get('payload')
    if not isinstance(document, dict):
        raise ValueError('browser fixture has no typed opened-page payload')
    page, live, maximum = document.get('id'), document.get('live_pages'), document.get('max_pages')
    valid_id = lambda value: isinstance(value, str) and 0 < len(value) <= 128 and not any(ord(c) < 32 for c in value)
    if (not valid_id(page) or not isinstance(live, list) or not 1 <= len(live) <= 64
            or not all(valid_id(value) for value in live) or len(set(live)) != len(live) or page not in live
            or type(maximum) is not int or not len(live) <= maximum <= 64):
        raise ValueError('browser fixture did not retain its actual bounded live page')
    return {'invocation': {key: row[key] for key in
                ('message_id', 'invocation_id', 'tool_call_id', 'tool', 'owner', 'status', 'invoked', 'arguments')},
            'page': {'id': page, 'live_pages': list(live), 'max_pages': maximum}}
