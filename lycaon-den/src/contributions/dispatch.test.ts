import { stubClient } from "../test/client-fixture.ts";
import { afterEach, describe, expect, it, vi } from "vitest";
import { COMMAND_DECLINED, registerCommandHandler, resetDispatcherForTests } from "../shortcuts/dispatcher.ts";
import type { LycaonClient } from "../api/client.ts";
import {
  resetContributionStoreForTest,
  seedContributionFrameForTest,
} from "./contribution-store.ts";
import type {
  CommandInvokeResponse,
  ContributionCommand,
  ContributionFrameResponse,
} from "../api/types.ts";
import {
  dispatchContributionCommand,
  nativeCommandId,
  registerUIEffectSink,
  registerCommandContextProvider,
} from "./dispatch.ts";

function frameWith(commands: ContributionCommand[]): ContributionFrameResponse {
  return {
    frame_revision: "frame-1",
    commands,
    menus: [],
    keybindings: [],
    binding_defaults: [],
    editor_actions: [],
    configuration: [],
    requirements: [],
    search_sources: [],
    operations: [],
    themes: [],
    notes: [],
  };
}

const navigateCommand: ContributionCommand = {
  id: "acme/reviewer:go-home",
  provider: "acme/reviewer",
  title: "Go home",
  executor: "host",
  invocation: "project",
  action_kind: "navigate",
  icon: "launcher",
  result_treatment: "effect",
};

const sessionCommand: ContributionCommand = {
  id: "acme/reviewer:start-review",
  provider: "acme/reviewer",
  title: "Start review",
  executor: "host",
  invocation: "session",
  action_kind: "workflow_start",
  icon: "route",
  result_treatment: "receipt",
};

const denCommand: ContributionCommand = {
  id: "painted-wolf/den:focus-composer",
  provider: "painted-wolf/den",
  title: "Focus composer",
  executor: "den",
  invocation: "den",
  action_kind: "native_ui",
  handler_id: "focus_composer",
  icon: "play",
  result_treatment: "effect",
};

function invokeClient(response: CommandInvokeResponse): LycaonClient {
  return stubClient({
    invokeProjectCommand: vi.fn(() => Promise.resolve(response)),
    invokeSessionCommand: vi.fn(() => Promise.resolve(response)),
  });
}

afterEach(() => {
  resetContributionStoreForTest();
});

describe("nativeCommandId", () => {
  it("returns only declared command identities", () => {
    expect(nativeCommandId("focus_composer")).toBeNull();
    seedContributionFrameForTest(frameWith([denCommand]));
    expect(nativeCommandId("focus_composer")).toBe(denCommand.id);
    expect(nativeCommandId("missing_handler")).toBeNull();
  });
});

