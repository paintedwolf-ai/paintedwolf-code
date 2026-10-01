"""Retry application admission limits without replaying ambiguous mutations."""
import datetime
import email.utils
import re
import time
import urllib.error
import urllib.request


def retry_delay(value, fallback):
    if value:
        value = value.strip()
        if re.fullmatch(r'[0-9]+', value):
            return max(1, int(value))
        try:
            deadline = email.utils.parsedate_to_datetime(value)
            if deadline.tzinfo is not None:
                return max(1, (deadline - datetime.datetime.now(datetime.timezone.utc)).total_seconds())
        except (ValueError, TypeError, OverflowError):
            pass
    return fallback


def open_request(request):
    delay = 1
    while True:
        try:
            return urllib.request.urlopen(request, timeout=None)
        except urllib.error.HTTPError as error:
            if error.code != 429:
                raise
            # The application's admission middleware rejects before executing the route.
            wait = retry_delay(error.headers.get('Retry-After'), delay)
            error.close()
            time.sleep(wait)
            delay = min(60, delay * 2)
