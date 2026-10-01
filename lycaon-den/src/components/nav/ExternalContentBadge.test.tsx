import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { ExternalContentBadge } from "./ExternalContentBadge.tsx";
import {
  EXTERNAL_CONTENT_BADGE_LABEL,
  EXTERNAL_CONTENT_BADGE_TITLE,
} from "../../chat/untrusted/untrusted-content-copy.ts";

describe("ExternalContentBadge", () => {
  it("hides when not visible", () => {
    render(() => <ExternalContentBadge visible={false} />);
    expect(screen.queryByTestId("external-content-badge")).toBeNull();
  });

  it("shows icon button with tooltip and activates on click", () => {
    const onActivate = vi.fn();
    render(() => <ExternalContentBadge visible onActivate={onActivate} />);
    const badge = screen.getByTestId("external-content-badge");
    expect(badge.tagName).toBe("BUTTON");
    expect(badge.getAttribute("aria-label")).toBe(EXTERNAL_CONTENT_BADGE_LABEL);
    expect(badge.getAttribute("data-tip")).toBe(EXTERNAL_CONTENT_BADGE_TITLE);
    expect(badge.querySelector("svg")).toBeTruthy();
    fireEvent.click(badge);
    expect(onActivate).toHaveBeenCalledTimes(1);
  });
});
