// @vitest-environment jsdom
import { usePresentationParticipant } from "../../ui/presentation-context.tsx";
import { PRESENTATION_LOADING_GRACE_MS } from "../../ui/presentation.ts";
import { createSignal, For, onMount } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import { useResidentStack } from "../../ui/resident-surfaces.ts";
import { ResidentSurface } from "./ResidentSurface.tsx";
import { useResidentPresence } from "../../ui/resident-presence-context.tsx";
import { useResidentLive } from "../../ui/resident-activity.ts";
import { PreparedSurface } from "../primitives/PreparedSurface.tsx";

function BootBody(props: {
  id: string;
  boot: () => "pending" | "ready";
}) {
  usePresentationParticipant(props.id, () => props.id !== "b" || props.boot() === "ready");
  return (
    <div
      data-testid={`body-${props.id}`}
      data-boot={props.id === "b" ? props.boot() : "ready"}
      class={props.id === "b" ? "den-stage-enter-fade" : undefined}
    >
      {props.id}
    </div>
  );
}

function MountProbe(props: { id: string; onMountCount: () => void }) {
  onMount(() => props.onMountCount());
  return (
    <div data-testid={`body-${props.id}`} data-boot="ready">
      {props.id}
    </div>
  );
}

function PresenceProbe(props: { id: string }) {
  const presence = useResidentPresence();
  return <div data-testid={`presence-${props.id}`}>{presence()}</div>;
}

