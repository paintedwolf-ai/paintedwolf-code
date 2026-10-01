import { fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal, onCleanup, onMount } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { StageSplitHost } from "./StageSplitHost.tsx";

function Host(props: {
  splitLive?: boolean;
  contextHidden?: boolean;
  mirrored?: boolean;
  narrowSurvivor?: "conversation" | "stage";
}) {
  return (
    <StageSplitHost
      splitLive={props.splitLive ?? true}
      contextHidden={props.contextHidden}
      stageColumnOccupied
      chatColumnOccupied
      stageOnLeft={!props.mirrored}
      narrowSurvivor={props.narrowSurvivor ?? "conversation"}
      chatWidthPx={440}
      washed={false}
      stageColDual={false}
      chatColDual
      onStageEnter={vi.fn()}
      onChatEnter={vi.fn()}
      stageColumn={<input data-testid="stage-body" />}
      chatColumn={<input data-testid="chat-body" />}
      divider={<div data-testid="split-divider" />}
    />
  );
}

describe("StageSplitHost", () => {
  it("hides context without remounting its content", () => {
    const [hidden, setHidden] = createSignal(false);
    render(() => <Host contextHidden={hidden()} />);
    const input = screen.getByTestId("stage-body") as HTMLInputElement;
    input.value = "Retain the editor";
    setHidden(true);
    expect(screen.getByTestId("split-col-stage").getAttribute("aria-hidden")).toBe("true");
    expect(screen.getByTestId("split-col-stage").inert).toBe(true);
    setHidden(false);
    expect(screen.getByTestId("stage-body")).toBe(input);
    expect(input.value).toBe("Retain the editor");
    expect(screen.getByTestId("split-col-stage").inert).toBeFalsy();
  });

  it("keeps both views mounted in inline and split layouts", () => {
    const { unmount } = render(() => <Host splitLive={false} />);
    expect(screen.getByTestId("split-col-stage")).toBeTruthy();
    // Occupancy CSS controls the mounted columns.
    expect(screen.getByTestId("split-col-chat")).toBeTruthy();
    expect(screen.queryByTestId("split-divider")).toBeNull();
    unmount();

    render(() => <Host />);
    const host = screen.getByTestId("shell-stage-host");
    expect(host.classList.contains("den-shell-stage-host--split")).toBe(true);
    expect(host.style.getPropertyValue("--den-split-chat-w")).toBe("440px");
    expect(screen.getByTestId("split-divider")).toBeTruthy();
    expect(screen.getByTestId("split-col-chat")).toBeTruthy();
    expect(screen.getByTestId("split-col-chat").classList.contains("den-split-col--dual")).toBe(
      true,
    );
  });

  it("bounds concurrent chat surfaces to one column", () => {
    // Concurrent surfaces need the same column bounds.
    render(() => <Host splitLive={false} />);
    expect(
      screen.getByTestId("split-col-chat").classList.contains("den-split-col--dual"),
    ).toBe(true);
  });

  it("bounds both columns during an inline stage handoff", () => {
    render(() => <Host splitLive={false} />);
    expect(
      screen
        .getByTestId("shell-stage-host")
        .classList.contains("den-shell-stage-host--handoff"),
    ).toBe(true);
  });

  it("names the entered column on pointer entry, not only on focus", () => {
    const onStageEnter = vi.fn();
    const onChatEnter = vi.fn();
    render(() => (
      <StageSplitHost
        splitLive
        stageColumnOccupied
        chatColumnOccupied
        stageOnLeft={true}
        narrowSurvivor="conversation"
        chatWidthPx={440}
        washed={false}
        stageColDual={false}
        chatColDual={false}
        onStageEnter={onStageEnter}
        onChatEnter={onChatEnter}
        stageColumn={<p data-testid="stage-prose">stage</p>}
        chatColumn={<p data-testid="chat-prose">chat</p>}
        divider={<div data-testid="split-divider" />}
      />
    ));

    // Pointer entry covers non-focusable content.
    fireEvent.pointerDown(screen.getByTestId("chat-prose"));
    expect(onChatEnter).toHaveBeenCalled();
    expect(onStageEnter).not.toHaveBeenCalled();

    fireEvent.pointerDown(screen.getByTestId("stage-prose"));
    expect(onStageEnter).toHaveBeenCalled();
  });

  it("hears a column entry a child handler stops from bubbling", () => {
    const onChatEnter = vi.fn();
    render(() => (
      <StageSplitHost
        splitLive
        stageColumnOccupied
        chatColumnOccupied
        stageOnLeft={true}
        narrowSurvivor="conversation"
        chatWidthPx={440}
        washed={false}
        stageColDual={false}
        chatColDual={false}
        onStageEnter={vi.fn()}
        onChatEnter={onChatEnter}
        stageColumn={<p>stage</p>}
        chatColumn={
          // Capture observes pointer entry stopped by child controls.
          <button
            type="button"
            data-testid="chat-swallow"
            onPointerDown={(e) => e.stopPropagation()}
          >
            row
          </button>
        }
        divider={<div data-testid="split-divider" />}
      />
    ));

    fireEvent.pointerDown(screen.getByTestId("chat-swallow"));
    expect(onChatEnter).toHaveBeenCalled();
  });

  it("publishes column occupancy so an empty column can collapse in CSS", () => {
    render(() => (
      <StageSplitHost
        splitLive={false}
        stageColumnOccupied={false}
        chatColumnOccupied
        stageOnLeft={true}
        narrowSurvivor="conversation"
        chatWidthPx={440}
        washed={false}
        stageColDual={false}
        chatColDual={false}
        onStageEnter={vi.fn()}
        onChatEnter={vi.fn()}
        stageColumn={null}
        chatColumn={<div>chat</div>}
        divider={<div data-testid="split-divider" />}
      />
    ));
    const host = screen.getByTestId("shell-stage-host");
    expect(host.getAttribute("data-stage-occupied")).toBe("false");
    expect(host.getAttribute("data-chat-occupied")).toBe("true");
    expect(host.classList.contains("den-shell-stage-host--handoff")).toBe(false);
  });

  it("survives the split threshold without rebuilding the conversation", () => {
    // Threshold changes preserve the mounted conversation and its view state.
    const [splitLive, setSplitLive] = createSignal(true);
    const mounted = vi.fn();
    const disposed = vi.fn();
    const StatefulChat = () => {
      onMount(mounted);
      onCleanup(disposed);
      return <input data-testid="chat-input" />;
    };
    render(() => (
      <StageSplitHost
        splitLive={splitLive()}
        stageColumnOccupied
        chatColumnOccupied
        stageOnLeft={true}
        narrowSurvivor="conversation"
        chatWidthPx={440}
        washed={false}
        stageColDual={false}
        chatColDual={false}
        onStageEnter={vi.fn()}
        onChatEnter={vi.fn()}
        stageColumn={<div>stage</div>}
        chatColumn={<StatefulChat />}
        divider={<div data-testid="split-divider" />}
      />
    ));

    const chatCol = screen.getByTestId("split-col-chat");
    const input = screen.getByTestId("chat-input") as HTMLInputElement;
    input.value = "half-typed message";
    input.focus();

    setSplitLive(false);
    expect(screen.queryByTestId("split-divider")).toBeNull();
    expect(screen.getByTestId("split-col-chat")).toBe(chatCol);
    expect(screen.getByTestId("chat-input")).toBe(input);
    expect(input.value).toBe("half-typed message");
    expect(document.activeElement).toBe(input);

    setSplitLive(true);
    expect(screen.getByTestId("split-divider")).toBeTruthy();
    expect(screen.getByTestId("chat-input")).toBe(input);
    expect(mounted).toHaveBeenCalledOnce();
    expect(disposed).not.toHaveBeenCalled();
  });

  it("swaps physical sides without reordering, remounting, or losing focus", () => {
    const [orientation, setOrientation] = createSignal<"standard" | "mirrored">(
      "standard",
    );
    const mounted = vi.fn();
    const disposed = vi.fn();
    const StatefulChat = () => {
      onMount(mounted);
      onCleanup(disposed);
      return <input data-testid="chat-input" />;
    };
    render(() => (
      <StageSplitHost
        splitLive
        stageColumnOccupied
        chatColumnOccupied
        stageOnLeft={orientation() === "standard"}
        narrowSurvivor="conversation"
        chatWidthPx={440}
        washed={false}
        stageColDual={false}
        chatColDual={false}
        onStageEnter={vi.fn()}
        onChatEnter={vi.fn()}
        stageColumn={<div>stage</div>}
        chatColumn={<StatefulChat />}
        divider={<div data-testid="split-divider" />}
      />
    ));

    const host = screen.getByTestId("shell-stage-host");
    const chat = screen.getByTestId("split-col-chat");
    const input = screen.getByTestId("chat-input") as HTMLInputElement;
    input.value = "Keep my draft";
    input.focus();
    const orderBefore = [...host.children];

    setOrientation("mirrored");
    expect(host.getAttribute("data-stage-leading")).toBe("false");
    expect([...host.children]).toEqual(orderBefore);
    expect(screen.getByTestId("split-col-chat")).toBe(chat);
    expect(document.activeElement).toBe(input);
    expect(input.value).toBe("Keep my draft");
    expect(mounted).toHaveBeenCalledOnce();
    expect(disposed).not.toHaveBeenCalled();
  });

  it("publishes only the conversation width, never a measured stage width", () => {
    render(() => <Host />);
    const host = screen.getByTestId("shell-stage-host");
    const stage = screen.getByTestId("split-col-stage") as HTMLElement;
    // A stage width here would be a JS measurement from an earlier frame; CSS
    // gives the stage whatever the live host box leaves beside the conversation.
    expect(host.style.getPropertyValue("--den-split-chat-w")).toBe("440px");
    expect(host.style.getPropertyValue("--den-split-stage-width")).toBe("");
    expect(stage.style.width).toBe("");
  });

  it("hides the conversation inside the split without unmounting it", () => {
    const [hidden, setHidden] = createSignal<"preview" | "hidden" | null>(null);
    const mounted = vi.fn();
    const disposed = vi.fn();
    const StatefulChat = () => {
      onMount(mounted);
      onCleanup(disposed);
      return <input data-testid="chat-input" />;
    };
    render(() => (
      <StageSplitHost
        splitLive
        stageColumnOccupied
        chatColumnOccupied
        stageOnLeft={true}
        conversationHidden={hidden()}
        narrowSurvivor="conversation"
        chatWidthPx={440}
        washed={false}
        stageColDual={false}
        chatColDual={false}
        onStageEnter={vi.fn()}
        onChatEnter={vi.fn()}
        stageColumn={<div>stage</div>}
        chatColumn={<StatefulChat />}
        divider={<div data-testid="split-divider" />}
      />
    ));
    const host = screen.getByTestId("shell-stage-host");
    const chat = screen.getByTestId("split-col-chat");
    const input = screen.getByTestId("chat-input") as HTMLInputElement;
    input.value = "half-typed message";
    expect(host.hasAttribute("data-conversation-hidden")).toBe(false);

    // A drag preview keeps the column reachable and the divider under the pointer.
    setHidden("preview");
    expect(host.getAttribute("data-conversation-hidden")).toBe("preview");
    expect(chat.inert).toBeFalsy();
    expect(screen.getByTestId("split-divider")).toBeTruthy();

    setHidden("hidden");
    expect(host.getAttribute("data-conversation-hidden")).toBe("hidden");
    expect(host.classList.contains("den-shell-stage-host--split")).toBe(true);
    expect(chat.inert).toBe(true);
    expect(chat.getAttribute("aria-hidden")).toBe("true");

    setHidden(null);
    expect(chat.inert).toBeFalsy();
    expect(screen.getByTestId("split-col-chat")).toBe(chat);
    expect(screen.getByTestId("chat-input")).toBe(input);
    expect(input.value).toBe("half-typed message");
    expect(mounted).toHaveBeenCalledOnce();
    expect(disposed).not.toHaveBeenCalled();
  });

  it("declares the column a narrow host would keep, only while split", () => {
    const [survivor, setSurvivor] = createSignal<"conversation" | "stage">(
      "conversation",
    );
    const [live, setLive] = createSignal(true);
    render(() => <Host splitLive={live()} narrowSurvivor={survivor()} />);
    const host = screen.getByTestId("shell-stage-host");
    expect(host.getAttribute("data-narrow-survivor")).toBe("conversation");

    // The stylesheet reads this; nothing here measures a width to set it.
    setSurvivor("stage");
    expect(host.getAttribute("data-narrow-survivor")).toBe("stage");

    setLive(false);
    expect(host.hasAttribute("data-narrow-survivor")).toBe(false);
  });

  it("ignores a stored hide outside a live split", () => {
    render(() => (
      <StageSplitHost
        splitLive={false}
        stageColumnOccupied
        chatColumnOccupied
        stageOnLeft={true}
        conversationHidden="hidden"
        narrowSurvivor="conversation"
        chatWidthPx={440}
        washed={false}
        stageColDual={false}
        chatColDual={false}
        onStageEnter={vi.fn()}
        onChatEnter={vi.fn()}
        stageColumn={<div>stage</div>}
        chatColumn={<div>chat</div>}
        divider={<div data-testid="split-divider" />}
      />
    ));
    expect(
      screen.getByTestId("shell-stage-host").hasAttribute("data-conversation-hidden"),
    ).toBe(false);
    expect(screen.getByTestId("split-col-chat").inert).toBeFalsy();
  });
});
