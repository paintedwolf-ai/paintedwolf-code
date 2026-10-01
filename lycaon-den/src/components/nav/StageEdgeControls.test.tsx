import { fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import type { AttentionRow } from "../../api/types.ts";
import { ShellHeaderTitlebar } from "../shell/ShellHeaderTitlebar.tsx";
import { type StageEdgePanes } from "./StageEdgeControls.tsx";

type Side = "left" | "right";

function panes(
  over: {
    nav?: Partial<{ collapsed: boolean; automatic: boolean; side: Side }>;
    conversation?: Partial<{
      collapsed: boolean;
      side: Side;
      attention: AttentionRow;
      hiddenWhenNarrow: boolean;
    }>;
  } = {},
): StageEdgePanes {
  return {
    nav: { collapsed: false, automatic: false, side: "left", onExpand: vi.fn(), ...over.nav },
    conversation: {
      collapsed: false,
      side: "right",
      onExpand: vi.fn(),
      hiddenWhenNarrow: false,
      onWiden: vi.fn(),
      widenRefusals: () => 0,
      ...over.conversation,
    },
  };
}

const edge = (side: Side) =>
  document.querySelector(`.den-shell-header-titlebar__edge[data-edge="${side}"]`);

describe("stage title bar edge controls", () => {
  it("reopens each hidden pane from its own window edge", () => {
    render(() => (
      <ShellHeaderTitlebar
        edges={panes({ nav: { collapsed: true }, conversation: { collapsed: true } })}
      />
    ));
    expect(edge("left")?.querySelector('[data-testid="nav-expand-btn"]')).toBeTruthy();
    expect(
      edge("right")?.querySelector('[data-testid="conversation-expand-btn"]'),
    ).toBeTruthy();
  });

  it("swaps the edges when the workspace mirrors", () => {
    render(() => (
      <ShellHeaderTitlebar
        edges={panes({
          nav: { collapsed: true, side: "right" },
          conversation: { collapsed: true, side: "left" },
        })}
      />
    ));
    expect(
      edge("left")?.querySelector('[data-testid="conversation-expand-btn"]'),
    ).toBeTruthy();
    expect(edge("right")?.querySelector('[data-testid="nav-expand-btn"]')).toBeTruthy();
  });

  it.each(["left", "right"] as const)("names two restores sharing the %s edge", (side) => {
    const current = panes({ nav: { collapsed: true, side }, conversation: { collapsed: true, side } });
    render(() => <ShellHeaderTitlebar edges={current} />);
    const nav = screen.getByRole("button", { name: "Show sidebar" });
    const chat = screen.getByRole("button", { name: "Show conversation" });
    expect(nav.textContent).toBe("Sidebar");
    expect(chat.textContent).toBe("Chat");
    expect(edge(side)?.contains(nav)).toBe(true);
    expect(edge(side)?.contains(chat)).toBe(true);
    fireEvent.click(nav);
    expect(current.nav.onExpand).toHaveBeenCalledOnce();
    expect(current.conversation.onExpand).not.toHaveBeenCalled();
    fireEvent.click(chat);
    expect(current.conversation.onExpand).toHaveBeenCalledOnce();
  });

  it("mounts a stage sidebar restore for the band when chat spans the wide seam", () => {
    const current = panes({ nav: { collapsed: true }, conversation: { side: "left", hiddenWhenNarrow: true } });
    current.nav.onlyWhenNarrow = true;
    render(() => <ShellHeaderTitlebar edges={current} />);
    const nav = screen.getByTestId("nav-expand-btn");
    expect(nav.closest(".den-nav-split-fallback")).not.toBeNull();
    expect(nav.textContent).toBe("Sidebar");
    expect(screen.getByTestId("split-fit-restore-conversation").textContent).toBe("Chat");
  });

  it("updates controls as panes appear, move, and disappear", () => {
    const [current, setCurrent] = createSignal<StageEdgePanes>();
    render(() => <ShellHeaderTitlebar edges={current()} />);
    expect(edge("left")).toBeNull();
    expect(edge("right")).toBeNull();

    const leading = panes({ nav: { collapsed: true } });
    setCurrent(leading);
    fireEvent.click(screen.getByTestId("nav-expand-btn"));
    expect(leading.nav.onExpand).toHaveBeenCalledOnce();

    const button = screen.getByTestId("nav-expand-btn");
    button.focus();
    const refreshed = panes({ nav: { collapsed: true } });
    setCurrent(refreshed);
    expect(screen.getByTestId("nav-expand-btn")).toBe(button);
    expect(document.activeElement).toBe(button);
    fireEvent.click(button);
    expect(refreshed.nav.onExpand).toHaveBeenCalledOnce();
    expect(leading.nav.onExpand).toHaveBeenCalledOnce();

    const trailing = panes({ nav: { collapsed: true, side: "right" } });
    setCurrent(trailing);
    expect(edge("left")).toBeNull();
    expect(edge("right")?.querySelector('[data-testid="nav-expand-btn"]')).toBeTruthy();
    fireEvent.click(screen.getByTestId("nav-expand-btn"));
    expect(trailing.nav.onExpand).toHaveBeenCalledOnce();

    setCurrent(panes());
    expect(edge("left")).toBeNull();
    expect(edge("right")).toBeNull();

    setCurrent(leading);
    setCurrent(undefined);
    expect(edge("left")).toBeNull();
    expect(edge("right")).toBeNull();
  });

  it("routes each control to its own pane", () => {
    const hidden = panes({ conversation: { collapsed: true } });
    render(() => <ShellHeaderTitlebar edges={hidden} />);
    expect(screen.queryByTestId("nav-expand-btn")).toBeNull();
    fireEvent.click(screen.getByTestId("conversation-expand-btn"));
    expect(hidden.conversation.onExpand).toHaveBeenCalledOnce();
  });

  it("keeps the double chevron for a sidebar the window width hid", () => {
    render(() => (
      <ShellHeaderTitlebar
        edges={panes({ nav: { collapsed: true, automatic: true } })}
      />
    ));
    expect(screen.getByTestId("nav-auto-restore-btn")).toBeTruthy();
  });

  it("offers the width back when the split keeps the stage instead", () => {
    const narrow = panes({ conversation: { hiddenWhenNarrow: true } });
    render(() => <ShellHeaderTitlebar edges={narrow} />);
    const widen = screen.getByTestId("split-fit-restore-conversation");
    expect(edge("right")?.contains(widen)).toBe(true);
    expect(widen.getAttribute("aria-label")).toBe(
      "Widen window to show conversation",
    );
    // The conversation is still open, so there is nothing to reopen.
    expect(screen.queryByTestId("conversation-expand-btn")).toBeNull();
    fireEvent.click(widen);
    expect(narrow.conversation.onWiden).toHaveBeenCalledOnce();
  });

  it("marks a waiting conversation the width took away", () => {
    render(() => (
      <ShellHeaderTitlebar
        edges={panes({
          conversation: {
            hiddenWhenNarrow: true,
            attention: {
              session_id: "s1",
              project_id: "p1",
              class: "needs_you",
              reason: "checkpoint",
            } as AttentionRow,
          },
        })}
      />
    ));
    const widen = screen.getByTestId("split-fit-restore-conversation");
    expect(widen.getAttribute("aria-label")).toBe(
      "Widen window to show conversation — Waiting on approval",
    );
    expect(
      widen.querySelector(".den-pane-toggle__dot")?.getAttribute(
        "data-attention-class",
      ),
    ).toBe("needs_you");
  });

  it("keeps the back chip on the leading edge beside a reopen control", () => {
    const back = { label: "Back to chat", onBack: vi.fn() };
    const shown = render(() => (
      <ShellHeaderTitlebar back={back} edges={panes({ nav: { collapsed: true } })} />
    ));
    expect(edge("left")?.querySelector('[data-testid="stage-back"]')).toBeTruthy();
    shown.unmount();

    render(() => <ShellHeaderTitlebar back={back} edges={panes()} />);
    expect(screen.getByTestId("stage-back")).toBeTruthy();
    expect(edge("left")).toBeNull();
  });
});
