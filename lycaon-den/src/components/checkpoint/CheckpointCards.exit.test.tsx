import { NoticeReporterProvider } from "../../notices/notice-reporter.tsx";
import type { NoticeReporter } from "../../notices/notice-store.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import { createAppStore } from "../../store/app-state.ts";
import type { PendingCheckpoint } from "../../chat/checkpoint/checkpoint-model.ts";
import { pendingCheckpointsForSession } from "../../chat/actions/chat-actions.ts";
import { CheckpointCards } from "./CheckpointCards.tsx";
import { ComposerChromeSlot } from "../chatview/ComposerChromeSlot.tsx";
import { mockProjectsStore } from "../../test/projects-fixture.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { toolApprovalFixture } from "../../chat/checkpoint/approval-test-fixtures.ts";

const CARD_HEIGHT_PX = 240;

const session = {
  id: "parent-1",
  owner_person_id: "00000000-0000-4000-8000-000000000002",
  project_id: "00000000-0000-4000-8000-000000000001",
  workspace_path: "/tmp/p",
  posture: "build" as const,
  status: "busy" as const,
  created_at: "t",
  activity_at: "t",
  updated_at: "t",
};

const testReporter: NoticeReporter = { reportError: () => {}, publish: () => {} };

function toolApproval(id: string): PendingCheckpoint {
  return {
    checkpointId: id,
    sessionId: "parent-1",
    kind: "tool_approval",
    status: "pending",
    issuedAt: "t",
    tool_approval: toolApprovalFixture({
      tool: "request_tools",
      title: "Approve request_tools",
      command: "mkdir",
    }),
  };
}

type AnimationRecord = {
  keyframes: Keyframe[];
  onfinish: ((this: Animation, ev: AnimationPlaybackEvent) => void) | null;
};

function flushRaf() {
  return new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
}

const animations: AnimationRecord[] = [];
const originalAnimate = Object.getOwnPropertyDescriptor(Element.prototype, "animate");
const originalGetAnimations = Object.getOwnPropertyDescriptor(Element.prototype, "getAnimations");
const originalRect = Object.getOwnPropertyDescriptor(Element.prototype, "getBoundingClientRect");
const jsdomLacksInert = !("inert" in HTMLElement.prototype);

beforeEach(() => {
  animations.length = 0;
  // Solid writes `inert` as a property; browsers reflect it to the attribute and jsdom does not.
  if (jsdomLacksInert) {
    Object.defineProperty(HTMLElement.prototype, "inert", {
      configurable: true,
      get(this: HTMLElement) {
        return this.hasAttribute("inert");
      },
      set(this: HTMLElement, value: boolean) {
        if (value) this.setAttribute("inert", "");
        else this.removeAttribute("inert");
      },
    });
  }
  Object.defineProperty(Element.prototype, "animate", {
    configurable: true,
    writable: true,
    value: (keyframes: Keyframe[]) => {
      const record: AnimationRecord = { keyframes, onfinish: null };
      animations.push(record);
      return {
        cancel: () => {},
        set onfinish(handler: AnimationRecord["onfinish"]) {
          record.onfinish = handler;
        },
        get onfinish() {
          return record.onfinish;
        },
        set oncancel(_handler: unknown) {},
      } as unknown as Animation;
    },
  });
  Object.defineProperty(Element.prototype, "getAnimations", {
    configurable: true,
    writable: true,
    value: () => [],
  });
  // Layout stands in for jsdom: a shell is as tall as the card it holds.
  Object.defineProperty(Element.prototype, "getBoundingClientRect", {
    configurable: true,
    writable: true,
    value(this: Element) {
      const height = this.querySelector('[data-testid="tool-approval-card"]') ? CARD_HEIGHT_PX : 0;
      return { x: 0, y: 0, top: 0, left: 0, right: 0, bottom: height, width: 0, height, toJSON: () => ({}) };
    },
  });
});

