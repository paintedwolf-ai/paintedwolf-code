import { stubClient } from "../../test/client-fixture.ts";
import { afterEach, describe, expect, it, vi } from "vitest";
import { stockBindingId } from "../../contributions/stock-frame-test.ts";
import { render, fireEvent } from "@solidjs/testing-library";
import { AskUserDock } from "./AskUserDock.tsx";
import type { AskUserDockSubmitFn } from "./AskUserDock.tsx";
import type { LycaonClient } from "../../api/client.ts";
import type { Message, WorkflowFeedbackMeta } from "../../api/types.ts";
import { createAppStore } from "../../store/app-state.ts";
import { setShortcutPlatformForTests } from "../../shortcuts/platform.ts";
import { LycaonApiError } from "../../api/http.ts";
import {
  resetShortcutPrefsForTests,
  saveShortcutOverride,
} from "../../settings/system/shortcut-prefs.ts";
import {
  resetComposerDraftsForTests,
  setComposerDraft,
} from "../../chat/composer/composer-drafts.ts";

afterEach(() => {
  resetShortcutPrefsForTests();
  setShortcutPlatformForTests(null);
  resetComposerDraftsForTests();
});

function client(overrides: Partial<LycaonClient> = {}): LycaonClient {
  return stubClient({
    resolveWorkflowFeedback: vi.fn().mockResolvedValue({}),
    resolveWorkflowDecision: vi.fn().mockResolvedValue({}),
    resolveWorkflowSecret: vi.fn().mockResolvedValue({}),
    getSessionArtifact: vi.fn().mockResolvedValue(new Blob(["x"], { type: "image/png" })),
    ...overrides,
  });
}

const base = {
  sessionId: "sess-1",
  runId: "run-1",
  runRevision: 7,
};

function lastReady(reported: boolean[]): boolean | undefined {
  return reported[reported.length - 1];
}

function feedbackStore(meta: WorkflowFeedbackMeta, messageId = "fb-msg") {
  const appStore = createAppStore();
  const row: Message = {
    id: messageId,
    role: "system", origin: "host" as const, authority: "system" as const, trust_tier: "trusted" as const,
    kind: "workflow_feedback",
    content: meta.prompt,
    workflow_run_id: base.runId,
    workflow_feedback: { ...meta },
    created_at: new Date().toISOString(),
  };
  appStore.actions.upsertMessage(row);
  return { appStore, messageId };
}

