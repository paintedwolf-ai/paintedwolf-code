import { Show, createSignal } from "solid-js";
import type { JSX } from "solid-js";
import { getBackendConnection } from "../../../platform/connection/backend.ts";
import { saveDiagnosticsBundle } from "../../../settings/system/diagnostics-export.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { settingAnchor, settingLabel } from "../../../settings/settings-registry.ts";

export function DiagnosticsExportPanel(): JSX.Element {
  const [busy, setBusy] = createSignal(false);
  const [message, setMessage] = createSignal<string | null>(null);

  const run = async () => {
    const connection = getBackendConnection();
    if (!connection) {
      setMessage("Not connected to the engine — start it and try again.");
      return;
    }

    setBusy(true);
    setMessage(null);
    try {
      const result = await saveDiagnosticsBundle(connection);
      switch (result.kind) {
        case "saved":
          setMessage(`Saved as ${result.path}. Paste the filename into a report — do not attach the zip.`);
          break;
        case "cancelled":
          setMessage(null);
          break;
        case "failed":
          setMessage(`Could not build the diagnostics file (${result.detail}).`);
          break;
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <section
      class="den-settings-section"
      data-testid="diagnostics-export"
      {...settingAnchor("diagnostics-bundle")}
    >
      <h3 class="den-settings-subhead">{settingLabel("diagnostics-bundle")}</h3>
      <p class="den-settings-hint">
        Saves a zip with your app version, the readiness checks, your settings,
        and recent logs. API keys and tokens are removed, and the credential
        files are left out entirely. It is saved to your computer — nothing is
        uploaded.
      </p>

      <DenButton
        variant="secondary"
        data-testid="diagnostics-export-save"
        disabled={busy()}
        onClick={() => void run()}
      >
        {busy() ? "Preparing…" : "Save diagnostics bundle"}
      </DenButton>

      <Show when={message()} keyed>
        {(text) => (
          <p class="den-settings-hint" data-testid="diagnostics-export-status">
            {text}
          </p>
        )}
      </Show>
    </section>
  );
}
