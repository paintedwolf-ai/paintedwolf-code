import { loadProjectTrust, openProjectTrust, projectTrustView, saveProjectTrust } from "../../settings/security/project-trust.ts";
import { showTrustReview } from "./trust-review-navigation.ts";
import { usePresentationParticipant } from "../../ui/presentation-context.tsx";
import { Show, createEffect, createSignal, on, onCleanup } from "solid-js";
import { useResidentInteractive } from "../../ui/resident-presence-context.tsx";
import { useResidentLive } from "../../ui/resident-activity.ts";
import { subscribeSourceInvalidation } from "../../files/source/source-invalidation.ts";
import { createBoundedDebouncedAsyncScheduler } from "../../store/coalesced-async.ts";
import { SOURCE_REFRESH_IDLE_MS, SOURCE_REFRESH_MAX_WAIT_MS } from "../../files/source/source-refresh.ts";
import type { LycaonClient } from "../../api/client.ts";
import type { TrustSurfaceId } from "../../api/types.ts";
import { TRUST_COPY } from "../../settings/security/trust-copy.ts";
import type { TrustTreeRoot } from "../../settings/security/trust-item-tree.ts";
import {
  presentSurfaces,
  replacesDefaultSurfaces,
  steeringSurfaces,
  suggestionSurfaces,
} from "../../settings/security/trust-model.ts";
import { SettingsScopeLede } from "../settings/SettingsScopeLede.tsx";
import { SettingsTextLink } from "../settings/SettingsTextLink.tsx";
import { SettingsEditorTitle, TrustIcon } from "../settings/SettingsEditorTitle.tsx";
import { SettingsListBackChrome } from "../settings/SettingsListBackChrome.tsx";
import { SettingsListChrome } from "../settings/SettingsListChrome.tsx";
import { SettingsListGroup } from "../settings/SettingsListGroup.tsx";
import { SettingsListPanel } from "../settings/SettingsListPanel.tsx";
import { ListSurfaceInbox } from "../list/ListSurfaceInbox.tsx";
import { TrustSurfaceDetail } from "./TrustSurfaceDetail.tsx";
import { TrustSurfaceRows } from "./TrustSurfaceRows.tsx";
import { chromeProps } from "../../styling/ui-chrome.ts";

type Props = {
  client: LycaonClient;
  projectId: string;
  projectRoots: readonly TrustTreeRoot[];
  counterpartLabel?: string;
  onOpenCounterpart?: () => void;
  onOpenExtensions?: () => void;
};

