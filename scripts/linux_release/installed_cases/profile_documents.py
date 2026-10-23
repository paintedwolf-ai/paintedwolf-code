"""Read bounded private physical configuration and reject ambiguous YAML documents."""
from __future__ import annotations
import os
import stat
from .package_payload import stable
from .records import CaseUnavailable

LIMIT = 1 << 20


def read_private(path):
    if not os.path.lexists(path):
        return None
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(descriptor, 'rb') as stream:
        before = os.fstat(stream.fileno())
        if (not stat.S_ISREG(before.st_mode) or before.st_uid != os.getuid()
                or before.st_mode & 0o077 or before.st_size > LIMIT or path.resolve(strict=True) != path):
            raise ValueError('owned configuration requires private physical storage')
        raw = stream.read(LIMIT + 1)
        if stable(before) != stable(os.fstat(stream.fileno())) or stable(before) != stable(path.lstat()):
            raise CaseUnavailable('owned-configuration-changed-during-observation')
    return raw


def yaml_document(raw: bytes):
    try:
        import yaml
    except ImportError:
        raise CaseUnavailable('bounded-provider-YAML-observer-unavailable') from None
    if len(raw) > LIMIT:
        raise ValueError('owned provider catalog exceeds its fixed bound')
    try:
        root = yaml.compose(raw, Loader=yaml.SafeLoader)
        pending, seen = [(root, 0)], set()
        while pending:
            node, depth = pending.pop()
            if node is None or id(node) in seen or depth > 32 or len(seen) >= 8192:
                raise ValueError('provider catalog has aliases or exceeds its node bound')
            seen.add(id(node))
            if isinstance(node, yaml.MappingNode):
                keys = set()
                for key, value in node.value:
                    if key.tag != 'tag:yaml.org,2002:str' or len(key.value) > 128 or key.value in keys:
                        raise ValueError('provider catalog has ambiguous mapping keys')
                    keys.add(key.value)
                    pending.extend(((key, depth + 1), (value, depth + 1)))
            elif isinstance(node, yaml.SequenceNode):
                pending.extend((value, depth + 1) for value in node.value)
            elif not isinstance(node, yaml.ScalarNode) or len(node.value) > 4096:
                raise ValueError('provider catalog scalar exceeds its bound')
        value = yaml.safe_load(raw)
    except (yaml.YAMLError, RecursionError, UnicodeError):
        raise ValueError('owned provider catalog has an unsupported YAML shape') from None
    if not isinstance(value, dict):
        raise ValueError('owned configuration requires a mapping document')
    return value
