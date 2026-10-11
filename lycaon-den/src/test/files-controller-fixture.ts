import { vi } from "vitest";
import { stubClient } from "./client-fixture.ts";
import type { ProjectRoot, ProjectSourceReadResponse } from "../api/types.ts";
import { openFilesBuffer, applyFilesBufferLoad } from "../files/documents/project-files-buffers.ts";
import { projectFilesState } from "../files/documents/files-buffer-state.ts";

export function controllerSource(content = "original", partial: Partial<ProjectSourceReadResponse> = {}): ProjectSourceReadResponse {
  return { file_id: "", version_id: "version", workspace_id: "workspace", workspace_kind: "project",
    path: "file.ts", content, over_limit: false, writable: true, binary: false,
    size_bytes: content.length, sha256: content, ...partial } as ProjectSourceReadResponse;
}

export function filesControllerFixture(projectId: string) {
  const client = stubClient({ getProjectSource: vi.fn().mockResolvedValue(controllerSource()) });
  const root = { id: "root", label: "Root", path: "/project", kind: "attached", is_primary: true, added_at: "2026-10-11T00:00:00Z" } satisfies ProjectRoot;
  const state = () => projectFilesState(projectId);
  const open = (path = "file.ts") => {
    const key = openFilesBuffer(projectId, { rootId: root.id, rootLabel: root.label, path, intent: "permanent" });
    applyFilesBufferLoad(projectId, key, controllerSource("original", { path }));
    return key;
  };
  const scope = { projectId: () => projectId, client: () => client, sourceSessionId: () => "chat",
    state, activeBuffer: () => state().activeKey ? state().byKey[state().activeKey!] ?? null : null,
    filesRoots: () => [root], rootLabelFor: () => root.label,
  };
  return { projectId, client, root, state, open, scope };
}
