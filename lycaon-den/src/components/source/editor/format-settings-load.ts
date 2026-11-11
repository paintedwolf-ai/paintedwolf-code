/** Load the host's formatting settings for open files. */

import type { LycaonClient } from "../../../api/client.ts";
import { rememberFormatConfigNames, type FormatSources } from "./format-settings.ts";
import { setFilesBufferFormatSettings } from "../../../files/documents/project-files-buffers.ts";
import { projectFilesState, type FileBuffer } from "../../../files/documents/files-buffer-state.ts";
import type { FileBufferKey } from "../../../files/components/project-files-model.ts";

const refreshGeneration = new Map<string, number>();

type ResolveArgs = {
  client: LycaonClient;
  projectId: string;
  rootId: string;
  path: string;
  sessionId?: string;
};

type Resolution = { ok: true; sources: FormatSources } | { ok: false };

/** A failed resolution is reported, not read as "nothing declared". */
async function resolveFormatSettings(args: ResolveArgs): Promise<Resolution> {
  try {
    const resolved = await args.client.getProjectSourceFormatSettings(
      args.projectId,
      args.path,
      args.rootId,
      args.sessionId,
    );
    rememberFormatConfigNames(args.projectId, resolved.config_names);
    return { ok: true, sources: resolved.sources };
  } catch {
    return { ok: false };
  }
}

/** Resolve formatting settings for a loaded buffer and store them on the buffer. */
export async function loadFormatSettingsForBuffer(args: {
  client: LycaonClient;
  projectId: string;
  key: FileBufferKey;
  buffer: Pick<FileBuffer, "rootId" | "path" | "kind" | "jobId">;
  sessionId?: string;
}): Promise<void> {
  if (args.buffer.kind !== "text" || args.buffer.jobId) {
    setFilesBufferFormatSettings(args.projectId, args.key, undefined);
    return;
  }
  const resolution = await resolveFormatSettings({
    client: args.client,
    projectId: args.projectId,
    rootId: args.buffer.rootId,
    path: args.buffer.path,
    sessionId: args.sessionId,
  });
  // A failed resolution keeps the buffer's current settings.
  if (resolution.ok) setFilesBufferFormatSettings(args.projectId, args.key, resolution.sources);
}

/** Re-resolve open root buffers after a configuration change. */
export async function refreshOpenFormatSettings(args: {
  client: LycaonClient;
  projectId: string;
  rootId: string;
  sessionId?: string;
}): Promise<void> {
  const refreshKey = `${args.projectId}\0${args.rootId}`;
  const generation = (refreshGeneration.get(refreshKey) ?? 0) + 1;
  refreshGeneration.set(refreshKey, generation);
  try {
    const state = projectFilesState(args.projectId);
    const buffers = state.order
      .map((key) => ({ key, buffer: state.byKey[key] }))
      .filter(
        (row): row is { key: FileBufferKey; buffer: FileBuffer } =>
          row.buffer?.rootId === args.rootId &&
          row.buffer.kind === "text" &&
          !row.buffer.jobId,
      );
    for (const { key, buffer } of buffers) {
      const resolution = await resolveFormatSettings({
        client: args.client,
        projectId: args.projectId,
        rootId: buffer.rootId,
        path: buffer.path,
        sessionId: args.sessionId,
      });
      if (refreshGeneration.get(refreshKey) !== generation) return;
      if (!resolution.ok) continue;
      const current = projectFilesState(args.projectId).byKey[key];
      if (
        !current ||
        current.rootId !== buffer.rootId ||
        current.path !== buffer.path
      ) {
        continue;
      }
      setFilesBufferFormatSettings(args.projectId, key, resolution.sources);
    }
  } finally {
    if (refreshGeneration.get(refreshKey) === generation) {
      refreshGeneration.delete(refreshKey);
    }
  }
}
