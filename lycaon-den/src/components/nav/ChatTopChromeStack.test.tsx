import { afterEach, describe, expect, it } from "vitest";
import { createSignal } from "solid-js";
import { render, screen } from "@solidjs/testing-library";
import { ChatTopChromeStack } from "./ChatTopChromeStack.tsx";
import { TabBar } from "./TabBar.tsx";
import type { TabSpec } from "./TabBar.tsx";

afterEach(() => {
  document.body.replaceChildren();
});

const tabs: TabSpec[] = [
  { id: "progress", label: "Progress", panel: () => <div data-testid="progress-panel" /> },
];

describe("ChatTopChromeStack", () => {
  it("mounts a slot only while its content is present", () => {
    const [present, setPresent] = createSignal(false);
    render(() => (
      <ChatTopChromeStack
        session={{
          present: () => present(),
          children: () => <div data-testid="notice-body" />,
        }}
      />
    ));
    const stack = screen.getByTestId("chat-top-chrome-stack");
    expect(stack.querySelectorAll("[data-slot]")).toHaveLength(0);

    setPresent(true);
    expect(
      [...stack.querySelectorAll("[data-slot]")].map((el) =>
        el.getAttribute("data-slot"),
      ),
    ).toEqual(["session"]);
    expect(screen.getByTestId("notice-body")).toBeTruthy();
  });

  it("renders present slots in the locked top-to-bottom order", () => {
    const slot = (present: boolean) => ({
      present: () => present,
      children: () => <div />,
    });
    render(() => (
      <ChatTopChromeStack
        session={slot(true)}
        spend={slot(true)}
        notifications={slot(true)}
        verify={slot(true)}
        promote={slot(false)}
        provider={slot(true)}
        folder={slot(true)}
      />
    ));
    const stack = screen.getByTestId("chat-top-chrome-stack");
    expect(
      [...stack.querySelectorAll("[data-slot]")].map((el) =>
        el.getAttribute("data-slot"),
      ),
    ).toEqual([
      "session",
      "spend",
      "notifications",
      "verify",
      "provider",
      "folder",
    ]);
  });

  // Presence updates must not rebuild stateful slot content.
  it("builds slot content once, not on every presence read", () => {
    let built = 0;
    const Card = () => {
      built += 1;
      return <div data-testid="card" />;
    };
    const [churn, setChurn] = createSignal(0);
    render(() => (
      <ChatTopChromeStack
        session={{ present: () => churn() >= 0, children: () => <Card /> }}
      />
    ));
    setChurn(1);
    setChurn(2);
    expect(built).toBe(1);
  });

  it("takes the full rail width below the chip row and side slots", () => {
    render(() => (
      <TabBar
        tabs={tabs}
        open="progress"
        onOpenChange={() => {}}
        leading={<button type="button" data-testid="leading-btn" />}
        trailing={<button type="button" data-testid="trailing-btn" />}
        dock={
          <ChatTopChromeStack
            session={{
              present: () => true,
              children: () => <div data-testid="notice-body" />,
            }}
          />
        }
      />
    ));

    const chips = document.querySelector(".tabs__chips");
    const dock = screen.getByTestId("chat-top-chrome-stack");
    const shell = document.querySelector(".tabs__panel-shell");
    const rail = document.querySelector(".tabs__rail");
    expect(chips && dock.compareDocumentPosition(chips) & Node.DOCUMENT_POSITION_PRECEDING)
      .toBeTruthy();
    expect(shell && dock.compareDocumentPosition(shell) & Node.DOCUMENT_POSITION_PRECEDING)
      .toBeTruthy();
    expect(dock.parentElement?.className).toBe("tabs__dock");
    // Side slots narrow the chip strip; the dock sits beside the rail, not inside it.
    expect(dock.closest(".tabs__strip")).toBeNull();
    expect(dock.closest(".tabs__rail")).toBeNull();
    expect(rail?.nextElementSibling).toBe(dock.parentElement);
    expect(dock.parentElement?.parentElement?.className).toBe("tabs");
  });
});
