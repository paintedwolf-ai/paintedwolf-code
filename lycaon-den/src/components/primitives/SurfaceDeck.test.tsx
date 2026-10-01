// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { batch, createSignal, onMount } from "solid-js";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { usePresentationParticipant } from "../../ui/presentation-context.tsx";
import { createResidentActivity } from "../../ui/resident-activity.ts";
import { SurfaceDeck } from "./SurfaceDeck.tsx";
import { ContextMenu } from "../ContextMenu.tsx";

describe("prepared tabs", () => {
  const frames = new Map<number, FrameRequestCallback>();
  let nextFrame = 0;
  const paint = () => {
    for (let i = 0; i < 4; i++) {
      const callbacks = [...frames.values()];
      frames.clear();
      for (const callback of callbacks) callback(performance.now());
    }
  };
  const surface = (key: string) => screen.getAllByTestId("resident-surface")
    .find((element) => element.dataset.residentKey === key)!;

  beforeEach(() => {
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      frames.set(++nextFrame, callback);
      return nextFrame;
    });
    vi.stubGlobal("cancelAnimationFrame", (id: number) => frames.delete(id));
  });
  afterEach(() => { cleanup(); frames.clear(); vi.unstubAllGlobals(); });

  function Tab(props: { id: string; ready: boolean; mounted: () => void; activity: Set<string> }) {
    usePresentationParticipant("tab-content", () => props.ready);
    onMount(props.mounted);
    createResidentActivity(() => {
      props.activity.add(props.id);
      return () => { props.activity.delete(props.id); };
    });
    return <input aria-label={`Draft ${props.id}`} value="initial" />;
  }

  it("reports the active key as publishing until its paint displays it", () => {
    const [active, select] = createSignal("a");
    const [ready, settle] = createSignal(false);
    const publishing: (string | null)[] = [];
    const activity = new Set<string>();
    render(() => <SurfaceDeck active={active()} onPublishingChange={(key) => publishing.push(key)}>{(key) =>
      <Tab id={key} ready={key === "a" || ready()} mounted={() => {}} activity={activity} />
    }</SurfaceDeck>);
    expect(publishing.at(-1)).toBe("a");
    paint();
    expect(publishing.at(-1)).toBeNull();
    select("b");
    expect(publishing.at(-1)).toBe("b");
    settle(true);
    expect(publishing.at(-1)).toBe("b");
    paint();
    expect(publishing.at(-1)).toBeNull();
  });

  it("publishes any tab kind only after preparation and retains its local state", () => {
    const [active, select] = createSignal("a");
    const [ready, settle] = createSignal(false);
    const mounted = vi.fn();
    const activity = new Set<string>();
    render(() => <SurfaceDeck active={active()}>{(key) =>
      <Tab id={key} ready={key === "a" || ready()} mounted={mounted} activity={activity} />
    }</SurfaceDeck>);
    paint();
    const input = screen.getByLabelText("Draft a") as HTMLInputElement;
    fireEvent.input(input, { target: { value: "continued typing" } });
    select("b");
    paint();
    expect(surface("a").dataset.resident).toBe("active");
    expect(surface("b").dataset.presentation).toBe("preparing");
    expect(surface("b").getAttribute("aria-hidden")).toBe("true");
    settle(true);
    expect(surface("a").dataset.resident).toBe("active");
    paint();
    expect(surface("b").dataset.resident).toBe("active");
    expect(activity).toEqual(new Set(["b"]));
    select("a");
    expect(screen.getByLabelText("Draft a")).toBe(input);
    expect(input.value).toBe("continued typing");
    expect(mounted).toHaveBeenCalledTimes(2);
    expect(activity).toEqual(new Set(["a"]));
  });

  it("cannot reuse readiness from an absent suspended payload", () => {
    const [active, select] = createSignal("a");
    const [generation, replace] = createSignal(1);
    const [available, mount] = createSignal(true);
    const [ready, settle] = createSignal(true);
    render(() => <SurfaceDeck active={active()} generation={key => key === "a" ? generation() : 0}
      payloadAvailable={key => key !== "a" || available()}>{key =>
      <Tab id={key} ready={key !== "a" || ready()} mounted={() => {}} activity={new Set()} />
    }</SurfaceDeck>);
    paint();
    select("b"); paint();
    batch(() => { replace(2); mount(false); });
    select("a"); paint();
    expect(surface("b").dataset.resident).toBe("active");
    expect(surface("a").dataset.presentation).toBe("preparing");
    batch(() => { settle(false); mount(true); });
    paint();
    expect(surface("b").dataset.resident).toBe("active");
    settle(true); paint();
    expect(surface("a").dataset.resident).toBe("active");
  });

  it("holds a replaced preview after its buffer is removed until the incoming tab paints", () => {
    const [active, select] = createSignal("a");
    const [keys, setKeys] = createSignal(["a"]);
    const [ready, settle] = createSignal(false);
    const activity = new Set<string>();
    const displayed = vi.fn();
    render(() => <SurfaceDeck active={active()} retain={(key) => keys().includes(key)} retainOutgoing
      generation={(key) => keys().includes(key) ? 1 : 0}
      payloadAvailable={(key) => keys().includes(key)} onDisplayedChange={displayed}>{(key) => {
      if (!keys().includes(key)) return null;
      return <Tab id={key} ready={key === "a" || ready()} mounted={() => {}} activity={activity} />;
    }}</SurfaceDeck>);
    paint();
    expect(surface("a").dataset.presentation).toBe("published");
    batch(() => { setKeys(["b"]); select("b"); });
    expect(surface("a").dataset.presentation).toBe("published");
    paint();
    expect(surface("a").dataset.resident).toBe("active");
    expect(surface("a").dataset.presentation).toBe("published");
    expect(displayed).toHaveBeenLastCalledWith("a");
    expect(surface("b").dataset.resident).toBe("pending");
    expect(screen.getByLabelText("Draft a")).toBeTruthy();
    settle(true);
    expect(surface("a").dataset.resident).toBe("active");
    expect(surface("a").dataset.presentation).toBe("published");
    paint();
    expect(screen.queryByLabelText("Draft a")).toBeNull();
    expect(surface("b").dataset.resident).toBe("active");
    expect(displayed).toHaveBeenLastCalledWith("b");
  });

  it("does not publish a superseded tab when its data arrives late", () => {
    const [active, select] = createSignal("a");
    const [ready, settle] = createSignal(false);
    render(() => <SurfaceDeck active={active()}>{(key) =>
      <Tab id={key} ready={key !== "b" || ready()} mounted={() => {}} activity={new Set()} />
    }</SurfaceDeck>);
    paint(); select("b"); paint(); select("c"); paint();
    settle(true); paint();
    expect(surface("c").dataset.resident).toBe("active");
    expect(surface("b").dataset.resident).toBe("idle");
  });

  it("suspends portaled menus while their tab is retained and restores them on return", async () => {
    const [active, select] = createSignal("a");
    const [interactive, setInteractive] = createSignal(true);
    const chosen = vi.fn();
    const dismissed = vi.fn();
    const anchor = document.createElement("input");
    document.body.appendChild(anchor);
    anchor.focus();
    render(() => <SurfaceDeck active={active()} interactive={interactive()}>{(key) => <>
      <Tab id={key} ready={key === "a"} mounted={() => {}} activity={new Set()} />
      <ContextMenu anchor={anchor} retainFocus onDismiss={dismissed}
        items={[{ label: `Choose ${key}`, onSelect: chosen }]} />
    </>}</SurfaceDeck>);
    expect(screen.queryByRole("menu")).toBeNull();
    paint();
    await Promise.resolve();
    const menu = screen.getByRole("menu");
    setInteractive(false);
    expect(surface("a").dataset.resident).toBe("active");
    expect(screen.queryByRole("menu")).toBeNull();
    setInteractive(true);
    await Promise.resolve();
    expect(screen.getByRole("menu")).toBe(menu);
    select("b");
    paint();
    expect(surface("a").dataset.resident).toBe("active");
    expect(screen.queryByRole("menu")).toBeNull();
    fireEvent.keyDown(document, { key: "Enter" });
    fireEvent.keyDown(document, { key: "Escape" });
    expect(chosen).not.toHaveBeenCalled();
    expect(dismissed).not.toHaveBeenCalled();
    select("a");
    await Promise.resolve();
    expect(screen.getByRole("menu")).toBe(menu);
    fireEvent.keyDown(document, { key: "Enter" });
    expect(chosen).toHaveBeenCalledOnce();
    anchor.remove();
  });
});
