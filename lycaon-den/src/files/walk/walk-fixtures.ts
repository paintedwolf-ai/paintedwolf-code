import { sourceEffectFixture } from "../source/source-effect-fixture.ts";
import type { SourceWalkEffect, SourceWalkResponse, SourceWalkTurn } from "../../api/types.ts";
import { buildWalk } from "./walk-model.ts";
import { stubFilesClient as stubClient, type ComparisonLoader } from "../../test/source-client-fixture.ts";

export function walkTurnFixture(turn: number, sessionId = "s1"): SourceWalkTurn {
  return {
    session_id: sessionId, turn,
    message_id: `${sessionId}-user-${turn}`, prompt: `Make change ${turn}`,
    observed_at: "2026-09-03T00:00:00Z",
  };
}

export function walkEffectFixture(id: string, turn: number, ordinal: number): SourceWalkEffect {
  return sourceEffectFixture({
    id, project_id: "p1", operation_id: `op-${id}`, file_id: "file-a",
    before_version_id: `before-${id}`, after_version_id: `after-${id}`,
    workspace_kind: "project", root_id: "r1", path: "a.ts", entry_kind: "file",
    op: "write", origin: "agent", session_id: "s1", turn, ordinal,
    tool_call_id: `call-${id}`, tool_name: "edit", cause: "tool", capture_quality: "exact",
    observed_at: "2026-09-03T00:00:00Z",
  });
}

export function walkResponseFixture(effects: SourceWalkEffect[]): SourceWalkResponse {
  return {
    baseline: "session:s1",
    files: effects.length ? [{
      file_id: "file-a", root_id: "r1", path: "a.ts", changed_since_presented: false,
      unpresented_agent_effects: 0, tip: { state: "content", sha256: "tip" },
      head_match: "unknown", effects,
    }] : [],
    turns: [...new Set(effects.map((effect) => effect.turn))].map((turn) => walkTurnFixture(turn)),
    commands: [], git_changes: [], commit_available: true, next_cursor: undefined,
  };
}

export function walkClientFixture(read: () => SourceWalkResponse) {
  return stubClient({
    listProjectSourceWalk: async () => read(),
    getProjectSourceWalkSummary: async (_projectId: string, _sessionId: string, messageIds: string[]) => {
      const response = read();
      const walk = buildWalk("session:s1", response.files, response.git_changes, response.commands, response.turns);
      return messageIds.map((messageId) => {
        const chapter = walk.chapters.find((entry) => entry.messageId === messageId);
        const steps = walk.steps.filter((step) => chapter?.stepKeys.includes(step.key));
        const items = new Set(steps.flatMap((step) => step.kind === "effect" ? [step.effect.file_id] : step.effects.map((effect) => effect.file_id)));
        return { message_id: messageId, turn: chapter?.turn ?? 0, steps: steps.length, items: items.size };
      });
    },
    listProjectSourcePins: async () => ({ pins: [] }),
    readComparison: (async (_id, opts) => ({
      in_range: true, location_changed: false,
      before: { state: "content", size_bytes: 3, availability: "available", content: `before ${"effectId" in opts ? opts.effectId : ""}` },
      after: { state: "content", size_bytes: 3, availability: "available", content: `after ${"effectId" in opts ? opts.effectId : ""}` },
    })) as ComparisonLoader,
  });
}
