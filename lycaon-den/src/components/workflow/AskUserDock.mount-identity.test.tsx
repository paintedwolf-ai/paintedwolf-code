import { stubClient } from "../../test/client-fixture.ts";
import { Show, createMemo, createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render } from "@solidjs/testing-library";
import { AskUserDock } from "./AskUserDock.tsx";
import type { AskUserDockSubmitFn } from "./AskUserDock.tsx";
import type { LycaonClient } from "../../api/client.ts";
import type {
  Message,
  PendingFeedback,
  WorkflowFeedbackMeta,
} from "../../api/types.ts";
import { createAppStore } from "../../store/app-state.ts";
import {
  askUserDockMeta,
  pendingAskFeedbackEntry,
  retainAskDockMeta,
} from "../../workflow/workflow-feedback-model.ts";

function client(): LycaonClient {
  return stubClient({
    resolveWorkflowFeedback: vi.fn().mockResolvedValue({}),
    resolveWorkflowDecision: vi.fn().mockResolvedValue({}),
    getSessionArtifact: vi
      .fn()
      .mockResolvedValue(new Blob(["x"], { type: "image/png" })),
  });
}

/**
 * The ChatView ask slot, keyed on the phase id alone. The pick lives in the dock's
 * local signals until Send, so a remount discards it; only a new question remounts.
 */
function mountAskSlot() {
  const c = client();
  const [phaseId, setPhaseId] = createSignal("ask-1");
  const [churn, setChurn] = createSignal(0);
  const ready: boolean[] = [];
  let submit: AskUserDockSubmitFn | undefined;

  const view = render(() => (
    <Show when={phaseId()} keyed>
      {(key) => (
        <AskUserDock
          meta={{
            phase_id: key,
            // Unrelated wire churn rides in on a reactive prop, not a new key.
            prompt: `Pick one (${churn()})`,
            response_type: "single_choice",
            options: ["red", "blue"],
          }}
          client={c}
          sessionId="sess-1"
          runId="run-1"
          runRevision={7}
          registerSubmit={(fn) => {
            submit = fn;
          }}
          onAnswerReadyChange={(next) => ready.push(next)}
        />
      )}
    </Show>
  ));

  return {
    ...view,
    c,
    setPhaseId,
    setChurn,
    ready,
    submit: (text: string) => submit!(text),
  };
}

describe("AskUserDock mount identity", () => {
  it("keeps an unsent pick across unrelated reactive churn", async () => {
    const slot = mountAskSlot();
    fireEvent.click(slot.getByLabelText("blue"));
    expect(slot.ready[slot.ready.length - 1]).toBe(true);

    // A message append / run publish reaches the dock as a prop update.
    slot.setChurn(1);
    expect(slot.getByTestId("workflow-feedback-prompt").textContent).toContain(
      "Pick one (1)",
    );
    expect(slot.ready[slot.ready.length - 1]).toBe(true);

    await slot.submit("");
    expect(slot.c.resolveWorkflowDecision).toHaveBeenCalledWith(
      "run-1",
      "ask-1",
      { expected_revision: 7, choices: ["blue"] },
    );
  });

  it("drops the pick when the question itself changes", () => {
    const slot = mountAskSlot();
    fireEvent.click(slot.getByLabelText("blue"));
    expect(slot.ready[slot.ready.length - 1]).toBe(true);

    slot.setPhaseId("ask-2");
    expect(slot.ready[slot.ready.length - 1]).toBe(false);
    expect((slot.getByLabelText("blue") as HTMLInputElement).checked).toBe(
      false,
    );
  });
});

const PENDING: PendingFeedback = {
  phase_id: "ask-1",
  prompt: "Pick one",
  response_type: "single_choice",
  options: ["red", "blue"],
};

/**
 * The real ChatView ask slot, memo for memo: transcript row -> dock meta -> the
 * retained object the keyed `<Show>` is keyed on. Answering stamps the row with an
 * optimistic answer, which drops it from `pendingAskFeedbackEntry` and pushes
 * `askUserDockMeta` onto its fresh-object-literal branch — so without retention the
 * dock is disposed mid-submit and the failure lands nowhere.
 */
function mountChatViewAskSlot(resolveWorkflowDecision: () => Promise<unknown>) {
  const appStore = createAppStore();
  const row: Message = {
    id: "fb-msg",
    role: "system",
    origin: "host",
    authority: "system",
    trust_tier: "trusted",
    kind: "workflow_feedback",
    content: PENDING.prompt,
    workflow_run_id: "run-1",
    workflow_feedback: {
      phase_id: PENDING.phase_id,
      prompt: PENDING.prompt,
      response_type: "single_choice",
      options: ["red", "blue"],
    },
    created_at: "2026-09-01T00:00:00Z",
  };
  appStore.actions.upsertMessage(row);

  const c = stubClient({
    resolveWorkflowDecision: vi.fn(resolveWorkflowDecision),
    resolveWorkflowFeedback: vi.fn().mockResolvedValue({}),
  });
  let submit: AskUserDockSubmitFn | undefined;

  const view = render(() => {
    const entry = createMemo(() =>
      pendingAskFeedbackEntry(appStore.state.messages, PENDING.phase_id),
    );
    const meta = createMemo(() => askUserDockMeta(PENDING, entry()?.meta));
    const retained = createMemo<WorkflowFeedbackMeta | undefined>((previous) =>
      retainAskDockMeta(previous, meta(), PENDING.phase_id),
    );
    return (
      <Show when={retained()} keyed>
        {(dockMeta) => (
          <AskUserDock
            meta={dockMeta}
            client={c}
            sessionId="sess-1"
            runId="run-1"
            runRevision={7}
            entryKey={entry()?.entryKey}
            appStore={appStore}
            registerSubmit={(fn) => {
              submit = fn;
            }}
          />
        )}
      </Show>
    );
  });

  return {
    ...view,
    c,
    appStore,
    answerOf: () =>
      appStore.state.messages.find((m) => m.id === "fb-msg")?.workflow_feedback
        ?.answer,
    submit: (text: string) => submit!(text),
  };
}

describe("AskUserDock inside the ChatView ask slot", () => {
  it("keeps the pick and shows the error when the answer is rejected", async () => {
    const slot = mountChatViewAskSlot(async () => {
      throw new Error("Workflow rejected that answer.");
    });

    fireEvent.click(slot.getByLabelText("blue"));
    expect((slot.getByLabelText("blue") as HTMLInputElement).checked).toBe(true);

    expect(await slot.submit("")).toBe(false);

    expect(slot.c.resolveWorkflowDecision).toHaveBeenCalledWith(
      "run-1",
      "ask-1",
      { expected_revision: 7, choices: ["blue"] },
    );
    expect(slot.getByTestId("workflow-feedback-error").textContent).toContain(
      "Workflow rejected that answer.",
    );
    expect((slot.getByLabelText("blue") as HTMLInputElement).checked).toBe(true);
  });

  it("rolls the optimistic answer back off the transcript row", async () => {
    const slot = mountChatViewAskSlot(async () => {
      throw new Error("Workflow rejected that answer.");
    });

    fireEvent.click(slot.getByLabelText("blue"));
    await slot.submit("");

    expect(slot.answerOf()).toBeUndefined();
  });

  it("still submits and clears the slot when the answer lands", async () => {
    const slot = mountChatViewAskSlot(async () => ({}));

    fireEvent.click(slot.getByLabelText("blue"));
    expect(await slot.submit("")).toBe(true);
    expect(slot.answerOf()).toBe("blue");
  });
});
