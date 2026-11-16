import { render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { ShellHeaderTitlebar } from "./ShellHeaderTitlebar.tsx";

vi.mock("./ChromeDragSurface.tsx", () => ({ ChromeDragSurface: () => null }));

const conversationAtRight = {
  nav: { collapsed: false, automatic: false, side: "left" as const, onExpand: () => {} },
  conversation: {
    collapsed: true,
    side: "right" as const,
    onExpand: () => {},
    hiddenWhenNarrow: false,
    onWiden: () => {},
    widenRefusals: () => 0,
  },
};

describe("ShellHeaderTitlebar session tabs", () => {
  it("places the session's tabs after the conversation's reopen control", () => {
    render(() => (
      <ShellHeaderTitlebar
        edges={conversationAtRight}
        sessionTabs={{ present: () => true, children: () => <div data-testid="rail" /> }}
      />
    ));
    const rail = screen.getByTestId("rail");
    const reopen = screen.getByTestId("conversation-expand-btn");
    expect(rail.compareDocumentPosition(reopen) & Node.DOCUMENT_POSITION_PRECEDING).toBeTruthy();
  });

  it("shows the tabs only while the stage carries them", () => {
    const [present, setPresent] = createSignal(false);
    render(() => (
      <ShellHeaderTitlebar
        sessionTabs={{ present, children: () => <div data-testid="rail" /> }}
      />
    ));
    expect(screen.queryByTestId("rail")).toBeNull();
    setPresent(true);
    expect(screen.getByTestId("rail")).toBeTruthy();
    setPresent(false);
    expect(screen.queryByTestId("rail")).toBeNull();
  });
});
