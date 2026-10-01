import { createEffect, createMemo, createSignal, onCleanup, untrack } from "solid-js";
import { observeSharedContentBox } from "../../../layout/shared-resize-observer.ts";
import { fontSelection } from "../../../settings/appearance/font-prefs.ts";
import { effectiveTextScale } from "../../../platform/desktop/accessibility-text-size.ts";
import { onShellLayoutSettled } from "../../../shell/shell-layout-busy.ts";
import { TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT } from "../presentation/transcript-disclosure.tsx";
import { observeTranscriptRowMutations } from "./transcript-row-mutations.ts";
import { transcriptLayoutRevision } from "./transcript-layout-revision.ts";
import { publishTranscriptRootRemPx, transcriptGeometryKey, transcriptInteriorSignature, transcriptSpacing, type TranscriptTypography } from "./transcript-spacing.ts";
import type { TranscriptRowMetrics } from "./transcript-row-content-estimate.ts";
import type { MeasuredTranscriptVirtualizer } from "./transcript-virtualizer.ts";

/** Coordinates column metrics, row observations, and explicit layout invalidations. */
export function createTranscriptGeometry(opts: {
  root: () => HTMLElement | null | undefined;
  enabled: () => boolean;
  viewport: () => HTMLElement | null;
}) {
  const [origin, setOrigin] = createSignal(0);
  const measureOrigin = () => {
    const root = opts.root();
    const viewport = opts.viewport();
    if (!root?.isConnected || !viewport?.isConnected || !opts.enabled()) return;
    setOrigin(root.getBoundingClientRect().top - viewport.getBoundingClientRect().top + viewport.scrollTop);
  };
  let virtualizer: MeasuredTranscriptVirtualizer | undefined;
  const [column, setColumn] = createSignal<{
    dimensions: Omit<TranscriptRowMetrics, "spacing">;
    typography: TranscriptTypography;
  } | null>(null);
  const interior = createMemo(() => transcriptInteriorSignature(transcriptSpacing()));
  const metrics = createMemo<TranscriptRowMetrics | null>(() => {
    interior();
    const size = column()?.dimensions;
    return size ? { ...size, spacing: untrack(transcriptSpacing) } : null;
  });
  const key = createMemo(() => {
    const size = metrics();
    const measured = column();
    return size && measured ? transcriptGeometryKey(size, interior(), measured.typography) : undefined;
  });
  const republish = () => virtualizer?.remeasureMounted();

  createEffect(() => {
    const root = opts.root();
    if (!root || !opts.enabled()) return;
    let disposed = false;
    let width = 0;
    let samplePending = false;
    const sample = () => {
      samplePending = false;
      if (disposed || width <= 0) return;
      measureOrigin();
      const remPx = Number.parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
      const bodyPx = Number.parseFloat(getComputedStyle(root).fontSize) || remPx;
      // Seams are rem in CSS and px in virtual geometry; one sample feeds both.
      publishTranscriptRootRemPx(remPx);
      const typography = { uiFont: fontSelection("ui"), monoFont: fontSelection("mono"),
        scale: effectiveTextScale(), revision: transcriptLayoutRevision() };
      const previous = untrack(column);
      if (previous?.dimensions.widthPx === width && previous.dimensions.remPx === remPx && previous.dimensions.bodyPx === bodyPx &&
          previous.typography.uiFont === typography.uiFont && previous.typography.monoFont === typography.monoFont &&
          previous.typography.scale === typography.scale && previous.typography.revision === typography.revision) return;
      // Publish identity and measured metrics together after the stylesheet update.
      setColumn({ dimensions: { widthPx: width, remPx, bodyPx }, typography });
    };
    const scheduleSample = () => {
      if (samplePending) return;
      samplePending = true;
      queueMicrotask(sample);
    };
    const stop = observeSharedContentBox(root, (box) => {
      if (box.width <= 0 || width === box.width) return;
      width = box.width;
      // The delivered width belongs to the current layout, before paint.
      sample();
    });
    createEffect(() => {
      fontSelection("ui");
      fontSelection("mono");
      effectiveTextScale();
      transcriptLayoutRevision();
      scheduleSample();
    });
    const fontsReady = () => { scheduleSample(); republish(); };
    document.fonts?.addEventListener("loadingdone", fontsReady);
    const stopSettled = onShellLayoutSettled(fontsReady);
    const stopMutations = observeTranscriptRowMutations(root, (rows) => {
      for (const row of rows) virtualizer?.queueMeasurement(row);
    });
    const disclosure = (event: Event) => {
      const target = event.target;
      if (!(target instanceof Element)) return;
      const row = target.closest<HTMLElement>(".transcript-viewport-row[data-index]");
      if (row && root.contains(row)) virtualizer?.queueMeasurement(row);
    };
    root.addEventListener(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, disclosure);
    onCleanup(() => {
      disposed = true;
      stop();
      stopSettled();
      stopMutations();
      document.fonts?.removeEventListener("loadingdone", fontsReady);
      root.removeEventListener(TRANSCRIPT_DISCLOSURE_LAYOUT_EVENT, disclosure);
    });
  });
  createEffect(() => { key(); untrack(republish); });
  return {
    metrics,
    key,
    origin,
    measureOrigin,
    attach: (value: MeasuredTranscriptVirtualizer) => { virtualizer = value; },
    bindRow: (element: HTMLElement, index: () => number) => {
      const rowIndex = createMemo(index);
      createEffect(() => {
        const next = rowIndex();
        element.setAttribute("data-index", String(next));
        if (next >= 0) untrack(() => virtualizer?.queueMeasurement(element));
      });
      onCleanup(() => virtualizer?.forgetMeasurement(element));
    },
  };
}