export function ProjectTrustPanel(props: Props) {
  const live = useResidentLive();
  const interactive = useResidentInteractive();
  const [resolved, setResolved] = createSignal(false);
  usePresentationParticipant("project-trust", resolved);
  const trust = () => projectTrustView(props.projectId).value;
  const [savingId, setSavingId] = createSignal<TrustSurfaceId | null>(null);
  const [selectedId, setSelectedId] = createSignal<TrustSurfaceId | null>(null);
  const [error, setError] = createSignal<string | null>(null);

  // Project changes invalidate in-flight panel work.
  let epoch = 0;
  let openingEpoch: number | null = null;
  let deferredLoad: boolean | null = null;

  const load = async (captureReview = false) => {
    if (openingEpoch !== null) {
      deferredLoad = (deferredLoad ?? false) || captureReview;
      return;
    }
    if (savingId() !== null) {
      deferredLoad = (deferredLoad ?? false) || captureReview;
      return;
    }
    const mine = ++epoch;
    if (captureReview) openingEpoch = mine;
    setResolved(false);
    setError(null);
    try {
      if (captureReview) await openProjectTrust(props.client, props.projectId);
      else await loadProjectTrust(props.client, props.projectId);
      if (mine !== epoch) return;
      setResolved(true);
    } catch (err) {
      if (mine !== epoch) return;
      setResolved(true);
      setError(err instanceof Error ? err.message : TRUST_COPY.loadError);
    } finally {
      if (openingEpoch === mine) {
        openingEpoch = null;
        const pending = deferredLoad;
        deferredLoad = null;
        if (pending !== null && live()) void load(pending);
      }
    }
  };

  createEffect(on(() => [props.projectId, props.client], () => {
    epoch++;
    openingEpoch = null;
    deferredLoad = null;
    setResolved(false);
    setSelectedId(null);
    setSavingId(null);
    if (live()) void load(interactive());
  }));
  createEffect(on(() => [live(), interactive()], () => {
    if (live()) void load(interactive());
  }, { defer: true }));
  const sourceRefresh = createBoundedDebouncedAsyncScheduler(async () => {
    if (live()) await load();
  }, SOURCE_REFRESH_IDLE_MS, SOURCE_REFRESH_MAX_WAIT_MS);
  createEffect(on(() => JSON.stringify(props.projectRoots), () => {
    if (live()) sourceRefresh.schedule();
  }, { defer: true }));
  onCleanup(subscribeSourceInvalidation((scope) => {
    if (live() && scope.projectId === props.projectId) sourceRefresh.schedule();
  }));
  onCleanup(() => {
    epoch++;
    sourceRefresh.cancel();
  });

  const steering = () => {
    const t = trust();
    return t ? steeringSurfaces(t) : [];
  };
  const replaces = () => {
    const t = trust();
    return t ? replacesDefaultSurfaces(t) : [];
  };
  const suggestions = () => {
    const t = trust();
    return t ? suggestionSurfaces(t) : [];
  };
  const surfaceCount = () => {
    const t = trust();
    return t ? presentSurfaces(t).length : 0;
  };
  const selectedSurface = () => {
    const id = selectedId();
    const current = trust();
    return id && current
      ? presentSurfaces(current).find((surface) => surface.id === id)
      : undefined;
  };

  const setEnabled = async (id: TrustSurfaceId, enabled: boolean) => {
    const mine = ++epoch;
    setSavingId(id);
    setError(null);
    try {
      await saveProjectTrust(props.client, props.projectId, {
        enabled: { [id]: enabled },
      });
      if (mine !== epoch) return;
      setResolved(true);
    } catch (err) {
      if (mine !== epoch) return;
      setError(err instanceof Error ? err.message : TRUST_COPY.saveError);
    } finally {
      if (mine === epoch) {
        setSavingId(null);
        const captureReview = deferredLoad;
        deferredLoad = null;
        if (captureReview !== null && live()) void load(captureReview);
      }
    }
  };

  return (
    <div class="den-settings-editor--project den-settings-editor" data-testid="project-trust-panel">
      <SettingsEditorTitle icon={<TrustIcon />}>{TRUST_COPY.panelTitle}</SettingsEditorTitle>
      <SettingsScopeLede
        scope="project"
        counterpartLabel={props.counterpartLabel}
        onOpenCounterpart={props.onOpenCounterpart}
      >
        {TRUST_COPY.panelLede}
      </SettingsScopeLede>

      <Show when={trust()}>{snapshot => <SettingsTextLink testId="project-trust-open-review" onClick={() => showTrustReview(props.projectId)}>
        View {snapshot().review.changes.length} {snapshot().review.changes.length === 1 ? "file change" : "file changes"}
      </SettingsTextLink>}</Show>
      <Show when={error()}>
        <p class="den-settings-warn" data-testid="project-trust-error">
          {error()}
        </p>
      </Show>

      <Show
        when={trust() !== null}
        fallback={
          <Show when={!resolved()}>
            <p class="den-settings-hint">Loading…</p>
          </Show>
        }
      >
        <Show
          when={surfaceCount() > 0}
          fallback={
            <div class="den-settings-pref-group" data-testid="project-trust-empty">
              <div
                class="den-settings-pref-group-title den-settings-pref-group-title--prominent"
                {...chromeProps()}
              >
                {TRUST_COPY.panelEmptyTitle}
              </div>
              <div class="den-settings-pref-row">
                <p class="den-settings-pref-copy">{TRUST_COPY.panelEmptyBody}</p>
              </div>
            </div>
          }
        >
          <SettingsListPanel
            testId="project-trust-list-panel"
            chrome={
              selectedSurface() ? (
                <SettingsListBackChrome
                  label={TRUST_COPY.detailBack}
                  testId="project-trust-detail-back"
                  onBack={() => setSelectedId(null)}
                />
              ) : (
                <SettingsListChrome
                  testId="project-trust-list-chrome"
                  count={TRUST_COPY.listCount(surfaceCount())}
                />
              )
            }
          >
            <ListSurfaceInbox
              detailOpen={selectedSurface() != null}
              listTestId="project-trust-list"
              detailTestId="project-trust-detail"
              list={
                <>
                  <Show when={steering().length > 0}>
                    <SettingsListGroup
                      label={TRUST_COPY.groupSteering}
                      testId="project-trust-steering"
                    >
                      <TrustSurfaceRows
                        surfaces={steering()}
                        onToggle={(id, on) => void setEnabled(id, on)}
                        onSelect={(surface) => setSelectedId(surface.id)}
                        savingId={savingId()}
                        testIdPrefix="project-trust-steering"
                      />
                    </SettingsListGroup>
                  </Show>

                  <Show when={replaces().length > 0}>
                    <SettingsListGroup
                      label={TRUST_COPY.groupReplacesDefaults}
                      testId="project-trust-replaces"
                    >
                      <TrustSurfaceRows
                        surfaces={replaces()}
                        onToggle={(id, on) => void setEnabled(id, on)}
                        onSelect={(surface) => setSelectedId(surface.id)}
                        savingId={savingId()}
                        testIdPrefix="project-trust-replaces"
                      />
                    </SettingsListGroup>
                  </Show>

                  <Show when={suggestions().length > 0}>
                    <SettingsListGroup
                      label={TRUST_COPY.groupSuggestions}
                      testId="project-trust-suggestions"
                    >
                      <TrustSurfaceRows
                        surfaces={suggestions()}
                        onToggle={(id, on) => void setEnabled(id, on)}
                        onSelect={(surface) => setSelectedId(surface.id)}
                        savingId={savingId()}
                        testIdPrefix="project-trust-suggestions"
                      />
                    </SettingsListGroup>
                  </Show>
                </>
              }
              detail={
                <Show when={selectedSurface()} keyed>
                  {(surface) => (
                    <TrustSurfaceDetail
                      projectId={props.projectId}
                      projectRoots={props.projectRoots}
                      surface={surface}
                    />
                  )}
                </Show>
              }
            />
          </SettingsListPanel>

          <Show when={suggestions().length > 0}>
            <p class="den-settings-hint">
              {TRUST_COPY.suggestionsNoteBefore}
              <Show when={props.onOpenExtensions} fallback={TRUST_COPY.suggestionsNotePath}>
                <SettingsTextLink
                  testId="project-trust-open-extensions"
                  onClick={() => props.onOpenExtensions?.()}
                >
                  {TRUST_COPY.suggestionsNotePath}
                </SettingsTextLink>
              </Show>
              {TRUST_COPY.suggestionsNoteAfter}
            </p>
          </Show>

          <p class="den-settings-hint" data-testid="project-trust-device-note">
            {TRUST_COPY.deviceNoteBefore}
            <Show when={props.onOpenCounterpart} fallback={TRUST_COPY.deviceNotePath}>
              <SettingsTextLink
                testId="project-trust-open-device"
                onClick={() => props.onOpenCounterpart?.()}
              >
                {TRUST_COPY.deviceNotePath}
              </SettingsTextLink>
            </Show>
            {TRUST_COPY.deviceNoteAfter}
          </p>
        </Show>
      </Show>
      <Show when={savingId()}>
        <span class="sr-only" role="status">
          Saving…
        </span>
      </Show>
    </div>
  );
}
