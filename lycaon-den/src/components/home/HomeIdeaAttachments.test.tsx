// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { resetAppStateSnapshotForTests } from "../../store/app-state-snapshot.ts";
import { syncChatPrefsFromSnapshot } from "../../settings/chat/chat-prefs.ts";
import { HomeView } from "./HomeView.tsx";
import type { FrozenComposition } from "../../home/home-composition.ts";
import type { SessionMode } from "../../api/types.ts";
import { homeLaunches, resetHomeLaunchesForTest, readLaunchBytes } from "../../home/launch/home-launches.ts";
import { memoryHomeOutbox } from "../../home/launch/home-outbox.ts";

type Submitted = FrozenComposition & { mode: SessionMode };
import { loaded } from "../../store/load-state.ts";
import { createSignal } from "solid-js";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import type { ResidentPresence } from "../../ui/resident-surfaces.ts";

let outbox = memoryHomeOutbox();
beforeEach(() => {
  resetAppStateSnapshotForTests();
  syncChatPrefsFromSnapshot();
  outbox = memoryHomeOutbox();
  resetHomeLaunchesForTest(outbox);
});

afterEach(() => {
  cleanup();
});

const homeHandlers = {
  onSubmitIdea: () => undefined,
  onOpenFolder: () => undefined,
  onCloneRepo: () => undefined,
  onOpenProject: () => undefined,
  onToggleStar: () => undefined,
  onRename: () => undefined,
  onAttachFolder: () => undefined,
  onPromote: () => undefined,
  onDelete: () => undefined,
};

function webFile(name: string, bytes: number, mime = "text/plain"): File {
  return new File([new Uint8Array(bytes)], name, { type: mime });
}

/** jsdom has no DragEvent; hand-build one the HTML5 intake understands. */
function dragEvent(type: string, files: File[]): Event {
  const event = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(event, "dataTransfer", {
    value: { types: ["Files"], files, dropEffect: "none" },
  });
  Object.defineProperty(event, "clientX", { value: 10 });
  Object.defineProperty(event, "clientY", { value: 12 });
  return event;
}

function pasteEvent(files: File[]): Event {
  const event = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "clipboardData", {
    value: {
      items: files.map((file) => ({ kind: "file", getAsFile: () => file })),
    },
  });
  return event;
}

function renderHome(overrides: Partial<Parameters<typeof HomeView>[0]> = {}) {
  return render(() => (
    <HomeView
      summaries={[]}
      section="recents"
      registry={loaded(null)}
      {...homeHandlers}
      {...overrides}
    />
  ));
}

