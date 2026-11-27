"""HTTP transport protocol and stdlib urllib implementation for D1 sidecar /v1 endpoints."""

from __future__ import annotations

import json
from typing import Protocol
import urllib.request


class SidecarTransport(Protocol):
    """Minimal transport interface enabling isolated unit tests without live sidecars."""

    def request(
        self,
        path: str,
        body: dict[str, object] | None = None,
        *,
        method: str | None = None,
    ) -> dict[str, object]: ...

    def request_raw(
        self,
        path: str,
        body: dict[str, object] | None = None,
        *,
        method: str | None = None,
    ) -> tuple[bytes, dict[str, str]]: ...


class HTTPTransport:
    """Stdlib urllib transport for live D1 sidecar /v1 endpoints (Bearer auth)."""

    def __init__(self, base_url: str, token: str) -> None:
        self._base_url = base_url.rstrip("/")
        self._token = token

    def _build_req(
        self,
        path: str,
        body: dict[str, object] | None,
        method: str | None,
    ) -> urllib.request.Request:
        data = json.dumps(body).encode("utf-8") if body is not None else None
        headers = {
            "Authorization": f"Bearer {self._token}",
            "Accept": "application/json",
        }
        if data is not None:
            headers["Content-Type"] = "application/json"
        effective_method = method or ("POST" if body is not None else "GET")
        return urllib.request.Request(
            f"{self._base_url}{path}",
            data=data,
            headers=headers,
            method=effective_method,
        )

    def request(
        self,
        path: str,
        body: dict[str, object] | None = None,
        *,
        method: str | None = None,
    ) -> dict[str, object]:
        raw, _ = self.request_raw(path, body, method=method)
        return json.loads(raw.decode("utf-8")) if raw else {}

    def request_raw(
        self,
        path: str,
        body: dict[str, object] | None = None,
        *,
        method: str | None = None,
    ) -> tuple[bytes, dict[str, str]]:
        req = self._build_req(path, body, method)
        with urllib.request.urlopen(req) as resp:
            headers = {k.lower(): v for k, v in resp.headers.items()}
            return resp.read(), headers
