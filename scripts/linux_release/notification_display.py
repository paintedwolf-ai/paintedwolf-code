"""Exact GNOME46 plain-label projection of the product's canonical notification wire text."""
import hashlib
import re

from .installed_cases.records import CaseUnavailable

SERVER = ['gnome-shell', 'GNOME', '46.0', '1.2']
_ENTITIES = {'&amp;': '&', '&lt;': '<', '&gt;': '>', '&quot;': '"', '&apos;': "'"}
_ESCAPE = str.maketrans({value: key for key, value in _ENTITIES.items()})
_HASH = re.compile(r'[a-f0-9]{64}')
_UNSUPPORTED = re.compile(r'[\x00-\x08\x0b-\x1f\x7f-\x84\x86-\x9f]')


def capabilities(values):
    if (not isinstance(values, (list, tuple)) or len(values) > 64
            or any(not isinstance(item, str) or len(item.encode()) > 128 for item in values)):
        raise ValueError('notification capabilities exceed the native typed contract')
    return {key: key in values for key in ('body', 'body-markup', 'actions')}


def capability_proof(value):
    if (not isinstance(value, dict) or set(value) != {'request_serial', 'request_sequence', 'reply_sequence',
            'body', 'body-markup', 'actions'}
            or any(type(value[key]) is not int or not 0 < value[key] <= maximum for key, maximum in
                (('request_serial', (1 << 32) - 1), ('request_sequence', 64), ('reply_sequence', 64)))
            or value['request_sequence'] >= value['reply_sequence']
            or any(type(value[key]) is not bool for key in ('body', 'body-markup', 'actions'))):
        raise ValueError('notification capability exchange has an invalid private proof')
    if not value['body'] or not value['actions']:
        raise CaseUnavailable('captured-notification-capabilities-do-not-support-body-and-action')
    return value


def display_body(wire, markup):
    if markup:
        plain = re.sub(r'&(?:amp|lt|gt|quot|apos);', lambda match: _ENTITIES[match[0]], wire)
        if plain.translate(_ESCAPE) != wire:
            raise CaseUnavailable('notification-body-is-not-canonical-escaped-plain-text')
    else:
        plain = wire
    return plain.replace('\n', ' ')


def digests(title, body, captured):
    capability_proof(captured)
    # Numeric control references and CR normalization are outside this exact display variant.
    if _UNSUPPORTED.search(title) or _UNSUPPORTED.search(body):
        raise CaseUnavailable('notification-text-exceeds-the-declared-display-variant')
    values = {'wire_title_sha256': title, 'wire_body_sha256': body,
        'display_title_sha256': title.replace('\n', ' '),
        'display_body_sha256': display_body(body, captured['body-markup'])}
    return {key: hashlib.sha256(value.encode()).hexdigest() for key, value in values.items()}


def validate(delivery):
    capability_proof(delivery['capabilities'])
    if (delivery['server'] != SERVER or delivery['capabilities']['reply_sequence'] >= delivery['request_sequence']
            or delivery['capabilities']['request_serial'] == delivery['request_serial']
            or any(not isinstance(delivery[key], str) or not _HASH.fullmatch(delivery[key]) for key in
                ('wire_title_sha256', 'wire_body_sha256', 'display_title_sha256', 'display_body_sha256'))):
        raise ValueError('notification display proof has no captured declared renderer contract')


def server_information(reader, destination, deadline):
    result = reader.call('/org/freedesktop/Notifications', 'org.freedesktop.Notifications',
        'GetServerInformation', None, '(ssss)', user=True, destination=destination, deadline=deadline)
    if result.get_type_string() != '(ssss)' or list(result.unpack()) != SERVER:
        raise CaseUnavailable('notification-renderer-is-not-the-declared-GNOME46-variant')
    return list(SERVER)
