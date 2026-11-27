"""Sidecar HTTP driver targeting exact D1 /v1 catalog, feed, search, replace, and agent routes."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any, Callable, Protocol
import urllib.parse
import urllib.request
import uuid

from .checker import ProductCatalogAnswer, ProductSearchAnswer
from .checker_replace import ProductReplaceAnswer
from .driver_adapters import (
    AgentDiscoveryObservation,
    map_agent_tool_result,
    map_browse_entries_to_answer,
    map_catalog_index_matches_to_answer,
    map_replace_apply_to_answer,
    map_search_response_to_answer,
)
from .driver_provenance import file_sha256


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


class SidecarDriver:
    """Typed D1/D2 sidecar driver for catalog, browse, feed, search, replace, and agent discovery."""

    def __init__(self, transport: SidecarTransport) -> None:
        self._transport = transport

    def attach_project(self, root_paths: list[Path]) -> tuple[str, list[str]]:
        payload = {"roots": [{"path": str(Path(p).resolve())} for p in root_paths]}
        resp = self._transport.request("/v1/projects", payload, method="POST")
        project_id = str(resp["id"])
        roots = [str(r["id"]) for r in (resp.get("roots") or []) if isinstance(r, dict) and r.get("id")]
        return project_id, roots

    def get_source_index(self, project_id: str) -> dict[str, object]:
        return self._transport.request(f"/v1/projects/{project_id}/source/index", method="GET")

    def browse_dir(self, project_id: str, root_id: str, dir_path: str = ".") -> dict[str, object]:
        qs = urllib.parse.urlencode({"root_id": root_id, "dir": dir_path})
        return self._transport.request(f"/v1/projects/{project_id}/source/browse?{qs}", method="GET")

    def walk_browse(
        self,
        project_id: str,
        root_ids: list[str],
        *,
        max_depth: int = 32,
    ) -> ProductCatalogAnswer:
        """Traverse `/source/browse` using exact D1 `SourceDirEntry` (`name`, `is_dir`).

        Explicitly returns `projection="browse"` (editor navigation semantics).
        """
        index_summary = self.get_source_index(project_id)
        rev = int(index_summary["revision"]) if index_summary.get("revision") is not None else None
        entries_by_root: dict[str, list[dict[str, object]]] = {}

        for root_id in root_ids:
            collected: list[dict[str, object]] = []
            queue: list[tuple[str, int]] = [(".", 0)]
            while queue:
                cur_dir, depth = queue.pop(0)
                listing = self.browse_dir(project_id, root_id, cur_dir)
                for entry in listing.get("entries") or []:
                    if not isinstance(entry, dict):
                        continue
                    name = str(entry.get("name") or "")
                    if not name:
                        continue
                    rel = name if cur_dir in ("", ".") else f"{cur_dir}/{name}"
                    is_dir = bool(entry.get("is_dir"))
                    collected.append({"name": name, "path": rel, "is_dir": is_dir})
                    if is_dir and depth < max_depth:
                        queue.append((rel, depth + 1))
            entries_by_root[root_id] = collected

        coverage = index_summary.get("coverage")
        return map_browse_entries_to_answer(
            entries_by_root,
            coverage if isinstance(coverage, list) else [],
            generation_revision=rev,
        )

    def walk_catalog_index(
        self,
        project_id: str,
        root_id: str,
        *,
        page_limit: int = 200,
    ) -> ProductCatalogAnswer:
        """Read settled catalog-backed file membership via paginated `/source/search` + `/source/index`."""
        all_matches: list[dict[str, object]] = []
        cursor: str | None = None
        last_coverage: list[dict[str, object]] = []
        revision: int | None = None

        while True:
            resp = self.file_lookup(project_id, "", root_id=root_id, limit=page_limit, cursor=cursor)
            if resp.get("revision") is not None:
                revision = int(resp["revision"])
            cov = resp.get("coverage")
            if isinstance(cov, list):
                last_coverage = cov
            matches = resp.get("matches") or []
            if isinstance(matches, list):
                all_matches.extend(m for m in matches if isinstance(m, dict))
            next_cur = str(resp.get("next_cursor") or "")
            if not next_cur or next_cur == cursor:
                break
            cursor = next_cur

        return map_catalog_index_matches_to_answer(
            all_matches,
            last_coverage,
            generation_revision=revision,
        )

    def list_source_walk(
        self,
        project_id: str,
        *,
        baseline: str = "presentation",
        limit: int = 100,
        cursor: str | None = None,
    ) -> dict[str, object]:
        params: dict[str, object] = {"baseline": baseline, "limit": limit}
        if cursor:
            params["cursor"] = cursor
        qs = urllib.parse.urlencode(params)
        return self._transport.request(f"/v1/projects/{project_id}/source/walk?{qs}", method="GET")

    def file_lookup(
        self,
        project_id: str,
        query: str,
        *,
        root_id: str | None = None,
        limit: int = 50,
        cursor: str | None = None,
    ) -> dict[str, object]:
        params: dict[str, object] = {"q": query, "limit": limit}
        if root_id:
            params["root_id"] = root_id
        if cursor:
            params["cursor"] = cursor
        qs = urllib.parse.urlencode(params)
        return self._transport.request(f"/v1/projects/{project_id}/source/search?{qs}", method="GET")

    def search_raw(
        self,
        query: str,
        *,
        origin_project_id: str | None = None,
        regex: bool = False,
        case_sensitive: bool = False,
        whole_word: bool = False,
        include: tuple[str, ...] = (),
        exclude: tuple[str, ...] = (),
        limit: int = 200,
        cursor: str | None = None,
        budget: str | None = None,
    ) -> dict[str, object]:
        body: dict[str, object] = {
            "query": query,
            "regex": regex,
            "case_sensitive": case_sensitive,
            "whole_word": whole_word,
            "limit": limit,
        }
        if origin_project_id:
            body["origin_project_id"] = origin_project_id
        if include:
            body["include"] = list(include)
        if exclude:
            body["exclude"] = list(exclude)
        if cursor:
            body["cursor"] = cursor
        if budget:
            body["budget"] = budget
        return self._transport.request("/v1/search", body, method="POST")

    def search(
        self,
        query: str,
        *,
        default_root_id: str = "root-0",
        default_leg: str = "code",
        continuation_required: bool = False,
        **kwargs: Any,
    ) -> ProductSearchAnswer:
        resp = self.search_raw(query, **kwargs)
        return map_search_response_to_answer(
            resp,
            default_root_id=default_root_id,
            default_leg=default_leg,
            continuation_required=continuation_required,
        )

    def export_search(
        self,
        query: str,
        *,
        fmt: str = "jsonl",
        origin_project_id: str | None = None,
        regex: bool = False,
        case_sensitive: bool = False,
        whole_word: bool = False,
    ) -> tuple[str, bool]:
        body: dict[str, object] = {
            "query": query,
            "format": fmt,
            "regex": regex,
            "case_sensitive": case_sensitive,
            "whole_word": whole_word,
        }
        if origin_project_id:
            body["origin_project_id"] = origin_project_id
        raw, headers = self._transport.request_raw("/v1/search/export", body, method="POST")
        truncated = headers.get("x-export-truncated", "").lower() == "true"
        return raw.decode("utf-8"), truncated

    def replace_preview(
        self,
        query: str,
        replacement: str,
        *,
        origin_project_id: str | None = None,
        regex: bool = False,
        case_sensitive: bool = False,
        whole_word: bool = False,
        include: tuple[str, ...] = (),
        exclude: tuple[str, ...] = (),
    ) -> dict[str, object]:
        body: dict[str, object] = {
            "query": query,
            "replacement": replacement,
            "regex": regex,
            "case_sensitive": case_sensitive,
            "whole_word": whole_word,
        }
        if origin_project_id:
            body["origin_project_id"] = origin_project_id
        if include:
            body["include"] = list(include)
        if exclude:
            body["exclude"] = list(exclude)
        return self._transport.request("/v1/search/replace/preview", body, method="POST")

    def replace_apply(
        self,
        preview_response: dict[str, object],
        *,
        query: str,
        replacement: str,
        origin_project_id: str,
        root_dirs_by_id: dict[str, Path],
        regex: bool = False,
        case_sensitive: bool = False,
        whole_word: bool = False,
        drifted_keys: set[tuple[str, str]] | None = None,
        drifted_hashes: dict[tuple[str, str], str] | None = None,
        operation_id: str | None = None,
    ) -> ProductReplaceAnswer:
        apply_files: list[dict[str, object]] = []
        for pf in preview_response.get("files") or []:
            if not isinstance(pf, dict):
                continue
            hunks = pf.get("hunks") or []
            apply_files.append(
                {
                    "root_id": str(pf["root_id"]),
                    "path": str(pf["path"]),
                    "sha256": str(pf["sha256"]),
                    "hunks": list(range(len(hunks))),
                }
            )
        body: dict[str, object] = {
            "operation_id": operation_id or str(uuid.uuid4()),
            "query": query,
            "replacement": replacement,
            "origin_project_id": origin_project_id,
            "regex": regex,
            "case_sensitive": case_sensitive,
            "whole_word": whole_word,
            "files": apply_files,
        }
        apply_resp = self._transport.request("/v1/search/replace/apply", body, method="POST")
        disk_hashes: dict[tuple[str, str], str] = {}
        for item in apply_resp.get("files") or []:
            if not isinstance(item, dict):
                continue
            rid = str(item.get("root_id") or "root-0")
            rel = str(item.get("path") or "").removeprefix("./")
            base = root_dirs_by_id.get(rid)
            if base is not None and (base / rel).is_file():
                disk_hashes[(rid, rel)] = file_sha256(base / rel)
        return map_replace_apply_to_answer(
            apply_resp,
            preview_response,
            disk_hashes_by_key=disk_hashes,
            drifted_keys=drifted_keys,
            drifted_hashes=drifted_hashes,
        )

    def inject_editor_draft(
        self,
        project_id: str,
        root_id: str,
        rel_path: str,
        content: str,
        *,
        client_id: str = "cfs-driver",
        eol: str = "lf",
        mixed_eol: bool = False,
        history_vector: str | None = None,
        session_id: str | None = None,
    ) -> dict[str, object]:
        """Inject an editor draft with all required D1 PUT fields (`mixed_eol`, `history_vector`, `session_id`)."""
        opened = self._transport.request(
            f"/v1/projects/{project_id}/editor-documents",
            {"path": rel_path, "root_id": root_id, "client_id": client_id},
            method="POST",
        )
        return self._transport.request(
            f"/v1/projects/{project_id}/editor-documents/{opened['id']}",
            {
                "operation_id": str(uuid.uuid4()),
                "client_id": client_id,
                "expected_revision": opened["revision"],
                "content": content,
                "eol": eol,
                "mixed_eol": mixed_eol,
                "history_vector": history_vector,
                "session_id": session_id,
            },
            method="PUT",
        )

    def run_agent_discovery(
        self,
        tool_executor: Callable[[str, dict[str, object]], dict[str, object]],
        tool_name: str,
        args: dict[str, object],
    ) -> AgentDiscoveryObservation:
        raw = tool_executor(tool_name, args)
        return map_agent_tool_result(tool_name, raw)
