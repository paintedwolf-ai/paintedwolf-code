// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { For, createSignal } from "solid-js";
import { afterEach, describe, expect, it } from "vitest";
import { UnderlineTabs } from "./UnderlineTabs.tsx";
import type { ResidentPresence } from "../../ui/resident-surfaces.ts";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";

afterEach(cleanup);

describe("UnderlineTabs keyboard navigation", () => {
  it("keeps one tab stop and activates tabs with arrows, Home, and End", async () => {
    const labels = ["General", "Keyboard", "Advanced"];
    const [selected, setSelected] = createSignal(1);
    const view = render(() => (
      <UnderlineTabs aria-label="Settings sections">
        <For each={labels}>
          {(label, index) => (
            <button
              type="button"
              role="tab"
              aria-selected={selected() === index()}
              onClick={() => setSelected(index())}
            >
              {label}
            </button>
          )}
        </For>
      </UnderlineTabs>
    ));
    const tabs = view.getAllByRole("tab") as HTMLButtonElement[];

    expect(tabs.map((tab) => tab.tabIndex)).toEqual([-1, 0, -1]);
    tabs[1]!.focus();
    fireEvent.keyDown(tabs[1]!, { key: "ArrowRight" });
    expect(selected()).toBe(2);
    expect(document.activeElement).toBe(tabs[2]);

    fireEvent.keyDown(tabs[2]!, { key: "ArrowRight" });
    expect(selected()).toBe(0);
    expect(document.activeElement).toBe(tabs[0]);

    fireEvent.keyDown(tabs[0]!, { key: "End" });
    expect(selected()).toBe(2);
    fireEvent.keyDown(tabs[2]!, { key: "Home" });
    expect(selected()).toBe(0);
    await waitFor(() =>
      expect(tabs.map((tab) => tab.tabIndex)).toEqual([0, -1, -1]),
    );
  });

  it("repairs roving tab stops when a retained Settings stage activates", async () => {
    const [presence, setPresence] = createSignal<ResidentPresence>("idle");
    render(() => (
      <ResidentPresenceProvider presence={presence()}>
        <UnderlineTabs aria-label="Settings sections">
          <For each={["Display", "Notifications", "Editor"]}>
            {(label, index) => (
              <button type="button" role="tab" aria-selected={index() === 0}>
                {label}
              </button>
            )}
          </For>
        </UnderlineTabs>
      </ResidentPresenceProvider>
    ));
    const tabs = screen.getAllByRole("tab", { hidden: true }) as HTMLButtonElement[];
    expect(tabs.map((tab) => tab.tabIndex)).toEqual([0, 0, 0]);

    setPresence("active");
    await waitFor(() =>
      expect(tabs.map((tab) => tab.tabIndex)).toEqual([0, -1, -1]),
    );
  });
});
