import { createSignal, Show } from "solid-js";
import type { JSX } from "solid-js";
import { ReportBugDialog } from "../../report-a-bug/ReportBugDialog.tsx";
import { DenButton } from "../../primitives/DenButton.tsx";
import { openThirdPartyNotices } from "../../../platform/desktop/open-third-party-notices.ts";
import { confirmAndOpenExternalLink } from "../../../platform/desktop/external-link.ts";
import { writeClipboardText } from "../../../utils/clipboard.ts";
import { settingAnchor, settingLabel } from "../../../settings/settings-registry.ts";
import {
  PRODUCT_NAME,
  REPOSITORY_URL,
  WEBSITE_URL,
} from "../../../../shared/brand.ts";

export type AboutPanelProps = {
  version: string | null;
};

export function AboutSettingsPanel(props: AboutPanelProps): JSX.Element {
  const [reportOpen, setReportOpen] = createSignal(false);
  const [noticesError, setNoticesError] = createSignal<string | null>(null);
  const [linkCopied, setLinkCopied] = createSignal(false);

  async function onOpenNotices(): Promise<void> {
    setNoticesError(null);
    try {
      await openThirdPartyNotices();
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      setNoticesError(msg || "Could not open third-party notices");
    }
  }

  return (
    <div class="den-settings-section" data-testid="about-settings-panel">
      <section class="den-settings-subsection">
        <h3 class="den-settings-subhead">Version</h3>
        <p class="den-settings-hint" data-testid="about-version">
          {props.version ? `Version ${props.version}` : "Version unavailable (not connected)"}
        </p>
      </section>

      <section
        class="den-settings-subsection"
        data-testid="about-share"
        {...settingAnchor("tell-a-friend")}
      >
        <h3 class="den-settings-subhead">{settingLabel("tell-a-friend")}</h3>
        <p class="den-settings-hint">
          If {PRODUCT_NAME} has been useful to you, the best thanks is telling
          a colleague who would use it, or starring the project on GitHub.
        </p>
        <div class="den-settings-actions">
          <DenButton
            variant="secondary"
            data-testid="about-copy-link"
            onClick={() => {
              void writeClipboardText(WEBSITE_URL).then(
                () => setLinkCopied(true),
                () => setLinkCopied(false),
              );
            }}
          >
            {linkCopied() ? "Link copied" : "Copy link"}
          </DenButton>
          <DenButton
            variant="secondary"
            data-testid="about-star"
            onClick={() => void confirmAndOpenExternalLink(REPOSITORY_URL)}
          >
            Star on GitHub
          </DenButton>
          <DenButton
            variant="ghost"
            data-testid="about-website"
            onClick={() => void confirmAndOpenExternalLink(WEBSITE_URL)}
          >
            Visit website
          </DenButton>
        </div>
      </section>

      <section class="den-settings-subsection" {...settingAnchor("privacy")}>
        <h3 class="den-settings-subhead">{settingLabel("privacy")}</h3>
        <p class="den-settings-hint" data-testid="about-privacy">
          This app collects no telemetry: no analytics, no crash reporting, no
          usage beacons. Your prompts and code go only to the AI provider you
          configured. Settings → Advanced → Diagnostics has a summary you can copy
          and a diagnostics bundle you can save — both stay on your machine.
        </p>
      </section>

      <section
        class="den-settings-subsection"
        {...settingAnchor("third-party-software")}
      >
        <h3 class="den-settings-subhead">{settingLabel("third-party-software")}</h3>
        <p class="den-settings-hint">
          License notices for bundled open-source components.
        </p>
        <DenButton
          variant="secondary"
          data-testid="about-third-party"
          onClick={() => void onOpenNotices()}
        >
          Third-party software
        </DenButton>
        <Show when={noticesError()}>
          {(msg) => (
            <p class="den-settings-hint" data-testid="about-third-party-error" role="alert">
              {msg()}
            </p>
          )}
        </Show>
      </section>

      <section class="den-settings-subsection">
        <h3 class="den-settings-subhead">Reports</h3>
        <DenButton
          variant="secondary"
          data-testid="about-report-bug"
          onClick={() => setReportOpen(true)}
        >
          Report a bug…
        </DenButton>
      </section>

      <Show when={reportOpen()}>
        <ReportBugDialog
          open={reportOpen()}
          onClose={() => setReportOpen(false)}
        />
      </Show>
    </div>
  );
}
