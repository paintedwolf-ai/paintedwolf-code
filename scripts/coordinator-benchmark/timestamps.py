"""Exact protocol timestamps; preserve all RFC3339 nanoseconds."""
from datetime import datetime
from decimal import Decimal
import re


def instant(value):
    match=re.fullmatch(r'(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})',value)
    if not match: raise ValueError('invalid application timestamp')
    whole=datetime.fromisoformat(match[1]+match[3].replace('Z','+00:00'))
    return Decimal(int(whole.timestamp()))+Decimal('0.'+(match[2] or '0'))

