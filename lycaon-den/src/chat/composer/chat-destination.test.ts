import { beforeEach, describe, expect, it } from "vitest";
import {
  chooseChatDestination,
  pendingChatDestinationRequest,
  resetChatDestinationForTests,
  settleChatDestination,
} from "./chat-destination.ts";

beforeEach(() => resetChatDestinationForTests());

describe("chat destination boundary", () => {
  it("waits for one exact project and session choice", async () => {
    const chosen = chooseChatDestination({
      projectId: " project-1 ",
      suggested: { projectId: "project-1", sessionId: "session-1" },
    });

    expect(pendingChatDestinationRequest()).toMatchObject({
      projectId: "project-1",
      suggested: { projectId: "project-1", sessionId: "session-1" },
    });

    settleChatDestination({ projectId: "project-1", sessionId: "session-2" });
    await expect(chosen).resolves.toEqual({
      projectId: "project-1",
      sessionId: "session-2",
    });
    expect(pendingChatDestinationRequest()).toBeNull();
  });

  it("cancels an older request when a new action controls the chooser", async () => {
    const older = chooseChatDestination({ projectId: "project-1" });
    const newer = chooseChatDestination({ projectId: "project-2" });

    await expect(older).resolves.toBeNull();
    settleChatDestination(null);
    await expect(newer).resolves.toBeNull();
  });
});
