import { Show, createSignal, type JSX } from "solid-js";
import type { AppStore } from "../../store/app-state-model.ts";
import { useSettingsBackend } from "../../settings/settings-backend.ts";
import { surfaceRevealDom, useSurfaceReveal } from "../../ui/surface-reveal.ts";
import { LAYOUT_BAND_SCALES, bindLayoutBand } from "../../layout/layout-bands.ts";
import {
  clampListPaneSizePx,
  listPaneSizeMinPx,
  maxListPaneSizePx,
  resolveListPaneSizePx,
  type ListPaneAxis,
} from "../../list/list-pane-model.ts";
import {
  beginListPaneResize,
  listPanePrefs,
  resetListPaneSize,
} from "../../shell/layout-store.ts";
import { observeElementExtent } from "../../layout/element-extent.ts";
import { ResizeHandle } from "../primitives/ResizeHandle.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { BrowseChrome } from "./BrowseChrome.tsx";
import type { StageBack } from "../shell/StageBackChip.tsx";

export type BrowseStagePanelProps = {
  appStore: AppStore;
  /** Leading back affordance when the stage was entered via a routed open. */
  back?: StageBack | null;
  title?: JSX.Element;
  primary?: JSX.Element;
  overflow?: JSX.Element;
  chips?: JSX.Element;
  meta?: JSX.Element;
  children: JSX.Element;
  detail?: JSX.Element | null;
  error?: string;
  errorTestId?: string;
  /** Controls backend readiness gating. */
  requireClient?: boolean;
  /** True after the initial body state settles. */
  contentReady?: () => boolean;
  /** Selects the vertical scroll host. */
  scrollHost: "main" | "content";
  testId?: string;
  /** Persisted detail-pane surface id. */
  detailSurface?: string;
};

/** Context stage with shared chrome and an optional detail pane. */
export function BrowseStagePanel(props: BrowseStagePanelProps) {
  const requireClient = () => props.requireClient !== false;
  const { backendConnecting, client } = useSettingsBackend(props.appStore);

  const boot = useSurfaceReveal({
    ready: () =>
      (!backendConnecting() || !requireClient()) &&
      (props.contentReady?.() ?? true),
    name: "browse-stage",
  });
  const bootAttrs = () => surfaceRevealDom(boot);

  const showBody = () => {
    if (!requireClient()) return true;
    return Boolean(client());
  };

  const [bodyEl, setBodyEl] = createSignal<HTMLDivElement>();
  const surface = () => props.detailSurface ?? "";
  const resizable = () => Boolean(props.detailSurface) && props.detail != null;

  // Observe pane limits outside pointer handlers.
  const bodyExtent = observeElementExtent(bodyEl);
  const bodySize = (axis: ListPaneAxis) =>
    axis === "width" ? bodyExtent.width() : bodyExtent.height();

  const paneSize = (axis: ListPaneAxis) =>
    resolveListPaneSizePx(surface(), listPanePrefs(surface()), axis, bodySize(axis));

  // Scope live geometry to the detail pane.
  const paneStyle = () => {
    if (!props.detailSurface) return undefined;
    const prefs = listPanePrefs(surface());
    const style: Record<string, string> = {};
    if (prefs.widthPx != null) {
      style["--den-detail-w"] = `${clampListPaneSizePx(surface(), "width", prefs.widthPx, bodySize("width"))}px`;
    }
    if (prefs.heightPx != null) {
      style["--den-detail-h"] = `${clampListPaneSizePx(surface(), "height", prefs.heightPx, bodySize("height"))}px`;
    }
    return Object.keys(style).length > 0 ? style : undefined;
  };

  return (
    <div
      ref={(el) => bindLayoutBand(el, LAYOUT_BAND_SCALES.browseStage)}
      class="den-browse-stage"
      classList={{
        ...bootAttrs().classList,
        "den-browse-stage--with-detail": props.detail != null,
      }}
      data-testid={props.testId ?? "browse-stage-panel"}
      data-boot={bootAttrs()["data-boot"]}
      aria-busy={bootAttrs()["aria-busy"]}
      aria-hidden={bootAttrs()["aria-hidden"]}
      inert={bootAttrs().inert}
    >
      <BrowseChrome
        variant="stage"
        back={props.back}
        title={props.title}
        primary={props.primary}
        overflow={props.overflow}
        chips={props.chips}
        meta={props.meta}
      />
      <Show when={props.error}>
        <p class="den-browse-stage__error" data-testid={props.errorTestId}>
          {props.error}
        </p>
      </Show>
      <Show when={requireClient() && backendConnecting()}>
        <p class="den-browse-stage__hint">Connecting to backend…</p>
      </Show>
      <Show when={showBody()}>
        <div
          ref={setBodyEl}
          class="den-browse-body"
          classList={{
            "den-browse-body--with-detail": props.detail != null,
            "den-browse-body--split": resizable(),
          }}
        >
          <Show
            when={props.scrollHost === "main"}
            fallback={
              <div class="den-browse-main den-browse-main--content-scroll">
                {props.children}
              </div>
            }
          >
            <Scrollport
              class="den-browse-main den-browse-main--scroll"
              contentClass="den-browse-main__content"
            >
              {props.children}
            </Scrollport>
          </Show>
          <Show when={resizable()}>
            <ResizeHandle
              class="den-browse-split"
              // CSS selects the responsive axis.
              axis="auto"
              // The handle sits on the pane's leading edge.
              direction={-1}
              size={paneSize}
              clamp={(value, axis) =>
                clampListPaneSizePx(surface(), axis, value, bodySize(axis))
              }
              onBegin={(axis, startSize) =>
                beginListPaneResize(
                  surface(),
                  axis,
                  startSize,
                  bodySize(axis),
                )
              }
              onReset={(axis) => void resetListPaneSize(surface(), axis)}
              rootClass="den-stage--split-resizing"
              ariaLabel="Resize details pane"
              ariaMin={(axis) => listPaneSizeMinPx(surface(), axis)}
              ariaMax={(axis) =>
                maxListPaneSizePx(surface(), axis, bodySize(axis))
              }
              tip="Drag to resize details — double-click to reset"
              testId="detail-resize"
            />
          </Show>
          <Show when={props.detail != null}>
            <aside class="den-browse-detail" style={paneStyle()}>
              {props.detail}
            </aside>
          </Show>
        </div>
      </Show>
    </div>
  );
}
