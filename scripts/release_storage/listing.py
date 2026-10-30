"""Bounded authenticated enumeration of immediate retained object prefixes."""
import os
from pathlib import PurePosixPath
import urllib.parse


def prefixes(prefix):
    if not isinstance(prefix, str):
        raise ValueError("retained prefix must be a string")
    path = PurePosixPath(prefix)
    if (not prefix.endswith('/') or path.is_absolute()
            or path.as_posix() != prefix.rstrip('/') or '..' in path.parts):
        raise ValueError('retained prefix must be a normalized relative directory')
    cursor, cursors, seen = None, set(), set()
    for _ in range(64):
        if os.environ.get('RELEASE_R2_TRANSPORT') == 's3':
            from .s3 import request
            arguments = ['--prefix', prefix, '--delimiter', '/', '--max-keys', '1000', '--no-paginate']
            if cursor is not None:
                arguments += ['--continuation-token', cursor]
            result = request('list-objects-v2', *arguments)
            if not isinstance(result, dict):
                raise ValueError('retained storage listing has an invalid shape')
            rows = result.get('CommonPrefixes', [])
            children = [row.get('Prefix') for row in rows] if isinstance(rows, list) and all(isinstance(row, dict) for row in rows) else None
            truncated, next_cursor = result.get('IsTruncated', False), result.get('NextContinuationToken')
        else:
            from release_distribution import storage_json
            account, bucket, token = (os.environ[name] for name in ('CLOUDFLARE_ACCOUNT_ID', 'R2_BUCKET', 'CLOUDFLARE_API_TOKEN'))
            query = {'prefix': prefix, 'delimiter': '/', 'per_page': 1000}
            if cursor is not None:
                query['cursor'] = cursor
            base = f'https://api.cloudflare.com/client/v4/accounts/{account}/r2/buckets/{bucket}/objects'
            _, result = storage_json(base + '?' + urllib.parse.urlencode(query), token)
            if not isinstance(result, dict) or result.get('success') is not True or not isinstance(result.get('result_info', {}), dict):
                raise ValueError('retained storage listing failed')
            info = result.get('result_info', {})
            children = info.get('delimited', [])
            truncated, next_cursor = info.get('is_truncated', False), info.get('cursor')
        if (not isinstance(children, list) or len(children) > 1000 or type(truncated) is not bool
                or any(not isinstance(child, str) for child in children)):
            raise ValueError('retained storage listing has an invalid shape')
        for child in children:
            suffix = child.removeprefix(prefix).removesuffix('/')
            if (not child.startswith(prefix) or not child.endswith('/') or not suffix or '/' in suffix
                    or suffix in {'.', '..'} or child in seen):
                raise ValueError('retained storage listing repeated or escaped its prefix')
            seen.add(child)
            yield child
        if not truncated:
            return
        if not isinstance(next_cursor, str) or not next_cursor or next_cursor in cursors:
            raise ValueError('retained storage listing has an invalid continuation token')
        cursor = next_cursor
        cursors.add(cursor)
    raise ValueError('retained storage listing exceeds its page bound')