describe("contribution dispatch", () => {
  it("carries project-scoped editor coordinates and respects a captured target", async () => {
    seedContributionFrameForTest(frameWith([navigateCommand]));
    const client = invokeClient({ status: "completed", frame_revision: "frame-1" });
    const current = { root_id: "root", path: "current.ts", start_line: 2, end_line: 4, document_revision: 7 };
    const release = registerCommandContextProvider((projectId) => projectId === "p1" ? current : undefined);
    try {
      await dispatchContributionCommand(navigateCommand.id, { client, projectId: "p1", sessionId: null });
      expect(client.invokeProjectCommand).toHaveBeenLastCalledWith("p1", navigateCommand.id, expect.objectContaining({ context: current }));
      await dispatchContributionCommand(navigateCommand.id, { client, projectId: "p2", sessionId: null });
      expect(client.invokeProjectCommand).toHaveBeenLastCalledWith("p2", navigateCommand.id, expect.not.objectContaining({ context: expect.anything() }));
      const captured = { ...current, path: "captured.ts" };
      await dispatchContributionCommand(navigateCommand.id, { client, projectId: "p1", sessionId: null }, captured);
      expect(client.invokeProjectCommand).toHaveBeenLastCalledWith("p1", navigateCommand.id, expect.objectContaining({ context: captured }));
    } finally { release(); }
  });
  it("fails closed with no hydrated frame", async () => {
    resetContributionStoreForTest();
    const result = await dispatchContributionCommand(navigateCommand.id, {
      client: invokeClient({ status: "completed", frame_revision: "frame-1" }),
      projectId: "p1",
      sessionId: null,
    });
    expect(result).toEqual({ ok: false, reason: "no_frame" });
  });

  it("never falls back to name lookup for a command outside the frame", async () => {
    seedContributionFrameForTest(frameWith([navigateCommand]));
    const client = invokeClient({ status: "completed", frame_revision: "frame-1" });
    const result = await dispatchContributionCommand("acme/reviewer:missing", {
      client,
      projectId: "p1",
      sessionId: null,
    });
    expect(result).toEqual({ ok: false, reason: "not_in_frame" });
    expect(client.invokeProjectCommand).not.toHaveBeenCalled();
  });

  it("routes a project-bound command with the captured frame revision and applies the effect", async () => {
    seedContributionFrameForTest(frameWith([navigateCommand]));
    const navigate = vi.fn();
    const unregister = registerUIEffectSink({ navigate, composerPrefill: vi.fn() });
    const client = invokeClient({
      status: "completed",
      frame_revision: "frame-1",
      ui_effect: { kind: "navigate", destination: "files" },
    });
    const result = await dispatchContributionCommand(navigateCommand.id, {
      client,
      projectId: "p1",
      sessionId: "s1",
    });
    expect(result.ok).toBe(true);
    expect(client.invokeProjectCommand).toHaveBeenCalledWith(
      "p1",
      navigateCommand.id,
      expect.objectContaining({ frame_revision: "frame-1" }),
    );
    expect(client.invokeSessionCommand).not.toHaveBeenCalled();
    expect(navigate).toHaveBeenCalledWith("files");
    unregister();
  });

  it("routes a session-bound command on the session route and requires a session", async () => {
    seedContributionFrameForTest(frameWith([sessionCommand]));
    const client = invokeClient({ status: "accepted", frame_revision: "frame-1", message_id: "11111111-1111-4111-8111-111111111111" });
    const noSession = await dispatchContributionCommand(sessionCommand.id, {
      client,
      projectId: "p1",
      sessionId: null,
    });
    expect(noSession).toEqual({ ok: false, reason: "no_session" });

    const result = await dispatchContributionCommand(sessionCommand.id, {
      client,
      projectId: "p1",
      sessionId: "s1",
    });
    expect(result.ok).toBe(true);
    expect(result).toMatchObject({ messageId: "11111111-1111-4111-8111-111111111111" });
    expect(client.invokeSessionCommand).toHaveBeenCalledWith(
      "s1",
      sessionCommand.id,
      expect.objectContaining({ frame_revision: "frame-1" }),
    );
  });

  it("reports a host-declared failed invocation without applying effects", async () => {
    seedContributionFrameForTest(frameWith([navigateCommand]));
    const navigate = vi.fn();
    const unregister = registerUIEffectSink({ navigate, composerPrefill: vi.fn() });
    const result = await dispatchContributionCommand(navigateCommand.id, {
      client: invokeClient({
        status: "failed",
        frame_revision: "frame-1",
        error: { code: "operation_output_invalid", message: "Provider returned invalid output" },
        ui_effect: { kind: "navigate", destination: "files" },
      }),
      projectId: "p1",
      sessionId: null,
    });

    expect(result).toEqual({
      ok: false,
      reason: "invoke_failed",
      message: "Provider returned invalid output",
    });
    expect(navigate).not.toHaveBeenCalled();
    unregister();
  });

  it("fails closed when a den handler is absent from the implementation map", async () => {
    seedContributionFrameForTest(frameWith([denCommand]));
    const result = await dispatchContributionCommand(denCommand.id, {
      client: null,
      projectId: "p1",
      sessionId: null,
    });
    expect(result).toEqual({ ok: false, reason: "handler_missing" });
  });
});

describe("den handler outcomes", () => {
  const declinableCommand: ContributionCommand = {
    ...denCommand,
    handler_id: "composer.send",
  };

  it("reports a declining handler instead of claiming success", async () => {
    resetDispatcherForTests();
    seedContributionFrameForTest(frameWith([declinableCommand]));
    const detach = registerCommandHandler("composer.send", () => COMMAND_DECLINED);
    const result = await dispatchContributionCommand(declinableCommand.id, {
      client: null,
      projectId: "p1",
      sessionId: null,
    });
    expect(result).toEqual({ ok: false, reason: "declined" });
    detach();
  });

  it("maps a throwing handler to invoke_failed instead of rejecting", async () => {
    resetDispatcherForTests();
    seedContributionFrameForTest(frameWith([declinableCommand]));
    const detach = registerCommandHandler("composer.send", () => {
      throw new Error("handler exploded");
    });
    const result = await dispatchContributionCommand(declinableCommand.id, {
      client: null,
      projectId: "p1",
      sessionId: null,
    });
    expect(result).toEqual({
      ok: false,
      reason: "invoke_failed",
      message: "handler exploded",
    });
    detach();
  });
});
