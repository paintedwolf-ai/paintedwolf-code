import { sourceEffectFixture } from "../source/source-effect-fixture.ts";
import type { LycaonClient } from "../../api/client.ts";
import type {
  FileBriefingRequest,
  SourceWalkEffect,
  SourceWalkFile,
} from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { stubClient } from "../../test/client-fixture.ts";

export function reviewFileFixture(
  overrides: Partial<SourceWalkFile> & Pick<SourceWalkFile, "path">,
): SourceWalkFile {
  return {
    file_id: `file-${overrides.path}`,
    root_id: "r1",
    changed_since_presented: false,
    unpresented_agent_effects: 0,
    tip: { state: "content", sha256: `tip-${overrides.path}` },
    head_match: "unknown",
    last_at: "2026-07-30T00:00:00Z",
    effects: [],
    ...overrides,
  } as SourceWalkFile;
}

function effectFixture(
  id: string,
  path: string,
  overrides: Partial<SourceWalkEffect> = {},
): SourceWalkEffect {
  return sourceEffectFixture({
    id,
    project_id: "p1",
    operation_id: `operation-${id}`,
    file_id: `file-${path}`,
    after_version_id: `version-${id}`,
    workspace_kind: "project",
    root_id: "r1",
    path,
    entry_kind: "file",
    op: "write",
    origin: "agent",
    turn: 1,
    ordinal: 1,
    cause: "tool",
    capture_quality: "exact",
    observed_at: "2026-07-30T00:00:00Z",
    ...overrides,
  });
}

export function workingFileFixture(path: string): SourceWalkFile {
  return reviewFileFixture({
    path,
    changed_since_presented: true,
    unpresented_agent_effects: 1,
    presentation_effect_id: `effect-${path}`,
    presentation_ordinal: 7,
    effects: [
      effectFixture(`effect-${path}`, path, { ordinal: 7 }),
      effectFixture(`old-${path}`, path, {
        origin: "user",
        ordinal: 6,
        observed_at: "2026-07-29T00:00:00Z",
      }),
    ],
  } as Partial<SourceWalkFile> & Pick<SourceWalkFile, "path">);
}

export function reviewLensFixtureClient(
  files: readonly SourceWalkFile[],
): LycaonClient {
  const briefing = {
    target_key: "fixture-current",
    attempt_id: "11111111-1111-4111-8111-111111111111",
    root_id: "r1",
    path: "src/app.ts",
    presentation: "current" as const,
    status: "complete" as const,
    preview: {
      language: "typescript",
      line_count: 9,
    },
    locations: [{ line: 1, name: "ReviewHeader", kind: "component" }],
    sections: [
      { kind: "purpose" as const, text: "Implements the `ReviewHeader`." },
      { kind: "structure" as const, text: "Builds the title and tab controls." },
      { kind: "key_behavior" as const, text: "Keeps tab semantics aligned." },
    ],
    fallback_text: "",
    truncated: false,
    source_sha256: "abcdef0123456789",
    updated_at: "2026-07-30T00:00:01Z",
  };
  return stubClient({
    listProjectSourceWalk: async (_projectId: string, opts?: { baseline?: string }) => ({
      baseline: opts?.baseline ?? "presentation",
      files: [...files],
      git_changes: [],
      commands: [],
      turns: [],
      commit_available: true,
    }),
    listProjectSourcePins: async () => ({ pins: [] }),
    getProjectSourceStorage: async () => ({
      inventory: {
        status: "ready",
        complete: true,
        roots_generation: 1,
        indexed_files: 0,
      },
      storage: {
        lanes: [{ lane: "source_blobs" as const, scope: "device" as const, used_bytes: 0 }],
      },
    }),
    completeProjectSourcePresentation: async () => undefined,
    listProjectSourceSeen: async () => ({ files: [], next_cursor: undefined }),
    getFileBriefing: async () => briefing,
    requestFileBriefing: async (
      _projectId: string,
      request: FileBriefingRequest,
    ) => ({
      ...briefing,
      root_id: request.root_id,
      path: request.path,
      presentation: request.presentation,
      target_key: `fixture-${request.presentation}`,
    }),
  });
}

export function reviewLensFixtureStore(): AppStore {
  return {
    state: {
      currentSession: { id: "s1", title: "Focus" },
      messages: [],
      workers: [],
    },
  } as unknown as AppStore;
}
