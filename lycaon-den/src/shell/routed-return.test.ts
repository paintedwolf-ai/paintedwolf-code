import { describe, expect, it } from "vitest";
import {
  workspaceContextEqual,
  type WorkspaceContext,
} from "../platform/windows/workspace-view-registry.ts";
import {
  routedReturnFor,
  routedReturnOffer,
  type RoutedOrigin,
} from "./routed-return.ts";

type Nav = "projects" | "files" | "search";

const chat = (sessionId: string): WorkspaceContext => ({
  kind: "session",
  projectId: "p1",
  sessionId,
});
const stage = (stageId: string): WorkspaceContext => ({
  kind: "stage",
  projectId: "p1",
  stageId,
});

/** One window: the chat its conversation column holds and what is on screen. */
function windowFacts(initialChat: string) {
  let currentChat = initialChat;
  let shown: WorkspaceContext[] = [];
  const origin = (from: Nav): RoutedOrigin | null => {
    if (from === "projects") return { context: chat(currentChat), label: "chat" };
    if (from === "search") return { context: stage("search"), label: "Search" };
    return null;
  };
  const visible = (context: WorkspaceContext) =>
    shown.some((row) => workspaceContextEqual(row, context));
  return {
    resolve: { origin, visible },
    switchChat: (sessionId: string) => {
      currentChat = sessionId;
    },
    show: (...contexts: WorkspaceContext[]) => {
      shown = contexts;
    },
  };
}

describe("routed return", () => {
  it("records nothing for a direct visit or an arrival at the current stage", () => {
    expect(routedReturnFor<Nav>("files", "files")).toBeNull();
    expect(routedReturnFor<Nav>("projects", "files")).toEqual({
      stage: "files",
      from: "projects",
    });
  });

  it("offers the return only on the stage it routed to", () => {
    const facts = windowFacts("a");
    facts.show(stage("files"));
    const record = routedReturnFor<Nav>("projects", "files");
    expect(routedReturnOffer(null, "files", facts.resolve)).toBeNull();
    expect(routedReturnOffer(record, "search", facts.resolve)).toBeNull();
    expect(routedReturnOffer(record, "files", facts.resolve)).toEqual({
      from: "projects",
      label: "Back to chat",
    });
  });

  it("hides the return while a split shows the conversation, whichever chat it holds", () => {
    const facts = windowFacts("a");
    const record = routedReturnFor<Nav>("projects", "files");
    facts.show(chat("a"), stage("files"));
    expect(routedReturnOffer(record, "files", facts.resolve)).toBeNull();

    // A sidebar pick swaps the conversation in place; the stage never changes.
    facts.switchChat("b");
    facts.show(chat("b"), stage("files"));
    expect(routedReturnOffer(record, "files", facts.resolve)).toBeNull();

    // Hiding the conversation makes the return honest again.
    facts.show(stage("files"));
    expect(routedReturnOffer(record, "files", facts.resolve)).toEqual({
      from: "projects",
      label: "Back to chat",
    });
  });

  it("names a stage origin and drops an origin that no longer resolves", () => {
    const facts = windowFacts("a");
    facts.show(stage("files"));
    expect(
      routedReturnOffer(routedReturnFor<Nav>("search", "files"), "files", facts.resolve),
    ).toEqual({ from: "search", label: "Back to Search" });
    expect(
      routedReturnOffer(routedReturnFor<Nav>("files", "search"), "search", facts.resolve),
    ).toBeNull();
  });
});
