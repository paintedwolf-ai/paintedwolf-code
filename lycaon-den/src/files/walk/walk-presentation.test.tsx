import { Show, createSignal, onCleanup } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { PresentationProvider, usePresentationParticipant } from "../../ui/presentation-context.tsx";
import { createWalkPresentation } from "./walk-presentation.ts";
import { StepBar } from "./StepBar.tsx";
import { WalkTransport } from "./WalkTransport.tsx";
import { enterWalk, leaveWalk, refreshWalk, resetWalkForTests, subscribeWalk, walkState } from "./walk-store.ts";
import { walkClientFixture, walkEffectFixture, walkResponseFixture } from "./walk-fixtures.ts";

beforeEach(() => {
  resetWalkForTests();
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(400);
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

function mount() {
  const [editorReady, setEditorReady] = createSignal(false);
  function Editor() {
    usePresentationParticipant("editor", editorReady);
    return null;
  }
  render(() => {
    const [tick, setTick] = createSignal(0);
    onCleanup(subscribeWalk(() => setTick((value) => value + 1)));
    const state = () => { void tick(); return walkState("p1"); };
    const presentation = createWalkPresentation({ active: () => state().active, loading: () => state().status === "loading" });
    return <>
      <Show when={state().active}>
        <PresentationProvider preparation={presentation.preparation}>
          <Editor />
          <StepBar projectId="p1" presentation={presentation.reveal} />
          <WalkTransport projectId="p1" presentation={presentation.reveal} />
        </PresentationProvider>
      </Show>
      <Show when={presentation.waiting()}><div role="status">Preparing walk…</div></Show>
    </>;
  });
  return { setEditorReady };
}

const response = () => walkResponseFixture([walkEffectFixture("one", 1, 1)]);
const boot = (id: string) => screen.getByTestId(id).getAttribute("data-boot");

describe("walk presentation", () => {
  it("publishes the entire measured chrome only after the editor prepares", async () => {
    const { setEditorReady } = mount();
    await enterWalk("p1", walkClientFixture(response), "s1");
    expect(screen.getByTestId("step-bar").classList.contains("den-stage-boot")).toBe(true);
    expect(screen.getByTestId("walk-transport").classList.contains("den-stage-boot")).toBe(true);
    expect(boot("step-bar")).toBe("pending");
    expect(boot("walk-transport")).toBe("pending");
    expect(screen.getByTestId("step-bar").inert).toBe(true);
    expect(screen.queryByRole("status")).toBeNull();
    setEditorReady(true);
    await waitFor(() => expect(boot("step-bar")).toBe("ready"));
    expect(boot("walk-transport")).toBe("ready");
    expect(screen.getByTestId("step-bar").classList.contains("den-stage-boot")).toBe(true);
    expect(screen.getByTestId("walk-transport").classList.contains("den-stage-boot")).toBe(true);
    expect(screen.getByTestId("step-bar-read").textContent).toContain("1 of 1");
    fireEvent.click(screen.getByTestId("walk-transport-close"));
    expect(screen.queryByTestId("step-bar")).toBeNull();
    expect(screen.queryByTestId("walk-transport")).toBeNull();
  });

  it("cancels an unpublished entry without flashing or publishing its late result", async () => {
    const { setEditorReady } = mount();
    setEditorReady(true);
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const client = walkClientFixture(response);
    vi.spyOn(client, "listProjectSourceWalk").mockImplementation(async () => { await gate; return response(); });
    const entering = enterWalk("p1", client, "s1");
    expect(screen.getByTestId("step-bar").classList.contains("den-stage-boot")).toBe(true);
    expect(screen.getByTestId("walk-transport").classList.contains("den-stage-boot")).toBe(true);
    expect(boot("step-bar")).toBe("pending");
    expect(boot("walk-transport")).toBe("pending");
    expect(screen.queryByRole("status")).toBeNull();
    leaveWalk("p1");
    release();
    await entering;
    expect(screen.queryByTestId("step-bar")).toBeNull();
    expect(screen.queryByTestId("walk-transport")).toBeNull();
  });

  it("retains a published walk through refreshes and prepares a new entry again", async () => {
    const { setEditorReady } = mount();
    setEditorReady(true);
    const client = walkClientFixture(response);
    await enterWalk("p1", client, "s1");
    await waitFor(() => expect(boot("step-bar")).toBe("ready"));
    const rail = screen.getByTestId("step-bar");
    const controls = screen.getByTestId("walk-transport");
    await refreshWalk("p1");
    expect(screen.getByTestId("step-bar")).toBe(rail);
    expect(screen.getByTestId("walk-transport")).toBe(controls);
    expect(boot("step-bar")).toBe("ready");
    leaveWalk("p1");
    setEditorReady(false);
    await enterWalk("p1", client, "s1");
    expect(boot("step-bar")).toBe("pending");
    expect(boot("walk-transport")).toBe("pending");
    setEditorReady(true);
    await waitFor(() => expect(boot("step-bar")).toBe("ready"));
  });

  it("uses the shared grace period while keeping all chrome hidden", async () => {
    const { setEditorReady } = mount();
    await enterWalk("p1", walkClientFixture(response), "s1");
    expect(screen.queryByRole("status")).toBeNull();
    await screen.findByRole("status");
    expect(boot("step-bar")).toBe("pending");
    expect(boot("walk-transport")).toBe("pending");
    setEditorReady(true);
    await waitFor(() => expect(boot("step-bar")).toBe("ready"));
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("prepares an explicit replacement without exposing empty pieces", async () => {
    const { setEditorReady } = mount();
    setEditorReady(true);
    await enterWalk("p1", walkClientFixture(response), "s1");
    await waitFor(() => expect(boot("step-bar")).toBe("ready"));
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const client = walkClientFixture(response);
    vi.spyOn(client, "listProjectSourceWalk").mockImplementation(async () => { await gate; return response(); });
    const entering = enterWalk("p1", client, "s1", null, { transition: "replace" });
    expect(boot("step-bar")).toBe("pending");
    expect(boot("walk-transport")).toBe("pending");
    release();
    await entering;
    await waitFor(() => expect(boot("step-bar")).toBe("ready"));
    expect(boot("walk-transport")).toBe("ready");
  });
});
