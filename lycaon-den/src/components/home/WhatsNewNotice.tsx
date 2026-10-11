import { Show, createEffect, createSignal } from "solid-js";
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
import { WhatsNewDialog } from "./WhatsNewDialog.tsx";

export type WhatsNewNoticeProps = {
  notices: NoticeStore;
  /** Override /health version (tests). */
  currentVersion?: string | null;
  /** Override notes lookup (tests). */
  notesFor?: (version: string) => string;
  /** Override latch write (tests). */
  saveLatch?: (version: string) => Promise<void>;
};

type VisibleCard = { version: string; notes: string };

/** Release acknowledgement belongs to the shared app notice. */
export function WhatsNewNotice(props: WhatsNewNoticeProps): JSX.Element {
  const [visible, setVisible] = createSignal<VisibleCard | null>(null);
  const [applying, setApplying] = createSignal(false);
  const [reading, setReading] = createSignal(false);

  createEffect(() => {
    if (applying()) return;

    const current =
      props.currentVersion !== undefined
        ? props.currentVersion
        : (lastSeenHealth()?.version ?? undefined);
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

  createEffect(() => {
    const card = visible();
    props.notices.withdraw("whats_new", APP_SCOPE);
    if (!card) return;
    props.notices.publish({
      code: "whats_new",
      severity: "info",
      title: `What’s new in ${card.version}`,
      message: "Review the latest changes when you have a moment.",
      interaction: {
        autoDismiss: false,
        onDismiss: () => void onGotIt(),
        actions: [
          { id: "read_release_notes", label: "Read more", variant: "secondary", dismiss: false, run: () => setReading(true) },
          { id: "acknowledge_release", label: "Got it", variant: "primary", run: () => {} },
        ],
      },
    }, APP_SCOPE);
  });

  return <Show when={visible()} keyed>{(card) => <WhatsNewDialog
    open={reading()}
    version={card.version}
    notes={card.notes}
    busy={applying()}
    onClose={() => setReading(false)}
    onGotIt={() => void onGotIt()}
  />}</Show>;
}
