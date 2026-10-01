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
import { SystemNudge } from "../SystemNudge.tsx";
import { WhatsNewDialog } from "./WhatsNewDialog.tsx";

export type WhatsNewCardProps = {
  /** Override /health version (tests). */
  currentVersion?: string | null;
  /** Override notes lookup (tests). */
  notesFor?: (version: string) => string;
  /** Override latch write (tests). */
  saveLatch?: (version: string) => Promise<void>;
};

type VisibleCard = { version: string; notes: string };

/**
 * Dismissible Home What's New card. Not a modal — never blocks work.
 * Latch is set on Got it (or silent seed / empty-notes advance), not on paint.
 */
export function WhatsNewCard(props: WhatsNewCardProps): JSX.Element {
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

  return (
    <Show when={visible()} keyed>
      {(card) => (
        <>
          <div class="whats-new-card" data-testid="whats-new-card">
            <SystemNudge
              testId="whats-new-nudge"
              title={`What’s new in ${card.version}`}
              description={
                <div class="whats-new-card__summary" data-testid="whats-new-summary">
                  <span class="whats-new-card__eyebrow">New release</span>
                  <span>Review the latest changes when you have a moment.</span>
                </div>
              }
              secondaryAction={{
                label: "Read more",
                testId: "whats-new-read-more",
                disabled: applying(),
                onClick: () => {
                  setReading(true);
                },
              }}
              primaryAction={{
                label: "Got it",
                testId: "whats-new-got-it",
                disabled: applying(),
                onClick: () => void onGotIt(),
              }}
              onDismiss={() => void onGotIt()}
            />
          </div>
          <WhatsNewDialog
            open={reading()}
            version={card.version}
            notes={card.notes}
            busy={applying()}
            onClose={() => setReading(false)}
            onGotIt={() => void onGotIt()}
          />
        </>
      )}
    </Show>
  );
}
