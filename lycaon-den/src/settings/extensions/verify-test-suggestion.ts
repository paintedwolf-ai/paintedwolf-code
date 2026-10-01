import { createEffect, createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { VerifySettingsResponse } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";

export type VerifyTestSuggestion = {
  /** The command the host detected, or "" when there is nothing to suggest. */
  detectedCommand: () => string;
  /** Where it was detected — falls back to generic copy the card can print. */
  detectedSource: () => string;
  /** True only while the host says this project should be asked. */
  suggesting: () => boolean;
  busy: () => boolean;
  accept: () => Promise<void>;
  dismiss: () => Promise<void>;
};

// Presence is host `suggestion_state`. A card that fetched on mount would
// request once per dock read.
export function createVerifyTestSuggestion(args: {
  /** Reactive: null until the backend is connected. */
  client: () => LycaonClient | null;
  appStore: AppStore;
  projectId: () => string;
}): VerifyTestSuggestion {
  const [doc, setDoc] = createSignal<VerifySettingsResponse | null>(null);
  const [busy, setBusy] = createSignal(false);
  // Answers land out of order when the project changes mid-flight; only the
  // newest load may write, or a switched-away project's suggestion shows here.
  let loadSerial = 0;

  createEffect(() => {
    // Re-fetch when async detection publishes a settings event (proposal landed).
    args.appStore.state.verifyDetectRevision;
    const client = args.client();
    const projectId = args.projectId();
    const serial = ++loadSerial;
    if (!client || !projectId) {
      setDoc(null);
      return;
    }
    void (async () => {
      try {
        const next = await client.getVerifySettings(projectId);
        if (serial === loadSerial) setDoc(next);
      } catch {
        if (serial === loadSerial) setDoc(null);
      }
    })();
  });

  const detectedCommand = () => doc()?.detected_command?.trim() ?? "";
  const suggesting = () =>
    doc()?.suggestion_state === "suggest" && detectedCommand() !== "";

  const write = async (
    run: (client: LycaonClient, projectId: string) => Promise<VerifySettingsResponse>,
  ) => {
    const client = args.client();
    const projectId = args.projectId();
    if (!client || !projectId || busy()) return;
    setBusy(true);
    try {
      const next = await run(client, projectId);
      // The write answers for the project it was sent for; a switch since then
      // controls the view, and its own load is already in flight.
      if (projectId === args.projectId()) setDoc(next);
    } finally {
      setBusy(false);
    }
  };

  return {
    detectedCommand,
    detectedSource: () => doc()?.detected_source?.trim() ?? "",
    suggesting,
    busy,
    accept: async () => {
      const command = detectedCommand();
      if (!command) return;
      await write((client, projectId) =>
        client.updateVerifySettings({ test: command }, projectId),
      );
    },
    dismiss: () =>
      write((client, projectId) =>
        client.dismissVerifySettings({ dismissed: true }, projectId),
      ),
  };
}