afterEach(() => {
  for (const [name, descriptor] of [
    ["animate", originalAnimate],
    ["getAnimations", originalGetAnimations],
    ["getBoundingClientRect", originalRect],
  ] as const) {
    if (descriptor) Object.defineProperty(Element.prototype, name, descriptor);
    else delete (Element.prototype as unknown as Record<string, unknown>)[name];
  }
  if (jsdomLacksInert) delete (HTMLElement.prototype as unknown as Record<string, unknown>).inert;
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

describe("CheckpointCards dock exit", () => {
  it("keeps the resolved card rendered and inert while the slot collapses from its height", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(session);
    const setPending = (checkpoints: PendingCheckpoint[]) =>
      appStore.actions.setPendingCheckpoints(
        "parent-1",
        appStore.state.sessionViewEpoch,
        appStore.state.checkpointEventEpoch,
        checkpoints,
      );
    setPending([toolApproval("cp-1")]);
    const client = stubClient({ resolveCheckpoint: vi.fn() });

    render(() => (
      <ComposerChromeSlot
        slot="checkpoint"
        present={pendingCheckpointsForSession(appStore, "parent-1").length > 0}
      >
        <NoticeReporterProvider reporter={testReporter}>
          <CheckpointCards
            appStore={appStore}
            client={client}
            sessionId="parent-1"
            projectDir="/tmp/p"
            projects={mockProjectsStore()}
          />
        </NoticeReporterProvider>
      </ComposerChromeSlot>
    ));
    await flushRaf();
    animations[0]?.onfinish?.call({} as Animation, new Event("finish") as AnimationPlaybackEvent);
    await flushRaf();
    const card = screen.getByTestId("tool-approval-card");
    expect(card.hasAttribute("inert")).toBe(false);
    expect((screen.getByTestId("approval-approve-primary") as HTMLButtonElement).disabled).toBe(false);

    // The host resolves the checkpoint; the store drops it in one update.
    setPending([]);

    const slot = screen.getByTestId("composer-chrome-slot-checkpoint");
    expect(slot.dataset.closing).toBe("true");
    const closing = animations.at(-1)?.keyframes ?? [];
    expect(closing[0]).toMatchObject({ height: `${CARD_HEIGHT_PX}px` });
    expect(closing.at(-1)).toMatchObject({ height: "0px" });
    expect(screen.getByTestId("tool-approval-card")).toBe(card);
    expect(card.hasAttribute("inert")).toBe(true);
    expect((screen.getByTestId("approval-approve-primary") as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByTestId("approval-no") as HTMLButtonElement).disabled).toBe(true);

    animations.at(-1)?.onfinish?.call({} as Animation, new Event("finish") as AnimationPlaybackEvent);
    await flushRaf();
    expect(screen.queryByTestId("composer-chrome-slot-checkpoint")).toBeNull();
    expect(screen.queryByTestId("tool-approval-card")).toBeNull();
  });

  it("swaps a retired card for a checkpoint that arrives during the exit", async () => {
    const appStore = createAppStore();
    appStore.actions.setCurrentSession(session);
    const setPending = (checkpoints: PendingCheckpoint[]) =>
      appStore.actions.setPendingCheckpoints(
        "parent-1",
        appStore.state.sessionViewEpoch,
        appStore.state.checkpointEventEpoch,
        checkpoints,
      );
    setPending([toolApproval("cp-1")]);

    render(() => (
      <ComposerChromeSlot
        slot="checkpoint"
        present={pendingCheckpointsForSession(appStore, "parent-1").length > 0}
      >
        <NoticeReporterProvider reporter={testReporter}>
          <CheckpointCards
            appStore={appStore}
            client={stubClient({ resolveCheckpoint: vi.fn() })}
            sessionId="parent-1"
            projectDir="/tmp/p"
            projects={mockProjectsStore()}
          />
        </NoticeReporterProvider>
      </ComposerChromeSlot>
    ));
    await flushRaf();
    animations[0]?.onfinish?.call({} as Animation, new Event("finish") as AnimationPlaybackEvent);
    await flushRaf();

    setPending([]);
    expect(screen.getByTestId("tool-approval-card").hasAttribute("inert")).toBe(true);

    setPending([toolApproval("cp-2")]);
    await flushRaf();
    const card = screen.getByTestId("tool-approval-card");
    expect(card.getAttribute("data-checkpoint-id")).toBe("cp-2");
    expect(card.hasAttribute("inert")).toBe(false);
    expect((screen.getByTestId("approval-approve-primary") as HTMLButtonElement).disabled).toBe(false);
  });
});
