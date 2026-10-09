import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { WalkChromeFixture } from "./walk-chrome-fixture.tsx";
import { walkClientFixture, walkEffectFixture, walkResponseFixture } from "./walk-fixtures.ts";
import { enterWalk, isWalking, refreshWalk, resetWalkForTests, setWalkAt, walkState } from "./walk-store.ts";

let width = 400;
let resize: (() => void) | undefined;
beforeEach(() => {
  resetWalkForTests();
  width = 400;
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockImplementation(() => width);
  vi.stubGlobal("ResizeObserver", class {
    constructor(private readonly callback: () => void) {}
    observe(element: HTMLElement) { if (element.dataset.testid === "step-bar-rail") resize = this.callback; }
    disconnect() {}
  });
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); resize = undefined; });

async function mount(count = 240) {
  const effects = Array.from({ length: count }, (_, i) => walkEffectFixture(`effect-${i}`, Math.floor(i / 10) + 1, i + 1));
  const client = walkClientFixture(() => walkResponseFixture(effects));
  render(() => <WalkChromeFixture projectId="p1" />);
  await enterWalk("p1", client, "s1", null, { startMessageId: "s1-user-1" });
  await waitFor(() => expect(screen.getByTestId("step-bar").dataset.boot).toBe("ready"));
  const viewport = screen.getByTestId("step-bar-rail");
  return { effects, client, viewport };
}
function browse(viewport: HTMLElement, left: number) {
  viewport.scrollLeft = left;
  fireEvent.scroll(viewport);
}
function current() { return document.querySelector<HTMLButtonElement>('[data-testid="step-bar-dot"][aria-current="step"]')!; }

