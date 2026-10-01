/**
 * Add-to-chat provenance for a selection, read only from stamped DOM attributes.
 * The body header prefix matches the host's `[User attached file: …]` hint shape.
 */

export const USER_SELECTED_TEXT_PREFIX = "[User selected text: " as const;

export type SelectionProvenance = {
  text: string;
  sessionId?: string;
  messageId?: string;
  toolCallId?: string;
  projectId?: string;
  path?: string;
  line?: number;
  rootId?: string;
  sourceRef?: string;
  hitKind?: string;
  artifactId?: string;
  handle?: string;
};

function attr(el: Element, name: string): string | undefined {
  const v = el.getAttribute(name)?.trim();
  return v ? v : undefined;
}

function closestAttr(from: Element, selector: string, name: string): string | undefined {
  const el = from.closest(selector);
  if (!el) return undefined;
  return attr(el, name);
}

/** Tool viewport rows use `{messageId}:{callId}` — keep the message UUID prefix. */
export function normalizeMessageId(raw: string | undefined): string | undefined {
  const id = raw?.trim();
  if (!id) return undefined;
  const colon = id.indexOf(":");
  if (colon > 0) return id.slice(0, colon);
  return id;
}

function firstToolCallId(raw: string | undefined): string | undefined {
  const v = raw?.trim();
  if (!v) return undefined;
  const first = v.split(/[\s,]+/).find((p) => p.length > 0);
  return first;
}

/**
 * Walk ancestors from the contextmenu target for stamped coords.
 * Prefer den-source-path over generic data-path.
 */
export function resolveSelectionProvenance(
  target: EventTarget | null,
  text: string,
): SelectionProvenance {
  const prov: SelectionProvenance = { text };
  if (!(target instanceof Element)) return prov;

  const stream = target.closest('[data-testid="chat-stream"]');
  if (stream) {
    const sid = attr(stream, "data-session-id");
    if (sid) prov.sessionId = sid;
  }

  const msgEl = target.closest("[data-msg-id]");
  if (msgEl) {
    const mid = normalizeMessageId(attr(msgEl, "data-msg-id"));
    if (mid) prov.messageId = mid;
    const tool = firstToolCallId(attr(msgEl, "data-tool-call-ids"));
    if (tool) prov.toolCallId = tool;
  }

  const project =
    closestAttr(target, "[data-project-id]", "data-project-id") ??
    (stream ? attr(stream, "data-project-id") : undefined);
  if (project) prov.projectId = project;

  const denPath = closestAttr(target, "[data-den-source-path]", "data-den-source-path");
  const dataPath = closestAttr(target, "[data-path]", "data-path");
  const path = denPath ?? dataPath;
  if (path) prov.path = path;

  const lineRaw =
    closestAttr(target, "[data-den-source-line]", "data-den-source-line") ??
    closestAttr(target, "[data-line]", "data-line");
  if (lineRaw) {
    const n = Number(lineRaw);
    if (Number.isFinite(n) && n > 0) prov.line = n;
  }

  const rootId = closestAttr(target, "[data-root-id]", "data-root-id");
  if (rootId) prov.rootId = rootId;

  const sourceRef = closestAttr(target, "[data-source-ref]", "data-source-ref");
  if (sourceRef) prov.sourceRef = sourceRef;
  const hitKind = closestAttr(target, "[data-hit-kind]", "data-hit-kind");
  if (hitKind) prov.hitKind = hitKind;

  // Search / detail may stamp session when outside chat-stream.
  if (!prov.sessionId) {
    const detailSid = closestAttr(target, "[data-session-id]", "data-session-id");
    if (detailSid) prov.sessionId = detailSid;
  }

  const artifactId = closestAttr(target, "[data-artifact-id]", "data-artifact-id");
  if (artifactId) prov.artifactId = artifactId;

  const handle = closestAttr(target, "[data-handle]", "data-handle");
  if (handle) prov.handle = handle;

  return prov;
}

/** First line of a text attachment body. Omits empty fields. */
export function formatSelectedTextHeader(prov: SelectionProvenance): string {
  const parts: string[] = [];
  if (prov.sessionId) parts.push(`session=${prov.sessionId}`);
  if (prov.messageId) parts.push(`message=${prov.messageId}`);
  if (prov.toolCallId) parts.push(`tool=${prov.toolCallId}`);
  if (prov.projectId) parts.push(`project=${prov.projectId}`);
  if (prov.handle) parts.push(`handle=${prov.handle}`);
  if (prov.path) parts.push(`path=${prov.path}`);
  if (prov.line != null) parts.push(`line=${prov.line}`);
  if (prov.rootId) parts.push(`root=${prov.rootId}`);
  if (prov.sourceRef) parts.push(`source_ref=${prov.sourceRef}`);
  if (prov.hitKind) parts.push(`hit_kind=${prov.hitKind}`);
  if (prov.artifactId) parts.push(`artifact=${prov.artifactId}`);
  return `${USER_SELECTED_TEXT_PREFIX}${parts.join("; ")}]`;
}

/** Header + truncated snippet; only the snippet is truncated. */
export function formatSelectedTextAttachmentBody(
  prov: SelectionProvenance,
  truncatedSnippet: string,
): string {
  return `${formatSelectedTextHeader(prov)}\n${truncatedSnippet}`;
}

export function selectionAttachmentFilename(path: string | undefined): string {
  const p = path?.trim();
  if (!p) return "selection.txt";
  const norm = p.replace(/\\/g, "/");
  const base = norm.split("/").filter(Boolean).pop() || p;
  return `${base} (selection).txt`;
}