describe("AskUserDock", () => {
  it("submits secret responses only through the protected endpoint", async () => {
    const c = client();
    const meta: WorkflowFeedbackMeta = {
      phase_id: "secret-1",
      prompt: "Provide the registry token",
      response_type: "secret",
      purpose: "secret",
      secret: { name: "Registry token", purpose: "Authenticate publishing", scope: "chat" },
    };
    const { getByLabelText, getByRole, getByTestId } = render(() => (
      <AskUserDock meta={meta} client={c} {...base} />
    ));
    const input = getByLabelText("Registry token") as HTMLInputElement;
    expect(input.type).toBe("password");
    fireEvent.input(input, { target: { value: "x" } });
    expect(getByTestId("workflow-secret-policy").textContent).toBe(
      "Available only to this chat · No agent-use deadline",
    );

    const visibility = getByRole("button", { name: "Show secret" });
    expect(visibility.getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(visibility);
    expect(input.type).toBe("text");
    expect(getByRole("button", { name: "Hide secret" }).textContent).toBe("Hide");
    fireEvent.click(getByRole("button", { name: "Hide secret" }));
    expect(input.type).toBe("password");

    fireEvent.click(getByRole("button", { name: "Store secret" }));

    await vi.waitFor(() => expect(c.resolveWorkflowSecret).toHaveBeenCalledWith(
      "run-1",
      "secret-1",
      { expected_revision: 7, secret_value: "x" },
    ));
    expect(c.resolveWorkflowFeedback).not.toHaveBeenCalled();
    expect(c.resolveWorkflowDecision).not.toHaveBeenCalled();
  });

  it("shows project persistence and the agent-use deadline before a secret is entered", () => {
    const meta: WorkflowFeedbackMeta = {
      phase_id: "secret-project",
      prompt: "Provide the deployment key",
      response_type: "secret",
      purpose: "secret",
      secret: {
        name: "Deployment key",
        purpose: "Publish releases",
        scope: "project",
        agent_use_ttl_ms: 86_400_000,
      },
    };
    const { getByTestId, getByText } = render(() => (
      <AskUserDock meta={meta} client={client()} {...base} />
    ));
    expect(getByText("Publish releases")).toBeTruthy();
    expect(getByTestId("workflow-secret-policy").textContent).toBe(
      "Available to later chats in this project · Agent use ends in 1 day",
    );
  });

  it("text mode has no options node and no remaining countdown", () => {
    const meta: WorkflowFeedbackMeta = {
		phase_id: "ask-1",
		prompt: "What scope?",
		response_type: "text",
	};
    const { getByTestId, queryByTestId } = render(() => (
      <AskUserDock meta={meta} client={client()} {...base} />
    ));
    expect(getByTestId("ask-user-dock").getAttribute("data-response-type")).toBe("text");
    expect(getByTestId("workflow-feedback-prompt").textContent).toContain("What scope?");
    expect(queryByTestId("workflow-feedback-option")).toBeNull();
    expect(queryByTestId("workflow-feedback-submit")).toBeNull();
    expect(queryByTestId("workflow-feedback-remaining")).toBeNull();
  });

  it("single option click selects only and does not resolve", async () => {
    const c = client();
    let submit: AskUserDockSubmitFn | undefined;
    const meta: WorkflowFeedbackMeta = {
      phase_id: "pick",
      prompt: "Pick one",
      response_type: "single_choice",
      options: ["red", "blue"],
    };
    const { getByLabelText, queryByTestId } = render(() => (
      <AskUserDock
        meta={meta}
        client={c}
        {...base}
        registerSubmit={(fn) => {
          submit = fn;
        }}
      />
    ));
    expect(queryByTestId("workflow-feedback-submit")).toBeNull();
    fireEvent.click(getByLabelText("blue"));
    expect(c.resolveWorkflowDecision).not.toHaveBeenCalled();
    await submit!("");
    expect(c.resolveWorkflowDecision).toHaveBeenCalledWith("run-1", "pick", {
      expected_revision: 7,
      choices: ["blue"],
    });
  });

  it("hint switches to the Send step once an option is picked", () => {
    const meta: WorkflowFeedbackMeta = {
      phase_id: "pick",
      prompt: "Pick one",
      response_type: "single_choice",
      options: ["red", "blue"],
    };
    const { container, getByLabelText } = render(() => (
      <AskUserDock meta={meta} client={client()} {...base} />
    ));
    const hint = () => container.querySelector(".den-ask-user-dock-hint")?.textContent;
    expect(hint()).toBe("Select one, then Enter");
    fireEvent.click(getByLabelText("blue"));
    expect(hint()).toBe("Press Enter to submit your answer");
  });

  it("hint follows the live platform binding for Send", async () => {
    setShortcutPlatformForTests("macos");
    await saveShortcutOverride(stockBindingId("composer-send"), "Mod+Enter");
    const meta: WorkflowFeedbackMeta = {
      phase_id: "pick",
      prompt: "Pick one",
      response_type: "single_choice",
      options: ["red", "blue"],
    };
    const { container } = render(() => (
      <AskUserDock meta={meta} client={client()} {...base} />
    ));
    expect(
      container.querySelector(".den-ask-user-dock-hint")?.textContent,
    ).toBe("Select one, then ⌘Enter");
  });

  it("single Other send resolves via registerSubmit", async () => {
    const c = client();
    let submit: AskUserDockSubmitFn | undefined;
    const meta: WorkflowFeedbackMeta = {
      phase_id: "pick",
      prompt: "Pick one",
      response_type: "single_choice",
      options: ["red", "blue"],
    };
    render(() => (
      <AskUserDock
        meta={meta}
        client={c}
        {...base}
        registerSubmit={(fn) => {
          submit = fn;
        }}
      />
    ));
    expect(submit).toBeTypeOf("function");
    await submit!("green");
    expect(c.resolveWorkflowDecision).toHaveBeenCalledWith("run-1", "pick", {
      expected_revision: 7,
      choices: ["green"],
    });
  });

  it("does not rebase an answer when the ask revision is stale", async () => {
    const revisionConflict = new LycaonApiError(
      "workflow run revision conflict",
      409,
      "workflow_revision_conflict",
    );
    const resolveWorkflowDecision = vi
      .fn()
      .mockRejectedValueOnce(revisionConflict)
      .mockResolvedValueOnce({});
    const c = client({
      resolveWorkflowDecision,
      getActiveWorkflowRun: vi.fn().mockResolvedValue({
        id: "run-1",
        session_id: "sess-1",
        status: "running",
        revision: 8,
        ui: {
          pending_feedback: {
            phase_id: "pick",
            prompt: "Pick one",
          },
        },
      }),
    });
    let submit: AskUserDockSubmitFn | undefined;
    const { getByLabelText } = render(() => (
      <AskUserDock
        meta={{
          phase_id: "pick",
          prompt: "Pick one",
          response_type: "single_choice",
          options: ["red", "blue"],
        }}
        client={c}
        {...base}
        registerSubmit={(fn) => {
          submit = fn;
        }}
      />
    ));

    fireEvent.click(getByLabelText("blue"));
    await expect(submit!("")).resolves.toBe(false);
    expect(resolveWorkflowDecision).toHaveBeenCalledWith(
      "run-1",
      "pick",
      { expected_revision: 7, choices: ["blue"] },
    );
    expect(resolveWorkflowDecision).toHaveBeenCalledTimes(1);
  });

  it("leaves a stale dock open with an actionable error", async () => {
    const resolveWorkflowDecision = vi.fn().mockRejectedValue(
      new LycaonApiError(
        "workflow run revision conflict",
        409,
        "workflow_revision_conflict",
      ),
    );
    const onResolved = vi.fn();
    const c = client({
      resolveWorkflowDecision,
      getActiveWorkflowRun: vi.fn().mockResolvedValue({
        id: "run-1",
        session_id: "sess-1",
        status: "running",
        revision: 8,
      }),
    });
    let submit: AskUserDockSubmitFn | undefined;
    const { getByLabelText } = render(() => (
      <AskUserDock
        meta={{
          phase_id: "pick",
          prompt: "Pick one",
          response_type: "single_choice",
          options: ["red", "blue"],
        }}
        client={c}
        {...base}
        onResolved={onResolved}
        registerSubmit={(fn) => {
          submit = fn;
        }}
      />
    ));

    fireEvent.click(getByLabelText("blue"));
    await expect(submit!("")).resolves.toBe(false);
    expect(resolveWorkflowDecision).toHaveBeenCalledTimes(1);
    expect(onResolved).not.toHaveBeenCalled();
  });

  it("multi toggles plus Send commits choices", async () => {
    const c = client();
    let submit: AskUserDockSubmitFn | undefined;
    const meta: WorkflowFeedbackMeta = {
      phase_id: "pick",
      prompt: "Pick some",
      response_type: "multi_choice",
      options: ["Host API", "Den UI", "Docs"],
    };
    const { getByLabelText, queryByTestId } = render(() => (
      <AskUserDock
        meta={meta}
        client={c}
        {...base}
        registerSubmit={(fn) => {
          submit = fn;
        }}
      />
    ));
    expect(queryByTestId("workflow-feedback-submit")).toBeNull();
    fireEvent.click(getByLabelText("Host API"));
    fireEvent.click(getByLabelText("Den UI"));
    expect(c.resolveWorkflowDecision).not.toHaveBeenCalled();
    await submit!("");
    expect(c.resolveWorkflowDecision).toHaveBeenCalledWith("run-1", "pick", {
      expected_revision: 7,
      choices: ["Host API", "Den UI"],
    });
  });

  it("multi Send with Other appends typed choice", async () => {
    const c = client();
    let submit: AskUserDockSubmitFn | undefined;
    const meta: WorkflowFeedbackMeta = {
      phase_id: "pick",
      prompt: "Pick some",
      response_type: "multi_choice",
      options: ["red", "blue"],
    };
    const { getByLabelText } = render(() => (
      <AskUserDock
        meta={meta}
        client={c}
        {...base}
        registerSubmit={(fn) => {
          submit = fn;
        }}
      />
    ));
    fireEvent.click(getByLabelText("red"));
    await submit!("green");
    expect(c.resolveWorkflowDecision).toHaveBeenCalledWith("run-1", "pick", {
      expected_revision: 7,
      choices: ["red", "green"],
    });
  });

  it("text Send resolves via resolveWorkflowFeedback", async () => {
    const c = client();
    let submit: AskUserDockSubmitFn | undefined;
    const meta: WorkflowFeedbackMeta = {
      phase_id: "ask-1",
      prompt: "Scope?",
      response_type: "text",
    };
    const { appStore, messageId } = feedbackStore(meta);
    render(() => (
      <AskUserDock
        meta={meta}
        client={c}
        {...base}
        appStore={appStore}
        entryKey={messageId}
        registerSubmit={(fn) => {
          submit = fn;
        }}
      />
    ));
    await submit!("ship MVP");
    expect(c.resolveWorkflowFeedback).toHaveBeenCalledWith("run-1", "ask-1", {
      expected_revision: 7,
      response: "ship MVP",
    });
    expect(appStore.state.messages[0]?.workflow_feedback?.answer).toBe("ship MVP");
  });

  it("compare image click selects only and does not resolve", async () => {
    const c = client();
    let submit: AskUserDockSubmitFn | undefined;
    const meta: WorkflowFeedbackMeta = {
      phase_id: "compare",
      prompt: "Pick a variant",
      response_type: "single_choice",
      purpose: "compare",
      artifact_ids: ["art-1", "art-2"],
    };
    const { findByTestId, queryByTestId } = render(() => (
      <AskUserDock
        meta={meta}
        client={c}
        {...base}
        registerSubmit={(fn) => {
          submit = fn;
        }}
      />
    ));
    expect(queryByTestId("workflow-feedback-submit")).toBeNull();
    const grid = await findByTestId("workflow-feedback-compare-grid");
    expect(grid.querySelectorAll("[data-testid=workflow-feedback-artifact]").length).toBe(2);
    expect(
      grid.querySelectorAll("[data-testid=workflow-feedback-compare-label]").length,
    ).toBe(2);
    const radios = grid.querySelectorAll(
      "input[type=radio][name=ask-user-dock-artifact-compare]",
    );
    expect(radios.length).toBe(2);
    fireEvent.change(radios[1]!);
    expect(c.resolveWorkflowDecision).not.toHaveBeenCalled();
    await submit!("");
    expect(c.resolveWorkflowDecision).toHaveBeenCalledWith("run-1", "compare", {
      expected_revision: 7,
      choices: ["B"],
    });
  });

  it("multi-artifact review with response_type text renders non-selectable gallery and submits composer text", async () => {
    const c = client();
    let submit: AskUserDockSubmitFn | undefined;
    const meta: WorkflowFeedbackMeta = {
      phase_id: "review-text",
      prompt: "Look these over and provide feedback",
      response_type: "text",
      purpose: "review",
      artifact_ids: ["art-1", "art-2", "art-3"],
    };
    const { findByTestId } = render(() => (
      <AskUserDock
        meta={meta}
        client={c}
        {...base}
        registerSubmit={(fn) => {
          submit = fn;
        }}
      />
    ));
    const grid = await findByTestId("workflow-feedback-compare-grid");
    expect(grid.querySelectorAll("[data-testid=workflow-feedback-artifact]").length).toBe(3);
    expect(grid.querySelectorAll("input[type=radio]").length).toBe(0);
    expect(grid.classList.contains("den-workflow-feedback-card-compare-grid--trio")).toBe(true);

    await submit!("Looks good but make the gates smaller");
    expect(c.resolveWorkflowFeedback).toHaveBeenCalledWith("run-1", "review-text", {
      expected_revision: 7,
      response: "Looks good but make the gates smaller",
      secrets: undefined,
    });
    expect(c.resolveWorkflowDecision).not.toHaveBeenCalled();
  });

  it("multi-artifact review with single_choice renders non-selectable gallery with review choices rail", async () => {
    const c = client();
    let submit: AskUserDockSubmitFn | undefined;
    const meta: WorkflowFeedbackMeta = {
      phase_id: "review-choice",
      prompt: "Review these 3 designs",
      response_type: "single_choice",
      purpose: "review",
      options: ["Approve", "Request changes", "Reject"],
      artifact_ids: ["art-1", "art-2", "art-3"],
    };
    const { findByTestId, getByLabelText } = render(() => (
      <AskUserDock
        meta={meta}
        client={c}
        {...base}
        registerSubmit={(fn) => {
          submit = fn;
        }}
      />
    ));
    const grid = await findByTestId("workflow-feedback-compare-grid");
    expect(grid.querySelectorAll("[data-testid=workflow-feedback-artifact]").length).toBe(3);
    expect(grid.querySelectorAll("input[type=radio]").length).toBe(0);

    const approveRadio = getByLabelText("Approve");
    fireEvent.click(approveRadio);
    await submit!("");
    expect(c.resolveWorkflowDecision).toHaveBeenCalledWith("run-1", "review-choice", {
      expected_revision: 7,
      choices: ["Approve"],
      secrets: undefined,
    });
  });

  it("reports answer-ready on select, clears on unselect and unmount", () => {
    const ready: boolean[] = [];
    const meta: WorkflowFeedbackMeta = {
      phase_id: "pick",
      prompt: "Pick some",
      response_type: "multi_choice",
      options: ["red", "blue"],
    };
    const { getByLabelText, unmount } = render(() => (
      <AskUserDock
        meta={meta}
        client={client()}
        {...base}
        onAnswerReadyChange={(v) => ready.push(v)}
      />
    ));
    expect(lastReady(ready)).toBe(false);
    fireEvent.click(getByLabelText("red"));
    expect(lastReady(ready)).toBe(true);
    fireEvent.click(getByLabelText("red"));
    expect(lastReady(ready)).toBe(false);
    fireEvent.click(getByLabelText("blue"));
    expect(lastReady(ready)).toBe(true);
    unmount();
    expect(lastReady(ready)).toBe(false);
  });

  it("text mode never reports answer-ready — composer text drives Send", () => {
    const ready: boolean[] = [];
    render(() => (
      <AskUserDock
        meta={{ phase_id: "ask-1", prompt: "Scope?", response_type: "text" }}
        client={client()}
        {...base}
        onAnswerReadyChange={(v) => ready.push(v)}
      />
    ));
    expect(ready.every((v) => v === false)).toBe(true);
  });

  it("compare selection reports answer-ready", async () => {
    const ready: boolean[] = [];
    const meta: WorkflowFeedbackMeta = {
      phase_id: "compare",
      prompt: "Pick a variant",
      response_type: "single_choice",
      purpose: "compare",
      artifact_ids: ["art-1", "art-2"],
    };
    const { findByTestId } = render(() => (
      <AskUserDock
        meta={meta}
        client={client()}
        {...base}
        onAnswerReadyChange={(v) => ready.push(v)}
      />
    ));
    const grid = await findByTestId("workflow-feedback-compare-grid");
    expect(lastReady(ready)).toBe(false);
    fireEvent.change(
      grid.querySelectorAll("input[type=radio][name=ask-user-dock-artifact-compare]")[0]!,
    );
    expect(lastReady(ready)).toBe(true);
  });

  it("empty composer submit is a no-op for text mode", async () => {
    const c = client();
    let submit: AskUserDockSubmitFn | undefined;
    render(() => (
      <AskUserDock
        meta={{ phase_id: "ask-1", prompt: "?", response_type: "text" }}
        client={c}
        {...base}
        registerSubmit={(fn) => {
          submit = fn;
        }}
      />
    ));
    expect(await submit!("")).toBe(false);
    expect(c.resolveWorkflowFeedback).not.toHaveBeenCalled();
  });

  it("minimize retracts the dock to its head strip and expand restores it", () => {
    const meta: WorkflowFeedbackMeta = {
      phase_id: "pick",
      prompt: "Pick one\nof these",
      response_type: "single_choice",
      options: ["red", "blue"],
    };
    const { getByTestId, queryByTestId } = render(() => (
      <AskUserDock meta={meta} client={client()} {...base} />
    ));
    const dock = getByTestId("ask-user-dock");
    expect(dock.hasAttribute("data-minimized")).toBe(false);

    fireEvent.click(getByTestId("ask-user-dock-minimize"));
    expect(dock.hasAttribute("data-minimized")).toBe(true);
    expect(queryByTestId("ask-user-dock-minimize")).toBeNull();
    // The body stays mounted so a selection survives the round trip.
    expect(queryByTestId("workflow-feedback-prompt")).not.toBeNull();
    expect(getByTestId("ask-user-dock-expand").textContent).toContain(
      "Pick one of these",
    );

    fireEvent.click(getByTestId("ask-user-dock-expand"));
    expect(dock.hasAttribute("data-minimized")).toBe(false);
    expect(queryByTestId("ask-user-dock-expand")).toBeNull();
  });

  it("minimized state is controlled when the caller controls it", () => {
    const seen: boolean[] = [];
    const { getByTestId } = render(() => (
      <AskUserDock
        meta={{ phase_id: "ask-1", prompt: "?", response_type: "text" }}
        client={client()}
        {...base}
        minimized={false}
        onMinimizedChange={(next) => seen.push(next)}
      />
    ));
    fireEvent.click(getByTestId("ask-user-dock-minimize"));
    expect(seen).toEqual([true]);
    // Caller kept it false, so the dock stays expanded.
    expect(getByTestId("ask-user-dock").hasAttribute("data-minimized")).toBe(false);
  });

  it("a rejected composer Send re-opens a minimized dock", async () => {
    let submit: AskUserDockSubmitFn | undefined;
    const { getByTestId } = render(() => (
      <AskUserDock
        meta={{
          phase_id: "pick",
          prompt: "Pick one",
          response_type: "single_choice",
          options: ["red", "blue"],
        }}
        client={client()}
        {...base}
        registerSubmit={(fn) => {
          submit = fn;
        }}
      />
    ));
    fireEvent.click(getByTestId("ask-user-dock-minimize"));
    expect(getByTestId("ask-user-dock").hasAttribute("data-minimized")).toBe(true);
    expect(await submit!("")).toBe(false);
    expect(getByTestId("ask-user-dock").hasAttribute("data-minimized")).toBe(false);
  });

  const choiceMeta: WorkflowFeedbackMeta = {
    phase_id: "pick",
    prompt: "Pick one",
    response_type: "single_choice",
    options: ["red", "blue"],
  };

  it("the rail points at the composer until a draft exists, then says what Send does", () => {
    const { getByTestId } = render(() => (
      <AskUserDock meta={choiceMeta} client={client()} {...base} />
    ));
    const rail = getByTestId("ask-user-redirect-rail");
    expect(rail.textContent).toContain("Not listed?");
    expect(rail.classList.contains("den-ask-user-dock-rail--armed")).toBe(false);

    setComposerDraft("sess-1", "all of the above");
    expect(rail.classList.contains("den-ask-user-dock-rail--armed")).toBe(true);
    expect(rail.textContent).toContain("instead of an option");
  });

  it("a draft arms the dock and supersedes the options it overrides", () => {
    const { getByTestId, container, queryByTestId } = render(() => (
      <AskUserDock meta={choiceMeta} client={client()} {...base} />
    ));
    const dock = getByTestId("ask-user-dock");
    const options = container.querySelector(".den-ask-user-dock-options")!;
    expect(dock.hasAttribute("data-composer-armed")).toBe(false);
    expect(options.classList.contains("den-ask-user-dock-superseded")).toBe(false);
    expect(queryByTestId("ask-user-dock")!.querySelector(".den-ask-user-dock-hint"))
      .not.toBeNull();

    setComposerDraft("sess-1", "all of the above");
    expect(dock.hasAttribute("data-composer-armed")).toBe(true);
    expect(options.classList.contains("den-ask-user-dock-superseded")).toBe(true);
    // The rail carries the instruction once armed.
    expect(container.querySelector(".den-ask-user-dock-hint")).toBeNull();
  });

  it("multi says the typed answer joins the picked options rather than replacing them", () => {
    const { getByTestId, container } = render(() => (
      <AskUserDock
        meta={{ ...choiceMeta, response_type: "multi_choice" }}
        client={client()}
        {...base}
      />
    ));
    setComposerDraft("sess-1", "and green");
    expect(getByTestId("ask-user-redirect-rail").textContent).toContain(
      "adds what you typed",
    );
    // Multi appends, so its options are not superseded.
    expect(
      container
        .querySelector(".den-ask-user-dock-options")!
        .classList.contains("den-ask-user-dock-superseded"),
    ).toBe(false);
  });

  it("text mode has no rail — the composer is the only answer path", () => {
    const { queryByTestId } = render(() => (
      <AskUserDock
        meta={{ phase_id: "ask-1", prompt: "Scope?", response_type: "text" }}
        client={client()}
        {...base}
      />
    ));
    expect(queryByTestId("ask-user-redirect-rail")).toBeNull();
  });

  it("compare Other text resolves the ask — the documented way to say neither", async () => {
    const c = client();
    let submit: AskUserDockSubmitFn | undefined;
    render(() => (
      <AskUserDock
        meta={{
          phase_id: "compare",
          prompt: "Pick a variant",
          response_type: "single_choice",
          purpose: "compare",
          artifact_ids: ["art-1", "art-2"],
        }}
        client={c}
        {...base}
        registerSubmit={(fn) => {
          submit = fn;
        }}
      />
    ));
    expect(await submit!("neither — go warmer")).toBe(true);
    expect(c.resolveWorkflowDecision).toHaveBeenCalledWith("run-1", "compare", {
      expected_revision: 7,
      choices: ["neither — go warmer"],
    });
  });
});
