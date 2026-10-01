import { describe, expect, it } from "vitest";
import { resolveSidebarPick, type SidebarPickInput } from "./sidebar-pick.ts";

function pick(overrides: Partial<SidebarPickInput> = {}) {
  return resolveSidebarPick({
    onScreen: false,
    conversationInLayout: false,
    hasPeerWindow: false,
    isSubject: false,
    ...overrides,
  });
}

describe("resolveSidebarPick", () => {
  it("reveals when the column already holds a chat", () => {
    expect(pick({ onScreen: true, conversationInLayout: true })).toBe("reveal");
  });

  it("reveals from a live split", () => {
    expect(
      pick({ onScreen: true, conversationInLayout: true, isSubject: false }),
    ).toBe("reveal");
  });

  it("raises the peer window rather than taking this column", () => {
    expect(pick({ onScreen: true, hasPeerWindow: true })).toBe("raise-peer");
  });

  it("moves the subject and keeps the stage when the chat is nowhere on screen", () => {
    expect(pick()).toBe("select-only");
  });

  it("treats a pick on the unseen subject as the ask to go there", () => {
    expect(pick({ isSubject: true })).toBe("show-subject");
  });

  // A missing window destination resolves to a local reveal.
  it("falls back to revealing when nothing can be raised", () => {
    expect(pick({ onScreen: true })).toBe("reveal");
  });

  // An already visible chat is revealed at its current destination.
  it("prefers visibility over subject when both hold", () => {
    expect(
      pick({ onScreen: true, conversationInLayout: true, isSubject: true }),
    ).toBe("reveal");
    expect(pick({ onScreen: true, hasPeerWindow: true, isSubject: true })).toBe(
      "raise-peer",
    );
  });
});
