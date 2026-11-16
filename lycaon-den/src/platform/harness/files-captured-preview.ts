import { batch } from "solid-js";
import { openFilesBuffer } from "../../files/documents/project-files-buffers.ts";
import type { FileVersionView } from "../../files/history/file-version.ts";
import type { FilesTreeSelection } from "../../files/tree/files-tree-context.ts";

export function filesLocationPreviewEnabled(): boolean {
  return import.meta.env.VITE_LYCAON_PROXY === "1" &&
    new URLSearchParams(window.location.search).get("files-location-preview") === "1";
}

type Capture = { before?: string; after?: string; title: string; detail: string };
const nginx = 'server {\n    listen 443 ssl;\n    server_name preview.example.test;\n\n    location / {\n        proxy_pass http://127.0.0.1:3000;\n        proxy_read_timeout 60s;\n    }\n}\n';
const deploy = '# Website deployment\nservice: website\nenvironment: staging\n\nhealthcheck:\n  path: /health\n  interval: 10s\n  timeout: 60s\n\nreplicas: 2\n';
/** Illustrative capture records; their bytes are never fetched from a remote machine. */
const captures: Record<string, Capture> = {
  'ops/deploy.yaml': {
    before: deploy.replace('timeout: 60s', 'timeout: 30s'), after: deploy,
    title: 'Captured change · Read only',
    detail: 'This Mac · /Users/chris/ops/deploy.yaml · Before and after captured · Update deployment, turn 3',
  },
  'staging-01/etc/nginx/nginx.conf': {
    before: nginx.replace('timeout 60s', 'timeout 30s'), after: nginx,
    title: 'Captured change · Read only',
    detail: 'staging-01 · /etc/nginx/nginx.conf · Before and after captured · Update deployment, turn 3',
  },
  'staging-01/etc/nginx/upstream.conf': {
    after: 'upstream website {\n    server 127.0.0.1:3000;\n    keepalive 32;\n}\n',
    title: 'Partial capture · After only · Read only',
    detail: 'staging-01 · /etc/nginx/upstream.conf · No before version was captured; this is not a creation diff',
  },
  'staging-01/etc/nginx/mime.types': {
    title: 'Reported change · Contents not captured',
    detail: 'staging-01 · /etc/nginx/mime.types · The tool reported a modification; no before or after bytes were returned',
  },
};

export function openFilesCapturedPreview(projectId: string, selection: FilesTreeSelection): boolean {
  if (!filesLocationPreviewEnabled()) return false;
  const capture = captures[`${selection.rootLabel}/${selection.path}`];
  if (!capture) return false;
  const available = capture.after !== undefined;
  const version: FileVersionView = {
    source: available ? { kind: 'text', before: capture.before ?? capture.after!, after: capture.after! } : { kind: 'unavailable' },
    versionId: '', fileId: '', rootId: selection.rootId, path: selection.path, op: undefined,
    ts: '2026-11-16T19:00:00Z', beforeAvailability: capture.before === undefined ? 'not_captured' : 'available',
    availability: available ? 'available' : 'not_captured', sha256: null,
    sizeBytes: new TextEncoder().encode(capture.after ?? '').length,
    initialComparison: capture.before === undefined ? 'current' : 'before',
  };
  batch(() => openFilesBuffer(projectId, {
    ...selection, kind: 'diff', intent: 'permanent', jobId: 'preview-diff:location-capture',
    diffPreview: { version, title: capture.title, detail: capture.detail,
      ...(!available ? { notice: { tone: 'info' as const, message: 'Reported modification only. File contents and the resulting change could not be verified from the returned evidence.' } } : {}),
    },
  }));
  return true;
}
