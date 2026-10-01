import { stubClient } from "../../test/client-fixture.ts";
import { createRoot, createSignal } from "solid-js";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { VerifySettingsResponse } from "../../api/types.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createVerifyTestSuggestion } from "./verify-test-suggestion.ts";

const getVerifySettings = vi.fn();
const updateVerifySettings = vi.fn();
const dismissVerifySettings = vi.fn();

const client = stubClient({
  getVerifySettings,
  updateVerifySettings,
  dismissVerifySettings,
});

function docFor(projectId: string): VerifySettingsResponse {
  return {
    scope: "project",
    test: "",
    verify_path: ".paintedwolf/verify.yaml",
    detected_command: `./task check-${projectId}`,
    detected_source: "README.md",
    backend_configured: true,
    suggestion_state: "suggest",
  };
}

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

describe("createVerifyTestSuggestion", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("asks nothing until the backend is connected, then loads", async () => {
    getVerifySettings.mockResolvedValue(docFor("a"));
    await createRoot(async (dispose) => {
      const [connected, setConnected] = createSignal(false);
      const suggestion = createVerifyTestSuggestion({
        client: () => (connected() ? client : null),
        appStore: createAppStore(),
        projectId: () => "a",
      });
      await flush();
      expect(getVerifySettings).not.toHaveBeenCalled();
      expect(suggestion.suggesting()).toBe(false);

      setConnected(true);
      await flush();
      expect(suggestion.suggesting()).toBe(true);
      expect(suggestion.detectedCommand()).toBe("./task check-a");
      dispose();
    });
  });

  // Late responses must remain bound to their project.
  it("drops a load that lands after the project changed", async () => {
    const pending = new Map<string, (doc: VerifySettingsResponse) => void>();
    getVerifySettings.mockImplementation(
      (projectId: string) =>
        new Promise((resolve) => pending.set(projectId, resolve)),
    );

    await createRoot(async (dispose) => {
      const [projectId, setProjectId] = createSignal("a");
      const suggestion = createVerifyTestSuggestion({
        client: () => client,
        appStore: createAppStore(),
        projectId,
      });
      await flush();
      setProjectId("b");
      await flush();

      // Project "b" answers first; "a" answers late and must not be shown.
      pending.get("b")?.(docFor("b"));
      await flush();
      pending.get("a")?.(docFor("a"));
      await flush();

      expect(suggestion.detectedCommand()).toBe("./task check-b");
      dispose();
    });
  });

  it("sets the detected command and stops suggesting", async () => {
    getVerifySettings.mockResolvedValue(docFor("a"));
    updateVerifySettings.mockResolvedValue({
      ...docFor("a"),
      test: "./task check-a",
      suggestion_state: "accepted",
    } satisfies VerifySettingsResponse);

    await createRoot(async (dispose) => {
      const suggestion = createVerifyTestSuggestion({
        client: () => client,
        appStore: createAppStore(),
        projectId: () => "a",
      });
      await flush();
      await suggestion.accept();
      expect(updateVerifySettings).toHaveBeenCalledWith(
        { test: "./task check-a" },
        "a",
      );
      expect(suggestion.suggesting()).toBe(false);
      dispose();
    });
  });
});
