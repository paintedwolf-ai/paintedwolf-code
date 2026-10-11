import { Show, createEffect, createSignal, createMemo, on, onCleanup } from "solid-js";
import type { JSX } from "solid-js";
import { lastSeenHealth } from "../../platform/connection/health.ts";
import {
  firstRunSetupCompletedPref,
  onboardingPrefsReady,
} from "../../settings/system/onboarding-prefs.ts";
import {
  saveLastSeenVersion,
  whatsNewLastSeenVersion,
  whatsNewPrefsReady,
} from "../../settings/system/whats-new-prefs.ts";
import { notesForVersion } from "../../whats-new/changelog.ts";
import { decideWhatsNew } from "../../whats-new/whats-new-gate.ts";
import { shouldShowOnboardingHardGate } from "../onboarding/onboarding-gate-model.ts";
import type { NoticeStore } from "../../notices/notice-store.ts";
import { APP_SCOPE } from "../../notices/notice-scope.ts";
import { CLIENT_NOTICES } from "../../notices/client-notices.generated.ts";
import { WhatsNewDialog } from "./WhatsNewDialog.tsx";

export type WhatsNewNoticeProps = {
  notices: NoticeStore;
  currentVersion?: string | null;
  notesFor?: (version: string) => string;
  saveLatch?: (version: string) => Promise<void>;
};

type ReleaseNotes = { version: string; notes: string };

/** Release acknowledgement belongs to the shared app notice. */
export function WhatsNewNotice(props: WhatsNewNoticeProps): JSX.Element {
  const [visible, setVisible] = createSignal<ReleaseNotes | null>(null);
  const [applying, setApplying] = createSignal(false);
  const [reading, setReading] = createSignal(false);

  const currentVersion = createMemo(() => props.currentVersion !== undefined
    ? props.currentVersion : lastSeenHealth()?.version);

  createEffect(() => {
    if (applying()) return;
    const current = currentVersion();
    if (current === undefined) return;

    const notesFn = props.notesFor ?? notesForVersion;
    const notes = current ? notesFn(current) : "";
    const decision = decideWhatsNew({
      currentVersion: current,
      lastSeenVersion: whatsNewLastSeenVersion(),
      notes,
      prefsReady: whatsNewPrefsReady(),
      onboardingHardGate: shouldShowOnboardingHardGate({
        prefsReady: onboardingPrefsReady(),
        firstRunSetupCompleted: firstRunSetupCompletedPref(),
      }),
    });

    if (decision.kind === "hide") {
      setVisible(null);
      setReading(false);
      return;
    }
    if (decision.kind === "show") {
      setVisible({ version: decision.version, notes: decision.notes });
      return;
    }

    setVisible(null);
    setApplying(true);
    const save = props.saveLatch ?? saveLastSeenVersion;
    void save(decision.version).finally(() => setApplying(false));
  });

  const onGotIt = async () => {
    const card = visible();
    if (!card) return;
    const save = props.saveLatch ?? saveLastSeenVersion;
    setApplying(true);
    try {
      await save(card.version);
      setVisible(null);
      setReading(false);
    } finally {
      setApplying(false);
    }
  };

  onCleanup(() => props.notices.withdraw("whats_new", APP_SCOPE));
  createEffect(on(visible, (card) => {
    props.notices.withdraw("whats_new", APP_SCOPE);
    if (!card) return;
    props.notices.publish({
      code: "whats_new",
      severity: "info",
      title: `${CLIENT_NOTICES.whats_new.title} ${card.version}`,
      message: CLIENT_NOTICES.whats_new.message,
      interaction: {
        autoDismiss: false,
        onDismiss: () => void onGotIt(),
        actions: [
          { id: "read_release_notes", label: "Read more", variant: "secondary", dismiss: false, run: () => setReading(true) },
          { id: "acknowledge_release", label: "Got it", variant: "primary", run: () => {} },
        ],
      },
    }, APP_SCOPE);
  }));

  return <Show when={visible()} keyed>{(card) => <WhatsNewDialog
    open={reading()}
    version={card.version}
    notes={card.notes}
    busy={applying()}
    onClose={() => setReading(false)}
    onGotIt={() => void onGotIt()}
  />}</Show>;
}
