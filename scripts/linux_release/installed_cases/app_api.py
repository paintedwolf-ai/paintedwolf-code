"""Use only the captured installed engine's loopback API token."""
from __future__ import annotations

import json
import urllib.error
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, target):
        raise ValueError("installed engine API redirected outside its captured endpoint")


class EngineAPI:
    def __init__(self, port: int, token: str):
        if type(port) is not int or not 0 < port < 65536 or not token or any(c.isspace() for c in token):
            raise ValueError("installed engine endpoint is incomplete")
        self._origin = f"http://127.0.0.1:{port}"
        self._token = token
        self._opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

    def request(self, method: str, path: str, payload=None) -> dict:
        parsed = urllib.parse.urlsplit(path)
        if (method not in {"GET", "POST", "PUT", "PATCH", "DELETE"} or parsed.scheme or parsed.netloc
                or parsed.fragment or not (parsed.path == "/health" or parsed.path.startswith("/v1/"))):
            raise ValueError("installed API request must use a fixed local product route")
        body = None if payload is None else json.dumps(payload).encode()
        if body is not None and len(body) > 1 << 20:
            raise ValueError("installed API request exceeds its fixture bound")
        request = urllib.request.Request(self._origin + path, data=body, method=method,
            headers={"Authorization": "Bearer " + self._token, "Content-Type": "application/json"})
        try:
            response = self._opener.open(request, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read((8 << 20) + 1)
            if len(raw) > 8 << 20:
                raise ValueError("installed API response exceeds its bound")
            return {"status": response.code, "body": json.loads(raw)}
