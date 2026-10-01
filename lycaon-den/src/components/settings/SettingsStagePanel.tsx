import { Show, onCleanup, type JSX } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { useSettingsBackend } from "../../settings/settings-backend.ts";
import {
  isRegionEntry,
  regionEntryTarget,
  registerFocusRegion,
  releaseFocusRegion,
} from "../../shortcuts/focus-region.ts";
import { surfaceRevealDom, useSurfaceReveal } from "../../ui/surface-reveal.ts";
import { ChromeCloseButton } from "../shell/ChromeCloseButton.tsx";
import { StageBackChip, type StageBack } from "../shell/StageBackChip.tsx";

type Props = {
  appStore: AppStore;
  /** Set when a routed jump (card CTA, Crossbar go-to) landed here. */
  back?: StageBack | null;
  error?: string;
  errorTestId?: string;
  before?: JSX.Element;
  /** Panels that render without a connected client (e.g. local-only debug). */
  offline?: JSX.Element;
  children?: JSX.Element | ((client: LycaonClient) => JSX.Element);
  /** Exit the Settings stage (same as Escape). */
  onClose?: () => void;
  closeLabel?: string;
};

const SETTINGS_ENTRY_PRIORITY = ["input, select, textarea, button, [tabindex='0']"] as const;

/** Full-bleed settings and configuration stage. */
export function SettingsStagePanel(props: Props) {
  const { backendConnecting, client } = useSettingsBackend(props.appStore);
  const settingsFocusClaim = {};

  // Reveal after connection settles; panels keep local loading state.
  const boot = useSurfaceReveal({
    ready: () => !backendConnecting(),
    name: "settings-stage",
  });

  const bootAttrs = () => surfaceRevealDom(boot);

  return (
    <div
      class="den-settings-panel"
      tabindex="-1"
      classList={bootAttrs().classList}
      data-boot={bootAttrs()["data-boot"]}
      aria-busy={bootAttrs()["aria-busy"]}
      aria-hidden={bootAttrs()["aria-hidden"]}
      inert={bootAttrs().inert}
      ref={(el) => {
        registerFocusRegion("settings", el, settingsFocusClaim);
        const onFocus = () => {
          if (!isRegionEntry(el)) return;
          regionEntryTarget(el, SETTINGS_ENTRY_PRIORITY)?.focus();
        };
        el.addEventListener("focus", onFocus);
        onCleanup(() => {
          el.removeEventListener("focus", onFocus);
          releaseFocusRegion("settings", settingsFocusClaim);
        });
      }}
    >
      <Show when={props.back} keyed>
        {(back) => (
          <div class="den-settings-stage-back">
            <StageBackChip back={back} />
          </div>
        )}
      </Show>
      <Show when={props.onClose != null}>
        <ChromeCloseButton
          class="den-settings-stage-close den-inset-icon-btn"
          label={props.closeLabel ?? "Close settings"}
          testId="settings-stage-close"
          onClick={() => props.onClose?.()}
        />
      </Show>
      {props.before}
      <Show when={props.error}>
        <p class="den-settings-warn" data-testid={props.errorTestId} role="alert">
          {props.error}
        </p>
      </Show>
      <Show when={backendConnecting()}>
        <p class="den-settings-hint" role="status">Connecting to backend…</p>
      </Show>
      {props.offline}
      <Show when={client()} keyed>
        {(c) =>
          typeof props.children === "function" ? props.children(c) : props.children
        }
      </Show>
    </div>
  );
}
