import { preflightReport } from "../../../platform/persistence/preflight-report.ts";
import { createSignal } from "solid-js";
import type { JSX } from "solid-js";
import {
  lastSeenHostVersion,
  lastSeenSchemaVersion,
} from "../../../platform/connection/health.ts";
import { DEN_VERSION } from "../../../platform/desktop/den-version.ts";
import { writeClipboardText } from "../../../utils/clipboard.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { settingAnchor, settingLabel } from "../../../settings/settings-registry.ts";

/** Versions and readiness from the shared preflight store. */
export function SystemInfoPanel(): JSX.Element {
  const [copied, setCopied] = createSignal(false);

  const report = preflightReport;

  const summary = (): string => {
    const lines = [
      `app version: ${lastSeenHostVersion() ?? "unknown"}`,
      `den version: ${DEN_VERSION}`,
      `store schema_version: ${lastSeenSchemaVersion() ?? "unknown"}`,
    ];
    const r = report();
    if (r) {
      lines.push(`readiness: ${r.overall}`);
      for (const probe of r.probes) {
        lines.push(`  ${probe.id}: ${probe.status}${probe.code ? ` (${probe.code})` : ""}`);
      }
    } else {
      lines.push("readiness: unavailable");
    }
    return lines.join("\n");
  };

  return (
    <section
      class="den-settings-subsection"
      data-testid="system-info"
      {...settingAnchor("system-information")}
    >
      <h3 class="den-settings-subhead">{settingLabel("system-information")}</h3>
      <p class="den-settings-hint">
        A summary of what you are running and what the app found on this Mac.
        Copy it into a bug report — it contains no keys and nothing about your
        projects.
      </p>

      <pre class="den-settings-hint" data-testid="system-info-summary">
        {summary()}
      </pre>

      <DenButton
        variant="secondary"
        data-testid="system-info-copy"
        onClick={() => {
          void writeClipboardText(summary()).then(
            () => setCopied(true),
            () => setCopied(false),
          );
        }}
      >
        {copied() ? "Copied" : "Copy for bug report"}
      </DenButton>
    </section>
  );
}
