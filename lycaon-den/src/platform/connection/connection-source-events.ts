import { receiveSourceViewEvent } from "../../ui/paged-view/source-view-session.ts";
import { applyAgentPresenceEvent } from "../../files/components/agent-presence-store.ts";
import { applySourceChangesEvent } from "../../files/source/source-events.ts";
import { applySourceOperationEvent } from "../../store/source-operations.ts";
import { applyFileBriefingEvent } from "../../files/components/file-briefing-live.ts";
import { receiveEditorDocumentEvent } from "../../files/documents/editor-document.ts";
import { notifyArtifactChanged } from "../../chat/visual/artifact-change-store.ts";
import { fileSummariesEnabled, fileSummariesSettingKnown } from "../../settings/editor/file-summary-settings.ts";
import type { TopicHandlers } from "../../api/events.ts";
export const connectionSourceEvents: TopicHandlers = {
  source_view: (ev) => receiveSourceViewEvent(ev),
  source_changed: (ev) => {
    applySourceChangesEvent(ev);
  },
  source_operation: (ev) => {
    applySourceOperationEvent(ev);
  },
  file_briefing: (ev) => {
    if (fileSummariesSettingKnown() && fileSummariesEnabled()) {
      applyFileBriefingEvent(ev);
    }
  },
  editor_document: (ev) => {
    receiveEditorDocumentEvent(ev);
  },
  agent_presence: (ev) => {
    applyAgentPresenceEvent(ev);
  },
  artifact: (ev) => {
    notifyArtifactChanged(ev);
  },
};
