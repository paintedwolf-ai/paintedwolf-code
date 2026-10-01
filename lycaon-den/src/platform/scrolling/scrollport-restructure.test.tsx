// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { render, waitFor } from "@solidjs/testing-library";
import { For, Show, createSignal } from "solid-js";
import { setupThemedScrollbars } from "./themed-scrollbars.ts";
import { Scrollport } from "../../components/primitives/Scrollport.tsx";
import { ProgressStrip } from "../../components/ProgressStrip.tsx";
import type { ProgressStep } from "../../api/types.ts";

const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

const chromeCount = (host: HTMLElement) =>
  [...host.children].filter((child) => child.classList.contains("os-scrollbar"))
    .length;

describe("scrollports keep the DOM their component rendered", () => {
  const cleanups: (() => void)[] = [];

  afterEach(() => {
    for (const stop of cleanups.splice(0)) stop();
    document.body.innerHTML = "";
  });

  function mountRoot(): HTMLElement {
    const root = document.createElement("div");
    root.id = "root";
    document.body.append(root);
    cleanups.push(setupThemedScrollbars());
    return root;
  }

  it("keeps rendered rows in the content and the chrome in the frame", async () => {
    const root = mountRoot();
    const [rows, setRows] = createSignal(["one"]);
    const view = render(
      () => (
        <Scrollport class="probe-frame" contentAs="ul" eager>
          <For each={rows()}>{(row) => <li>{row}</li>}</For>
        </Scrollport>
      ),
      { container: root },
    );
    await settle();
    const frame = view.container.querySelector<HTMLElement>(".probe-frame");
    expect(frame).toBeTruthy();
    if (!frame) return;
    const viewport = frame.querySelector<HTMLElement>(":scope > .den-scrollport__viewport");
    // The library adopts the rendered viewport instead of generating one.
    expect(viewport?.hasAttribute("data-overlayscrollbars-viewport")).toBe(true);
    expect(frame.querySelectorAll("[data-overlayscrollbars-viewport]")).toHaveLength(1);
    expect(chromeCount(frame)).toBeGreaterThan(0);
    expect(viewport?.querySelector(".os-scrollbar")).toBeNull();

    setRows(["one", "two"]);
    await settle();

    expect(viewport?.querySelectorAll(":scope > ul > li")).toHaveLength(2);
    expect(viewport?.querySelector(".os-scrollbar")).toBeNull();
  });

  it("keeps its chrome when a render clears the content", async () => {
    const root = mountRoot();
    const [open, setOpen] = createSignal(true);
    const view = render(
      () => (
        <Scrollport class="probe-frame" eager>
          <Show when={open()}>
            <p>body</p>
          </Show>
        </Scrollport>
      ),
      { container: root },
    );
    await settle();
    const frame = view.container.querySelector<HTMLElement>(".probe-frame");
    expect(frame).toBeTruthy();
    if (!frame) return;
    const chrome = chromeCount(frame);
    expect(chrome).toBeGreaterThan(0);

    setOpen(false);
    await settle();
    expect(chromeCount(frame)).toBe(chrome);
  });

  it("constructs scrollbars only for progress variants that can overflow", async () => {
    const root = mountRoot();
    const liveSteps: ProgressStep[] = Array.from({ length: 8 }, (_, index) => ({
      state: "pending",
      label: `Live step ${index + 1}`,
    }));
    const embeddedSteps: ProgressStep[] = Array.from(
      { length: 8 },
      (_, index) => ({
        state: "pending",
        label: `Embedded step ${index + 1}`,
      }),
    );
    const view = render(
      () => (
        <>
          <ProgressStrip
            created
            steps={[{ state: "pending", label: "Archived plan" }]}
          />
          <ProgressStrip steps={liveSteps} />
          <ProgressStrip embedded steps={embeddedSteps} />
        </>
      ),
      { container: root },
    );
    await settle();

    const strips =
      view.container.querySelectorAll<HTMLElement>(".progress-strip");
    expect(strips).toHaveLength(3);
    expect(
      strips[0]?.querySelectorAll("[data-den-scrollport]"),
    ).toHaveLength(0);
    expect(
      strips[1]?.querySelectorAll("[data-den-scrollport]"),
    ).toHaveLength(1);
    expect(
      strips[2]?.querySelectorAll("[data-den-scrollport]"),
    ).toHaveLength(1);
    const liveList = strips[1]?.querySelector<HTMLElement>(
      ".progress-strip__list-scroll",
    );
    const embeddedColumns = strips[2]?.querySelector<HTMLElement>(
      ".progress-strip__columns",
    );
    expect(liveList).toBeTruthy();
    expect(embeddedColumns).toBeTruthy();
    if (!liveList || !embeddedColumns) return;
    await waitFor(() => {
      expect(chromeCount(liveList)).toBeGreaterThan(0);
      expect(chromeCount(embeddedColumns)).toBeGreaterThan(0);
    });
  });
});
