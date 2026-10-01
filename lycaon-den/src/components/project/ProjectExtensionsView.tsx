import { Show, createEffect, createSignal, onMount } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { useSettingsBackend } from "../../settings/settings-backend.ts";
import { BrowseStagePanel } from "../browse/BrowseStagePanel.tsx";
import type { StageBack } from "../shell/StageBackChip.tsx";
import { ExtensionsSettingsPanel } from "../settings/extensions/ExtensionsSettingsPanel.tsx";
import { requestFirstTimeTip } from "../../first-time-tips/first-time-tips-service.ts";

type Props = {
  projectId: string;
  appStore: AppStore;
  onOpenScanners?: () => void;
  /** Set when a routed jump (Crossbar go-to, card CTA) landed here. */
  back?: StageBack | null;
};

/** Project Context → Extensions — Active / Installed / Config for this workspace. */
export function ProjectExtensionsView(props: Props) {
  const { client } = useSettingsBackend(props.appStore);
  const [catalogReady, setCatalogReady] = createSignal(false);
  createEffect(() => {
    props.projectId;
    setCatalogReady(false);
  });
  onMount(() => requestFirstTimeTip("project-extensions"));

  return (
    <section
      class="project-extensions-view"
      data-testid="project-extensions-view"
    >
      <BrowseStagePanel
        appStore={props.appStore}
        back={props.back}
        scrollHost="main"
        contentReady={catalogReady}
      >
        <Show when={client()} keyed>
          {(c: LycaonClient) => (
            <ExtensionsSettingsPanel
              client={c}
              surface="project"
              projectId={props.projectId}
              extensionsRevision={() => props.appStore.state.extensionsRevision}
              onOpenScanners={props.onOpenScanners}
              onReadyChange={setCatalogReady}
            />
          )}
        </Show>
      </BrowseStagePanel>
    </section>
  );
}
