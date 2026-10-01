import type { Message } from "../../../api/types.ts";
import { citationGroundingPresent } from "../../grounding/citation-grounding-model.ts";
import {
  draftWireFields,
  isCoordinatorDraftVariantB,
  isCoordinatorDraftWire,
  isCoordinatorIntermediateStep,
} from "./draft-model.ts";
import { messageGeneratingTokens } from "./generating-tokens.ts";
import type { TranscriptLayout } from "../layout/transcript-layout.ts";
import type { TranscriptItem } from "./transcript-item-model.ts";

type CoordinatorDraftItem = Extract<TranscriptItem, { kind: "draft" }>;
type CoordinatorAssistantItem = Extract<TranscriptItem, { kind: "assistant" }>;
type CoordinatorSlotItem = CoordinatorDraftItem | CoordinatorAssistantItem;

type CoordinatorSlotBuildOpts = {
  layout: TranscriptLayout;
};

function draftTranscriptItem(msg: Message): CoordinatorDraftItem {
  return {
    kind: "draft",
    key: msg.id,
    text: msg.content,
    live: msg.status === "streaming",
    generatingTokens: messageGeneratingTokens(msg),
    ...draftWireFields(msg),
  };
}

/** Grounded or committed final answer — version rail attaches in AssistantChatTurn. */
function acceptedCoordinatorAnswerItem(
  msg: Message,
): CoordinatorAssistantItem | undefined {
  const text = msg.content.trim();
  const hasRetries = isCoordinatorDraftVariantB(msg);
  // Grounding is the host's accept signal; the version rail still attaches on an
  // empty-body commit frame that carries retry history.
  if (citationGroundingPresent(msg.grounding)) {
    if (!text && !hasRetries) return undefined;
    return {
      kind: "assistant",
      key: msg.id,
      text,
      grounding: msg.grounding,
      navigationRefs: msg.navigation_refs,
    };
  }
  if (msg.draft_status === "committed" && text) {
    return {
      kind: "assistant",
      key: msg.id,
      text,
      grounding: msg.grounding,
      navigationRefs: msg.navigation_refs,
    };
  }
  return undefined;
}

function terminalDraftVersionHistoryItem(msg: Message): CoordinatorDraftItem | undefined {
  if (msg.draft_status !== "withdrawn" && msg.draft_status !== "rejected") {
    return undefined;
  }
  if (!msg.content.trim() && !isCoordinatorDraftVariantB(msg)) return undefined;
  return draftTranscriptItem(msg);
}

/**
 * Map one coordinator slot wire row to a draft-rail or accepted-answer transcript
 * item. Returns undefined when the slot has nothing renderable yet.
 */
export function coordinatorSlotItem(
  msg: Message,
  opts: CoordinatorSlotBuildOpts,
): CoordinatorSlotItem | undefined {
  if (opts.layout === "chat" && isCoordinatorIntermediateStep(msg)) {
    return draftTranscriptItem(msg);
  }

  if (opts.layout === "worker") {
    if (!isCoordinatorDraftWire(msg)) return undefined;
    if (!msg.content.trim()) return undefined;
    return {
      kind: "assistant",
      key: msg.id,
      text: msg.content,
      grounding: msg.grounding,
      navigationRefs: msg.navigation_refs,
    };
  }

  if (opts.layout === "chat") {
    // Visibility controls accepted transcript rendering.
    if (
      msg.visibility === "internal" &&
      isCoordinatorDraftWire(msg)
    ) {
      return draftTranscriptItem(msg);
    }
    const accepted = acceptedCoordinatorAnswerItem(msg);
    if (accepted) return accepted;
  }

  if (!isCoordinatorDraftWire(msg)) {
    return terminalDraftVersionHistoryItem(msg);
  }

  return draftTranscriptItem(msg);
}
