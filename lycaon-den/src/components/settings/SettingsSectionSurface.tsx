import { createEffect, untrack, type JSX } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { Project } from "../../api/types.ts";
import type { SettingsStore } from "../../store/settings-store.ts";
import { loadSettingsPanel, type SettingsStorePanel } from "../../settings/settings-actions.ts";
import { usePresentationParticipant } from "../../ui/presentation-context.tsx";
import { useResidentLive } from "../../ui/resident-activity.ts";

/** Starts required section acquisition before evaluating the section's render tree. */
export function SettingsSectionSurface(props: {
  panel?: SettingsStorePanel;
  client: LycaonClient | null;
  store: SettingsStore;
  projects: readonly Project[];
  projectDir?: string;
  projectScope?: boolean;
  children: JSX.Element;
}) {
  const live = useResidentLive();
  usePresentationParticipant("settings-data", () => !props.panel || !props.client || props.store.panelReady(props.client, props.panel),
    () => {
      const message = props.client && props.panel ? props.store.panelError(props.client, props.panel) : undefined;
      return message ? { tone: "error", message } : null;
    }, () => {
      if (!props.client || !props.panel) return;
      props.store.invalidate();
      void loadSettingsPanel(props.store, props.client, props.projects, props.panel, props.projectDir, { alwaysProjectScope: props.projectScope });
    });
  let activated = false;
  createEffect(() => {
    const client = props.client;
    const panel = props.panel;
    if (!live() || !client || !panel) return;
    if (activated) props.store.invalidate();
    activated = true;
    untrack(() => void loadSettingsPanel(props.store, client, props.projects, panel, props.projectDir, { alwaysProjectScope: props.projectScope }));
  });
  return <>{props.children}</>;
}