describe("Home idea drop intake", () => {
  it("shows the drop affordance while dragging and buffers the dropped file", async () => {
    renderHome();
    const frame = screen.getByTestId("home-view");

    frame.dispatchEvent(dragEvent("dragenter", [webFile("notes.txt", 12)]));
    expect(screen.getByTestId("home-drop-overlay")).toBeTruthy();

    frame.dispatchEvent(dragEvent("drop", [webFile("notes.txt", 12)]));
    expect(screen.queryByTestId("home-drop-overlay")).toBeNull();

    const chip = await screen.findByTestId("home-idea-attachment-chip");
    expect(chip.textContent).toContain("notes.txt");
    expect(screen.getByTestId("home-idea-attach-count").textContent).toBe(
      "1 attached",
    );
  });

  it("buffers pasted files from the idea input", async () => {
    renderHome();
    const input = screen.getByTestId("home-idea-input");
    input.dispatchEvent(pasteEvent([webFile("clip.txt", 8)]));
    const chip = await screen.findByTestId("home-idea-attachment-chip");
    expect(chip.textContent).toContain("clip.txt");
  });

  it("removes one chip and clears the rest", async () => {
    renderHome();
    const frame = screen.getByTestId("home-view");
    frame.dispatchEvent(
      dragEvent("drop", [webFile("a.txt", 8), webFile("b.txt", 8)]),
    );
    await waitFor(() =>
      expect(screen.getAllByTestId("home-idea-attachment-chip")).toHaveLength(2),
    );

    fireEvent.click(screen.getAllByTestId("home-idea-attachment-remove")[0]!);
    await waitFor(() =>
      expect(screen.getAllByTestId("home-idea-attachment-chip")).toHaveLength(1),
    );

    fireEvent.click(screen.getByTestId("home-idea-attach-clear"));
    await waitFor(() =>
      expect(screen.queryByTestId("home-idea-attachment-chip")).toBeNull(),
    );
  });

  it("sends a rejected chip with the idea so the chat can ask for it", async () => {
    const onSubmitIdea = vi.fn<(draft: Submitted) => void>();
    renderHome({ onSubmitIdea });
    const frame = screen.getByTestId("home-view");
    frame.dispatchEvent(dragEvent("drop", [webFile("empty.txt", 0)]));
    await screen.findByTestId("home-idea-attachment-chip");

    const input = screen.getByTestId("home-idea-input") as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "Build it anyway" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onSubmitIdea).toHaveBeenCalledOnce();
    expect(onSubmitIdea.mock.calls[0]![0].attachments[0]).toMatchObject({ kind: "reject", name: "empty.txt" });
  });

  it("stages dropped bytes into the outbox at intake", async () => {
    renderHome();
    screen.getByTestId("home-view").dispatchEvent(dragEvent("drop", [webFile("shot.png", 24, "image/png")]));
    const chip = await screen.findByTestId("home-idea-attachment-chip");
    expect(chip).toBeTruthy();
    await waitFor(() => expect(outbox.blobs.size).toBe(1));
    const [itemId] = [...outbox.blobs.keys()];
    expect((await readLaunchBytes(itemId!))?.size).toBe(24);
    expect(homeLaunches.pending()).toEqual([]);
  });

  it("submits the idea with its buffered attachments", async () => {
    const onSubmitIdea = vi.fn<(draft: Submitted) => void>();
    renderHome({ onSubmitIdea });
    const frame = screen.getByTestId("home-view");
    frame.dispatchEvent(dragEvent("drop", [webFile("shot.png", 24, "image/png")]));
    await screen.findByTestId("home-idea-attachment-chip");

    const input = screen.getByTestId("home-idea-input") as HTMLTextAreaElement;
    fireEvent.click(screen.getByRole("radio", { name: "Ask" }));
    fireEvent.input(input, { target: { value: "Make it look like this" } });
    fireEvent.click(screen.getByTestId("home-idea-send"));

    expect(onSubmitIdea).toHaveBeenCalledTimes(1);
    const draft = onSubmitIdea.mock.calls[0]![0];
    expect(draft.text).toBe("Make it look like this");
    expect(draft.mode).toBe("ask");
    expect(draft.attachments).toHaveLength(1);
    expect(draft.attachments[0]).toMatchObject({
      kind: "web-file",
      name: "shot.png",
    });
  });

  it("binds the local Ask selection when immediately submitting the Home idea", () => {
    const onSubmitIdea = vi.fn<(draft: Submitted) => void>();
    renderHome({ onSubmitIdea });
    fireEvent.click(screen.getByRole("radio", { name: "Ask" }));
    const input = screen.getByTestId("home-idea-input") as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "How do I get started?" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onSubmitIdea.mock.calls[0]?.[0]).toMatchObject({ text: "How do I get started?", mode: "ask", attachments: [] });
  });

  it("allows an attachment-only submission", async () => {
    const onSubmitIdea = vi.fn<(draft: Submitted) => void>();
    renderHome({ onSubmitIdea });
    const frame = screen.getByTestId("home-view");
    frame.dispatchEvent(dragEvent("drop", [webFile("spec.txt", 16)]));
    await screen.findByTestId("home-idea-attachment-chip");

    fireEvent.click(screen.getByTestId("home-idea-send"));
    expect(onSubmitIdea).toHaveBeenCalledTimes(1);
    expect(onSubmitIdea.mock.calls[0]![0].text).toBe("");
  });

  it("takes drops again when Home returns from being parked", () => {
    const [presence, setPresence] = createSignal<ResidentPresence>("idle");
    render(() => (
      <ResidentPresenceProvider presence={presence()}>
        <HomeView summaries={[]} section="recents" registry={loaded(null)} {...homeHandlers} />
      </ResidentPresenceProvider>
    ));
    const frame = screen.getByTestId("home-view");

    frame.dispatchEvent(dragEvent("dragenter", [webFile("notes.txt", 12)]));
    expect(screen.queryByTestId("home-drop-overlay")).toBeNull();

    setPresence("active");
    frame.dispatchEvent(dragEvent("dragenter", [webFile("notes.txt", 12)]));
    expect(screen.getByTestId("home-drop-overlay")).toBeTruthy();
  });
});
