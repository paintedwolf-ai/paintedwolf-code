import { beforeEach, describe, expect, it, vi } from "vitest";
import type { EventScope } from "../../api/types.ts";
import { connectionSourceEvents } from "./connection-source-events.ts";

const mocks = vi.hoisted(() => ({
  receiveSourceViewEvent: vi.fn(),
  applyAgentPresenceEvent: vi.fn(),
  applySourceChangesEvent: vi.fn(),
  applySourceOperationEvent: vi.fn(),
  applyFileBriefingEvent: vi.fn(),
  receiveEditorDocumentEvent: vi.fn(),
  notifyArtifactChanged: vi.fn(),
  fileSummariesSettingKnown: vi.fn(() => true),
  fileSummariesEnabled: vi.fn(() => true),
}));
vi.mock("../../ui/paged-view/source-view-session.ts", () => ({ receiveSourceViewEvent: mocks.receiveSourceViewEvent }));
vi.mock("../../files/components/agent-presence-store.ts", () => ({ applyAgentPresenceEvent: mocks.applyAgentPresenceEvent }));
vi.mock("../../files/source/source-events.ts", () => ({ applySourceChangesEvent: mocks.applySourceChangesEvent }));
vi.mock("../../store/source-operations.ts", () => ({ applySourceOperationEvent: mocks.applySourceOperationEvent }));
vi.mock("../../files/components/file-briefing-live.ts", () => ({ applyFileBriefingEvent: mocks.applyFileBriefingEvent }));
vi.mock("../../files/documents/editor-document.ts", () => ({ receiveEditorDocumentEvent: mocks.receiveEditorDocumentEvent }));
vi.mock("../../chat/visual/artifact-change-store.ts", () => ({ notifyArtifactChanged: mocks.notifyArtifactChanged }));
vi.mock("../../settings/editor/file-summary-settings.ts", () => ({
  fileSummariesSettingKnown: mocks.fileSummariesSettingKnown,
  fileSummariesEnabled: mocks.fileSummariesEnabled,
}));

const scope: EventScope = { kind: "project", project_id: "proj-1" };

function deliver(topic: keyof typeof connectionSourceEvents, event: object) {
  (connectionSourceEvents[topic] as (ev: object, scope: EventScope) => void)(event, scope);
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.fileSummariesSettingKnown.mockReturnValue(true);
  mocks.fileSummariesEnabled.mockReturnValue(true);
});

describe("connection source events", () => {
  it.each([
    ["source_view", mocks.receiveSourceViewEvent],
    ["source_changed", mocks.applySourceChangesEvent],
    ["source_operation", mocks.applySourceOperationEvent],
    ["editor_document", mocks.receiveEditorDocumentEvent],
    ["agent_presence", mocks.applyAgentPresenceEvent],
    ["artifact", mocks.notifyArtifactChanged],
  ] as const)("routes %s to its owning store", (topic, apply) => {
    const event = { topic };
    deliver(topic, event);
    expect(apply).toHaveBeenCalledWith(event);
  });

  it("applies file briefings only while file summaries are known to be enabled", () => {
    const event = { path: "a.ts" };
    deliver("file_briefing", event);
    expect(mocks.applyFileBriefingEvent).toHaveBeenCalledWith(event);

    mocks.fileSummariesEnabled.mockReturnValue(false);
    deliver("file_briefing", event);
    mocks.fileSummariesSettingKnown.mockReturnValue(false);
    mocks.fileSummariesEnabled.mockReturnValue(true);
    deliver("file_briefing", event);
    expect(mocks.applyFileBriefingEvent).toHaveBeenCalledTimes(1);
  });
});
