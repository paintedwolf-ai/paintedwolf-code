import { afterEach, describe, expect, it } from "vitest";
import {
  UNREAD_ABSENCE_MS,
  captureUnreadBoundary,
  clearUnreadBoundary,
  resetUnreadBoundariesForTests,
  unreadSince,
} from "./unread-boundary.ts";

afterEach(resetUnreadBoundariesForTests);

const at = (iso: string) => Date.parse(iso);

describe("unread boundary", () => {
  it("holds the stamp from the previous look until the next one replaces it", () => {
    captureUnreadBoundary("s1", "2026-09-12T19:00:00Z", at("2026-09-12T21:00:00Z"));
    expect(unreadSince("s1")).toBe(at("2026-09-12T19:00:00Z"));
    captureUnreadBoundary("s1", "2026-09-12T22:00:00Z", at("2026-09-13T08:00:00Z"));
    expect(unreadSince("s1")).toBe(at("2026-09-12T22:00:00Z"));
    expect(unreadSince("s2")).toBeNull();
  });

  it("draws no line after a glance away", () => {
    captureUnreadBoundary("s1", "2026-09-12T19:00:00Z", at("2026-09-12T21:00:00Z"));
    const seen = at("2026-09-12T21:30:00Z");
    captureUnreadBoundary("s1", "2026-09-12T21:30:00Z", seen + UNREAD_ABSENCE_MS - 1);
    expect(unreadSince("s1")).toBeNull();
    captureUnreadBoundary("s1", "2026-09-12T21:30:00Z", seen + UNREAD_ABSENCE_MS);
    expect(unreadSince("s1")).toBe(seen);
  });

  it("clears the line once the person writes back", () => {
    captureUnreadBoundary("s1", "2026-09-12T19:00:00Z", at("2026-09-12T21:00:00Z"));
    clearUnreadBoundary("s1");
    expect(unreadSince("s1")).toBeNull();
  });

  it("draws no line for a chat that has never been read", () => {
    captureUnreadBoundary("s1", "2026-09-12T19:00:00Z", at("2026-09-12T21:00:00Z"));
    captureUnreadBoundary("s1", undefined, at("2026-09-12T21:00:00Z"));
    expect(unreadSince("s1")).toBeNull();
  });
});
