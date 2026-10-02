/** Help menu destinations that open over whatever view is showing. */
import { Show, createSignal, onCleanup, onMount } from "solid-js";
import type { JSX } from "solid-js";
import { WEBSITE_URL } from "../../shared/brand.ts";
import { WhatsNewDialog } from "../components/home/WhatsNewDialog.tsx";
import { ReportBugDialog } from "../components/report-a-bug/ReportBugDialog.tsx";
import { APP_SCOPE } from "../notices/notice-scope.ts";
import { getRegisteredNoticeStore } from "../platform/connection/app-connection.ts";
import { openAppLink } from "../platform/desktop/external-link.ts";
import { lastSeenHealth } from "../platform/connection/health.ts";
import { openThirdPartyNotices } from "../platform/desktop/open-third-party-notices.ts";
import { saveLastSeenVersion } from "../settings/system/whats-new-prefs.ts";
import {
  COMMAND_DECLINED,
  registerCommandHandler,
} from "../shortcuts/dispatcher.ts";
import { notesForVersion } from "../whats-new/changelog.ts";

type ReleaseNotes = { version: string; notes: string };

export function HelpMenuHost(): JSX.Element {
  const [reportOpen, setReportOpen] = createSignal(false);
  const [release, setRelease] = createSignal<ReleaseNotes | null>(null);
  const [saving, setSaving] = createSignal(false);

  onMount(() => {
    const detachers = [
      registerCommandHandler("help.whatsNew", () => {
        const version = lastSeenHealth()?.version;
        const notes = version ? notesForVersion(version) : "";
        if (!version || !notes) return COMMAND_DECLINED;
        setRelease({ version, notes });
        return undefined;
      }),
      registerCommandHandler("help.reportBug", () => {
        setReportOpen(true);
      }),
      registerCommandHandler("help.website", () => {
        void openAppLink(WEBSITE_URL);
      }),
      registerCommandHandler("help.thirdPartyNotices", () => {
        void openThirdPartyNotices().catch((err: unknown) => {
          getRegisteredNoticeStore()?.reporterFor(APP_SCOPE).reportError(err);
        });
      }),
    ];
    onCleanup(() => {
      for (const detach of detachers) detach();
    });
  });

  // Reading the notes here acknowledges them the same way Home's card does.
  const acknowledge = async (version: string) => {
    setSaving(true);
    try {
      await saveLastSeenVersion(version);
      setRelease(null);
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <Show when={release()} keyed>
        {(notes) => (
          <WhatsNewDialog
            open
            version={notes.version}
            notes={notes.notes}
            busy={saving()}
            onClose={() => setRelease(null)}
            onGotIt={() => void acknowledge(notes.version)}
          />
        )}
      </Show>
      <Show when={reportOpen()}>
        <ReportBugDialog open={reportOpen()} onClose={() => setReportOpen(false)} />
      </Show>
    </>
  );
}