describe("long walk browsing", () => {
  it("bounds mounted markers while retaining the selected tab stop", async () => {
    const { viewport } = await mount(10_000);
    expect(screen.getByTestId("step-bar-read").textContent).toContain("1 of 10000");
    expect(screen.getAllByTestId("step-bar-dot").length).toBeLessThan(25);
    const first = current();
    browse(viewport, 200_000);
    expect(screen.getAllByTestId("step-bar-dot").length).toBeLessThan(25);
    expect(current()).toBe(first);
    expect(first.tabIndex).toBe(0);
    expect(walkState("p1").at).toBe(0);
    expect(screen.getAllByTestId("step-bar-dot").some((dot) => dot.getAttribute("aria-label")?.startsWith("Step 5001 of"))).toBe(true);
  });

  it("browses with the edge arrows without loading another comparison", async () => {
    const { viewport, client } = await mount();
    const load = vi.spyOn(client, "readComparison");
    fireEvent.click(screen.getByRole("button", { name: "Browse later steps" }));
    await waitFor(() => expect(viewport.scrollLeft).toBe(300));
    expect(walkState("p1").at).toBe(0);
    expect(load).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Browse earlier steps" }));
    await waitFor(() => expect(viewport.scrollLeft).toBe(0));
  });

  it("seeks using scrolled coordinates and dismisses a stale tooltip", async () => {
    const { viewport } = await mount();
    fireEvent.mouseEnter(current());
    expect(screen.getByTestId("step-bar-tooltip")).toBeTruthy();
    browse(viewport, 400);
    expect(screen.queryByTestId("step-bar-tooltip")).toBeNull();
    fireEvent.click(viewport, { clientX: 92 });
    await waitFor(() => expect(walkState("p1").at).toBe(12));
  });

  it("keeps visible edge targets stationary when clicked or focused", async () => {
    const { viewport } = await mount();
    browse(viewport, 400);
    for (const index of [10, 19, 10]) {
      const dot = screen.getByRole("button", { name: new RegExp(`^Step ${index + 1} of`) });
      dot.focus();
      fireEvent.click(dot);
      await waitFor(() => expect(walkState("p1").at).toBe(index));
      expect(viewport.scrollLeft).toBe(400);
    }
  });

  it("preserves browsing and marker identities as live steps arrive", async () => {
    const { viewport, effects } = await mount();
    browse(viewport, 800);
    const held = screen.getAllByTestId("step-bar-dot")[5];
    effects.push(walkEffectFixture("new", 25, 241));
    await refreshWalk("p1");
    expect(viewport.scrollLeft).toBe(800);
    expect(walkState("p1").at).toBe(0);
    expect(screen.getAllByTestId("step-bar-dot")).toContain(held);
    expect(screen.getByTestId("walk-transport-refresh").getAttribute("aria-label")).toContain("1 new step");
    fireEvent.click(screen.getByTestId("walk-transport-refresh"));
    await waitFor(() => expect(walkState("p1").at).toBe(240));
    expect(viewport.scrollLeft).toBeGreaterThan(9000);
  });

  it("moves focus only once the requested comparison settles", async () => {
    const { viewport, client } = await mount();
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const original = client.readComparison;
    vi.spyOn(client, "readComparison").mockImplementation(async (...args) => { await gate; return original(...args); });
    current().focus();
    const held = current();
    fireEvent.keyDown(held, { key: "End" });
    expect(document.activeElement).toBe(held);
    expect(viewport.scrollLeft).toBe(0);
    expect(walkState("p1").targetAt).toBe(239);
    release();
    await waitFor(() => expect(walkState("p1").at).toBe(239));
    await waitFor(() => expect(document.activeElement).toBe(current()));
    expect(viewport.scrollLeft).toBeGreaterThan(9000);
    fireEvent.keyDown(current(), { key: "Home" });
    await waitFor(() => expect(walkState("p1").at).toBe(0));
    expect(viewport.scrollLeft).toBe(0);
  });

  it("resizes around a visible selection without returning a browser to it", async () => {
    const { viewport } = await mount();
    setWalkAt("p1", 8);
    await waitFor(() => expect(walkState("p1").at).toBe(8));
    width = 200;
    resize?.();
    await waitFor(() => expect(viewport.scrollLeft).toBeGreaterThan(0));
    const x = Number.parseFloat(current().style.left);
    expect(x - viewport.scrollLeft).toBeLessThan(200);
    browse(viewport, 2000);
    width = 300;
    resize?.();
    await new Promise((resolve) => requestAnimationFrame(resolve));
    expect(viewport.scrollLeft).toBe(2000);
  });

  it("browses recognizable turns and changes, searches, and restores focus", async () => {
    await mount();
    const counter = screen.getByTestId("step-bar-read");
    fireEvent.click(counter);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("searchbox", { name: "Find in walk" })));
    const history = screen.getByRole("toolbar", { name: "Changes by turn" });
    expect(history.querySelectorAll("button[data-walk-destination]")).toHaveLength(240);
    expect(screen.getByText("Turn 24")).toBeTruthy();
    fireEvent.keyDown(screen.getByRole("searchbox"), { key: "ArrowDown" });
    fireEvent.keyDown(document.activeElement!, { key: "End" });
    expect(document.activeElement?.textContent).toContain("Step 240");
    fireEvent.click(screen.getByRole("button", { name: /a\.ts\s*Step 97\s*Modified/ }));
    await waitFor(() => expect(walkState("p1").at).toBe(96));
    expect(document.activeElement).toBe(counter);
    fireEvent.click(counter);
    fireEvent.input(screen.getByRole("searchbox"), { target: { value: "Make change 20" } });
    expect(screen.getByRole("toolbar", { name: "Changes by turn" }).querySelectorAll("button[data-walk-destination]")).toHaveLength(10);
    expect(screen.queryByText("Turn 19")).toBeNull();
    fireEvent.input(screen.getByRole("searchbox"), { target: { value: "" } });
    expect(screen.getByRole("toolbar", { name: "Changes by turn" }).querySelectorAll("button[data-walk-destination]")).toHaveLength(240);
    fireEvent.input(screen.getByRole("searchbox"), { target: { value: "Make change 20" } });
    fireEvent.click(screen.getByRole("button", { name: /a\.ts\s*Step 191\s*Modified/ }));
    await waitFor(() => expect(walkState("p1").at).toBe(190));
    fireEvent.click(counter);
    fireEvent.input(screen.getByRole("searchbox"), { target: { value: "no-such-file" } });
    expect(screen.getByText(/No matching changes/)).toBeTruthy();
    fireEvent.keyDown(screen.getByRole("searchbox"), { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.activeElement).toBe(counter);
    expect(isWalking("p1")).toBe(true);
  });
});
