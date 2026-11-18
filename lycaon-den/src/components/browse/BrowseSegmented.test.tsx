import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { BrowseSegmented } from "./BrowseSegmented.tsx";
import { DenField } from "../primitives/DenField.tsx";

function stubRect(
  el: Element,
  rect: Pick<DOMRect, "left" | "top" | "width" | "height">,
): void {
  vi.spyOn(el, "getBoundingClientRect").mockReturnValue({
    x: rect.left,
    y: rect.top,
    width: rect.width,
    height: rect.height,
    top: rect.top,
    left: rect.left,
    right: rect.left + rect.width,
    bottom: rect.top + rect.height,
    toJSON: () => ({}),
  } as DOMRect);
}

describe("BrowseSegmented", () => {
  it("names each option independently of an enclosing field label", () => {
    const change = vi.fn();
    render(() => <DenField label="Transport"><BrowseSegmented
      ariaLabel="Transport" value="http" onChange={change}
      options={[{ id: "http", label: "HTTP" }, { id: "stdio", label: "Local command", ariaLabel: "Standard input and output" }]}
    /></DenField>);
    const http = screen.getByRole("button", { name: "HTTP" });
    const stdio = screen.getByRole("button", { name: "Standard input and output" });
    expect(http.getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(stdio);
    expect(change).toHaveBeenCalledWith("stdio");
    expect(screen.queryAllByRole("button", { name: "Transport" })).toHaveLength(0);
  });

  afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers(); });

  it("keeps exclusive selection with on-state class", () => {
    const [value, setValue] = createSignal("all");
    render(() => (
      <BrowseSegmented
        class="probe-one probe-two"
        testId="kind-filter"
        ariaLabel="Unit kind filter"
        value={value()}
        onChange={setValue}
        options={[
          { id: "all", label: "All", testId: "extensions-kind-all" },
          { id: "policy", label: "Policy", testId: "extensions-kind-policy" },
        ]}
      />
    ));
    const filter = screen.getByTestId("kind-filter");
    expect(filter.classList.contains("probe-one")).toBe(true);
    expect(filter.classList.contains("probe-two")).toBe(true);
    expect(
      screen.getByTestId("extensions-kind-all").classList.contains("den-browse-segment--on"),
    ).toBe(true);
    fireEvent.click(screen.getByTestId("extensions-kind-policy"));
    expect(
      screen.getByTestId("extensions-kind-policy").classList.contains("den-browse-segment--on"),
    ).toBe(true);
    expect(
      screen.getByTestId("extensions-kind-all").classList.contains("den-browse-segment--on"),
    ).toBe(false);
  });

  it("does not fire onChange when clicking the already-selected segment", () => {
    const onChange = vi.fn();
    render(() => (
      <BrowseSegmented
        ariaLabel="filter"
        value="all"
        onChange={onChange}
        options={[{ id: "all", label: "All", testId: "seg-all" }]}
      />
    ));
    fireEvent.click(screen.getByTestId("seg-all"));
    expect(onChange).not.toHaveBeenCalled();
  });

  it("uses a radio tab stop, skips disabled choices and supports Home and End", () => {
    const [value, setValue] = createSignal("short");
    const options = [
      { id: "short", label: "Short" },
      { id: "unavailable", label: "Unavailable", disabled: true },
      { id: "long", label: "Long" },
    ];
    render(() => <BrowseSegmented
      ariaLabel="Length" semantics="radio" value={value()} onChange={setValue} options={options}
    />);
    const short = screen.getByRole("radio", { name: "Short" });
    const unavailable = screen.getByRole("radio", { name: "Unavailable" });
    const long = screen.getByRole("radio", { name: "Long" });
    expect(screen.getByRole("radiogroup", { name: "Length" })).toBeTruthy();
    expect(unavailable.getAttribute("aria-disabled")).toBe("true");
    fireEvent.click(unavailable);
    expect(short.tabIndex).toBe(0);

    short.focus();
    fireEvent.keyDown(short, { key: "ArrowDown" });
    expect(document.activeElement).toBe(long);
    expect(long.tabIndex).toBe(0);
    expect(short.tabIndex).toBe(-1);
    fireEvent.keyDown(long, { key: "Home" });
    expect(short.getAttribute("aria-checked")).toBe("true");
    fireEvent.keyDown(short, { key: "End" });
    expect(long.getAttribute("aria-checked")).toBe("true");
  });

  it("snaps the initially selected thumb after a hidden track gets a size", () => {
    vi.useFakeTimers();
    let resize: ResizeObserverCallback | undefined;
    vi.stubGlobal("ResizeObserver", class {
      constructor(callback: ResizeObserverCallback) { resize = callback; }
      observe() {}
      disconnect() {}
    });

    const settled = vi.fn();
    const [value, setValue] = createSignal("ask");
    render(() => <div style={{ display: "none" }} data-testid="hidden">
      <BrowseSegmented
        ariaLabel="Session mode" semantics="radio" value={value()} onChange={setValue}
        onSelectionSettled={settled}
        options={[{ id: "agent", label: "Agent" }, { id: "ask", label: "Ask" }]}
      />
    </div>);
    const track = screen.getByRole("radiogroup", { hidden: true });
    const thumb = track.querySelector(".den-browse-segment-thumb") as HTMLElement;
    const ask = track.querySelector<HTMLElement>('[data-value="ask"]')!;
    const agent = track.querySelector<HTMLElement>('[data-value="agent"]')!;
    const animation = { cancel: vi.fn(), onfinish: null } as unknown as Animation;
    const animate = vi.fn(() => animation);
    Object.defineProperty(thumb, "animate", { value: animate });
    vi.advanceTimersToNextFrame();
    expect(settled).not.toHaveBeenCalled();

    screen.getByTestId("hidden").style.display = "block";
    stubRect(track, { left: 100, top: 50, width: 96, height: 24 });
    stubRect(ask, { left: 151, top: 52, width: 43, height: 20 });
    stubRect(agent, { left: 102, top: 52, width: 47, height: 20 });
    resize?.([], {} as ResizeObserver);
    vi.advanceTimersToNextFrame();

    expect(ask.getAttribute("aria-checked")).toBe("true");
    expect(thumb.style.transform).toBe("translate(51px, 2px)");
    expect(thumb.style.width).toBe("43px");
    expect(thumb.style.height).toBe("20px");
    expect(animate).not.toHaveBeenCalled();
    expect(settled).toHaveBeenCalledExactlyOnceWith("ask");

    setValue("agent");
    vi.advanceTimersToNextFrame();
    expect(animate).toHaveBeenCalledOnce();
    expect(settled).toHaveBeenCalledTimes(1);
    animation.onfinish?.call(animation, new Event("finish") as AnimationPlaybackEvent);
    expect(settled).toHaveBeenLastCalledWith("agent");
  });

  it("moves the selection thumb to the selected segment", () => {
    vi.useFakeTimers();
    const [value, setValue] = createSignal("all");
    render(() => (
      <BrowseSegmented
        testId="kind-filter"
        ariaLabel="Unit kind filter"
        value={value()}
        onChange={setValue}
        options={[
          { id: "all", label: "All", testId: "seg-all" },
          { id: "policy", label: "Policy", testId: "seg-policy" },
        ]}
      />
    ));
    const track = screen.getByTestId("kind-filter");
    const thumb = track.querySelector(
      ".den-browse-segment-thumb",
    ) as HTMLElement;
    const all = screen.getByTestId("seg-all");
    const policy = screen.getByTestId("seg-policy");
    Object.defineProperties(track, {
      clientLeft: { value: 1, configurable: true },
      clientTop: { value: 1, configurable: true },
      scrollLeft: { value: 0, configurable: true },
      scrollTop: { value: 0, configurable: true },
    });
    stubRect(track, { left: 100, top: 50, width: 200, height: 30 });
    stubRect(all, { left: 103, top: 53, width: 80, height: 24 });
    stubRect(policy, { left: 185, top: 53, width: 112, height: 24 });

    setValue("policy");
    vi.advanceTimersToNextFrame();
    setValue("all");
    vi.advanceTimersToNextFrame();
    // The box matches the segment; CSS insets the painted fill.
    expect(thumb.style.transform).toBe("translate(2px, 2px)");
    expect(thumb.style.width).toBe("80px");
    expect(thumb.style.height).toBe("24px");
    expect(thumb.style.opacity).toBe("1");
    setValue("policy");
    vi.advanceTimersToNextFrame();
    expect(screen.getByTestId("seg-policy").getAttribute("aria-pressed")).toBe(
      "true",
    );
    expect(thumb.style.transform).toBe("translate(84px, 2px)");
    expect(thumb.style.width).toBe("112px");
  });

  it("retargets an active travel when the track resizes", () => {
    let resize: ResizeObserverCallback | undefined;
    let frame: FrameRequestCallback | undefined;
    class WorkingResizeObserver {
      constructor(callback: ResizeObserverCallback) {
        resize = callback;
      }
      observe() {}
      disconnect() {}
    }
    vi.stubGlobal("ResizeObserver", WorkingResizeObserver);
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      frame = callback;
      return 1;
    });
    vi.stubGlobal("cancelAnimationFrame", vi.fn());

    const [value, setValue] = createSignal("missing");
    render(() => (
      <BrowseSegmented
        testId="kind-filter"
        ariaLabel="Unit kind filter"
        value={value()}
        onChange={setValue}
        options={[
          { id: "all", label: "All", testId: "seg-all" },
          { id: "policy", label: "Policy", testId: "seg-policy" },
        ]}
      />
    ));

    const track = screen.getByTestId("kind-filter");
    const thumb = track.querySelector(
      ".den-browse-segment-thumb",
    ) as HTMLElement;
    const all = screen.getByTestId("seg-all");
    const policy = screen.getByTestId("seg-policy");
    Object.defineProperties(track, {
      clientLeft: { value: 1, configurable: true },
      clientTop: { value: 1, configurable: true },
      scrollLeft: { value: 0, configurable: true },
      scrollTop: { value: 0, configurable: true },
    });
    stubRect(track, { left: 100, top: 50, width: 200, height: 30 });
    stubRect(all, { left: 103, top: 53, width: 80, height: 24 });
    stubRect(policy, { left: 185, top: 53, width: 112, height: 24 });

    const setKeyframes = vi.fn();
    const cancel = vi.fn();
    const animation = {
      cancel,
      effect: { setKeyframes },
      onfinish: null,
    } as unknown as Animation;
    const animate = vi.fn(() => animation);
    Object.defineProperty(thumb, "animate", {
      value: animate,
      configurable: true,
    });

    setValue("all");
    frame?.(0);
    setValue("policy");
    frame?.(0);
    expect(animate).toHaveBeenCalledOnce();

    stubRect(policy, { left: 186, top: 53, width: 112, height: 24 });
    resize?.([], {} as ResizeObserver);
    frame?.(0);

    expect(cancel).not.toHaveBeenCalled();
    expect(setKeyframes).toHaveBeenCalledWith([
      { transform: "translate(2px, 2px)", width: "80px", height: "24px" },
      {
        transform: "translate(85px, 2px)",
        width: "112px",
        height: "24px",
      },
    ]);
  });
});