describe("ResidentSurface", () => {
  let nextFrameId = 1;
  let frameCallbacks = new Map<number, FrameRequestCallback>();

  const flushPaint = (frames = 4) => {
    for (let i = 0; i < frames; i++) {
      const callbacks = [...frameCallbacks.values()];
      frameCallbacks = new Map();
      for (const callback of callbacks) callback(performance.now());
    }
  };

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => {
      const id = nextFrameId++;
      frameCallbacks.set(id, cb);
      return id;
    });
    vi.stubGlobal("cancelAnimationFrame", (id: number) => {
      frameCallbacks.delete(id);
    });
  });

  afterEach(() => {
    frameCallbacks.clear();
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("prepares nested sections across one shared paint boundary", () => {
    const [ready, setReady] = createSignal(false);
    const onReady = vi.fn();
    render(() => (
      <ResidentSurface surfaceKey="stage" presence="pending" onReady={onReady}>
        <PreparedSurface name="panel">
          <ResidentSurface surfaceKey="section" presence="active">
            <PreparedSurface name="body" ready={ready}>
              <span>Complete content</span>
            </PreparedSurface>
          </ResidentSurface>
        </PreparedSurface>
      </ResidentSurface>
    ));
    flushPaint();
    expect(onReady).not.toHaveBeenCalled();
    setReady(true);
    flushPaint(2);
    expect(onReady).toHaveBeenCalledOnce();
    expect(screen.getAllByTestId("resident-surface").every((el) => el.dataset.presentation === "published")).toBe(true);
  });

  it("keeps idle surfaces mounted and inert", () => {
    const [active, setActive] = createSignal<string | null>("a");
    render(() => {
      const stack = useResidentStack(active);
      return (
        <For each={stack.keys()}>
          {(key) => (
            <ResidentSurface
              surfaceKey={key}
              presence={stack.presence(key)}
              retained={
                stack.pending() !== null && stack.presence(key) === "active"
              }
              onReady={stack.markReady}
            >
              <div data-testid={`body-${key}`} data-boot="ready">
                {key}
              </div>
            </ResidentSurface>
          )}
        </For>
      );
    });
    setActive("b");
    flushPaint();
    expect(screen.getByTestId("body-a")).toBeTruthy();
    expect(screen.getByTestId("body-b")).toBeTruthy();
    const idle = screen
      .getAllByTestId("resident-surface")
      .find((el) => el.getAttribute("data-resident-key") === "a");
    expect(idle?.getAttribute("data-resident")).toBe("idle");
    expect(idle?.classList.contains("den-resident-surface--idle")).toBe(true);
    expect(idle?.getAttribute("aria-hidden")).toBe("true");
    expect((idle as HTMLElement | undefined)?.inert).toBe(true);
  });

  it("holds the outgoing surface until pending boot becomes ready", () => {
    const [active, setActive] = createSignal<string | null>("a");
    const [boot, setBoot] = createSignal<"pending" | "ready">("pending");
    render(() => {
      const stack = useResidentStack(active);
      return (
        <For each={stack.keys()}>
          {(key) => (
            <ResidentSurface
              surfaceKey={key}
              presence={stack.presence(key)}
              retained={
                stack.pending() !== null && stack.presence(key) === "active"
              }
              onReady={stack.markReady}
            >
              <BootBody id={key} boot={boot} />
            </ResidentSurface>
          )}
        </For>
      );
    });
    setActive("b");
    flushPaint();
    const a = () =>
      screen
        .getAllByTestId("resident-surface")
        .find((el) => el.getAttribute("data-resident-key") === "a");
    const b = () =>
      screen
        .getAllByTestId("resident-surface")
        .find((el) => el.getAttribute("data-resident-key") === "b");
    expect(a()?.getAttribute("data-resident")).toBe("active");
    expect(a()?.getAttribute("data-retained")).toBe("false");
    expect(a()?.getAttribute("aria-hidden")).toBe("true");
    expect((a() as HTMLElement | undefined)?.inert).toBe(true);
    expect(b()?.getAttribute("data-resident")).toBe("pending");
    expect(b()?.getAttribute("aria-hidden")).toBe("true");
    expect((b() as HTMLElement | undefined)?.inert).toBe(true);
    vi.advanceTimersByTime(PRESENTATION_LOADING_GRACE_MS);
    expect(a()?.getAttribute("data-retained")).toBe("true");
    expect(screen.getByRole("status").textContent).toBe("Loading…");
    setBoot("ready");
    flushPaint();
    expect(a()?.getAttribute("data-resident")).toBe("idle");
    expect(b()?.getAttribute("data-resident")).toBe("active");
    expect(b()?.getAttribute("data-retained")).toBe("false");
  });

  it("prepares a returning surface again when its idle content went stale", () => {
    const [active, setActive] = createSignal<string | null>("a");
    const [boot, setBoot] = createSignal<"pending" | "ready">("ready");
    render(() => {
      const stack = useResidentStack(active);
      return (
        <For each={stack.keys()}>
          {(key) => (
            <ResidentSurface
              surfaceKey={key}
              presence={stack.presence(key)}
              onReady={stack.markReady}
              onWithdrawn={stack.withdraw}
            >
              <BootBody id={key} boot={boot} />
            </ResidentSurface>
          )}
        </For>
      );
    });
    const surface = (key: string) =>
      screen.getAllByTestId("resident-surface").find((el) => el.getAttribute("data-resident-key") === key);
    setActive("b");
    flushPaint();
    setActive("a");
    expect(surface("a")?.getAttribute("data-resident")).toBe("active");

    setBoot("pending");
    setActive("b");
    flushPaint();
    expect(surface("a")?.getAttribute("data-resident")).toBe("active");
    expect(surface("b")?.getAttribute("data-resident")).toBe("pending");
    setBoot("ready");
    flushPaint();
    expect(surface("a")?.getAttribute("data-resident")).toBe("idle");
    expect(surface("b")?.getAttribute("data-resident")).toBe("active");
  });

  it("does not remount children when presence flips idle and back", () => {
    const [active, setActive] = createSignal<string | null>("a");
    let mounts = 0;
    render(() => {
      const stack = useResidentStack(active);
      return (
        <For each={stack.keys()}>
          {(key) => (
            <ResidentSurface
              surfaceKey={key}
              presence={stack.presence(key)}
              onReady={stack.markReady}
            >
              <MountProbe
                id={key}
                onMountCount={() => {
                  mounts += 1;
                }}
              />
            </ResidentSurface>
          )}
        </For>
      );
    });
    expect(mounts).toBe(1);
    setActive("b");
    flushPaint();
    expect(mounts).toBe(2);
    setActive("a");
    expect(screen.getByTestId("body-a").getAttribute("data-boot")).toBe("ready");
    expect(mounts).toBe(2);
  });

  it("pins a parked surface to its last size and releases it on return", () => {
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue(
      DOMRect.fromRect({ width: 640, height: 480 }),
    );
    const [presence, setPresence] = createSignal<"active" | "idle">("active");
    render(() => (
      <ResidentSurface surfaceKey="chat" presence={presence()}>
        <div data-testid="body-chat" />
      </ResidentSurface>
    ));
    const surface = screen.getByTestId("resident-surface");
    expect(surface.style.getPropertyValue("--den-parked-width")).toBe("");

    setPresence("idle");
    expect(surface.style.getPropertyValue("--den-parked-width")).toBe("640px");
    expect(surface.style.getPropertyValue("--den-parked-height")).toBe("480px");

    setPresence("active");
    expect(surface.style.getPropertyValue("--den-parked-width")).toBe("");
    expect(surface.style.getPropertyValue("--den-parked-height")).toBe("");
  });

  it("provides each retained child its live presence", () => {
    const [active, setActive] = createSignal<string | null>("a");
    render(() => {
      const stack = useResidentStack(active);
      return (
        <For each={stack.keys()}>
          {(key) => (
            <ResidentSurface
              surfaceKey={key}
              presence={stack.presence(key)}
              onReady={stack.markReady}
            >
              <PresenceProbe id={key} />
            </ResidentSurface>
          )}
        </For>
      );
    });
    expect(screen.getByTestId("presence-a").textContent).toBe("active");

    setActive("b");
    flushPaint();

    expect(screen.getByTestId("presence-a").textContent).toBe("idle");
    expect(screen.getByTestId("presence-b").textContent).toBe("active");
  });

  it("propagates a parent's pending and idle state through retained nested sections", () => {
    const [parent, setParent] = createSignal<"active" | "pending" | "idle">("pending");
    const Probe = () => {
      const presence = useResidentPresence();
      const live = useResidentLive();
      return <div data-testid="nested-presence" data-live={live()}>{presence()}</div>;
    };
    render(() => <ResidentSurface surfaceKey="workspace" presence={parent()}>
      <ResidentSurface surfaceKey="section" presence="active"><Probe /></ResidentSurface>
    </ResidentSurface>);
    flushPaint();
    const nested = screen.getByTestId("nested-presence");
    expect(nested.textContent).toBe("pending");
    expect(nested.getAttribute("data-live")).toBe("true");
    setParent("active");
    flushPaint();
    expect(nested.textContent).toBe("active");
    setParent("idle");
    expect(nested.textContent).toBe("idle");
    expect(nested.getAttribute("data-live")).toBe("false");
    expect((nested.closest(".den-resident-surface") as HTMLElement).inert).toBe(true);
  });
});
