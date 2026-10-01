import { describe, expect, it } from "vitest";
import {
  composerBlockReason,
  composerComposeBlockReason,
  isComposerDisabled,
  isComposerSendBlocked,
} from "./composer-rules.ts";

// New installs have no configured provider.
describe("composer no-provider block", () => {
  it("blocks the composer when no provider is configured", () => {
    expect(composerBlockReason("connected", "s1", null, null, true)).toBe(
      "no_provider",
    );
    expect(isComposerDisabled("connected", "s1", null, null, true)).toBe(true);
  });

  it("holds Send while preparing but keeps draft and attach open", () => {
    expect(
      composerBlockReason("connected", "s1", null, null, false, "preparing"),
    ).toBe("session_preparing");
    expect(
      composerComposeBlockReason(
        "connected",
        "s1",
        null,
        null,
        false,
        "preparing",
      ),
    ).toBeNull();
    expect(
      isComposerDisabled("connected", "s1", null, null, false, "preparing"),
    ).toBe(false);
    expect(
      isComposerSendBlocked("connected", "s1", null, null, false, "preparing"),
    ).toBe(true);
  });

  it("holds Send while a workflow is paused without refusing attach", () => {
    const paused = { status: "paused" } as const;
    expect(
      composerBlockReason("connected", "s1", paused as never, null, false),
    ).toBe("workflow_paused");
    expect(
      composerComposeBlockReason(
        "connected",
        "s1",
        paused as never,
        null,
        false,
      ),
    ).toBeNull();
    expect(
      isComposerDisabled("connected", "s1", paused as never, null, false),
    ).toBe(false);
    expect(
      isComposerSendBlocked("connected", "s1", paused as never, null, false),
    ).toBe(true);
  });

  it("leaves the composer usable once a provider exists", () => {
    expect(composerBlockReason("connected", "s1", null, null, false)).toBeNull();
    expect(isComposerDisabled("connected", "s1", null, null, false)).toBe(false);
  });

  it("defaults to not blocking when the caller omits provider state", () => {
    expect(composerBlockReason("connected", "s1")).toBeNull();
  });

  // Offline is the more actionable message when both are true.
  it("reports offline ahead of no_provider", () => {
    expect(composerBlockReason("disconnected", "s1", null, null, true)).toBe(
      "offline",
    );
  });
});
