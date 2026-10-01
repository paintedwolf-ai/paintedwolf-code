import { perfMark } from "../chat/stream/den-main-thread-perf.ts";
import { activeThemePaintKey } from "../settings/appearance/appearance-prefs.ts";
import { recordProjectThumbnail } from "./thumbnail-store.ts";

const THUMB_WIDTH = 320;
const THUMB_HEIGHT = 180;

export type ProjectThumbnailModel = {
  projectName: string;
  sessionTitle: string;
  messageCount: number;
  stage: string;
};

type ThumbnailPalette = {
  background: string;
  panel: string;
  line: string;
  text: string;
  muted: string;
  accent: string;
};

function cssVar(css: CSSStyleDeclaration, name: string, fallback: string): string {
  return css.getPropertyValue(name).trim() || fallback;
}

function thumbnailPalette(): ThumbnailPalette {
  const css = getComputedStyle(document.documentElement);
  return {
    background: cssVar(css, "--den-background", "#11151c"),
    panel: cssVar(css, "--den-surface", "#181e28"),
    line: cssVar(css, "--den-line", "#303948"),
    text: cssVar(css, "--den-text", "#e9edf4"),
    muted: cssVar(css, "--den-text-muted", "#8993a4"),
    accent: cssVar(css, "--den-accent-signal", "#6f9df8"),
  };
}

function escapeXml(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&apos;");
}

function truncate(value: string, max: number): string {
  const compact = value.trim().replace(/\s+/g, " ");
  if (compact.length <= max) return compact;
  return `${compact.slice(0, Math.max(0, max - 1))}…`;
}

/** Builds the fixed project-card preview from project state. */
export function renderProjectThumbnail(
  model: ProjectThumbnailModel,
  palette: ThumbnailPalette = thumbnailPalette(),
): string {
  const project = escapeXml(truncate(model.projectName || "Untitled project", 34));
  const session = escapeXml(truncate(model.sessionTitle || "New session", 42));
  const stage = escapeXml(truncate(model.stage || "Conversation", 20));
  const activity = `${Math.max(0, Math.round(model.messageCount))} messages`;
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${THUMB_WIDTH}" height="${THUMB_HEIGHT}" viewBox="0 0 ${THUMB_WIDTH} ${THUMB_HEIGHT}">
<defs><clipPath id="project-thumb-text"><rect x="108" y="20" width="188" height="70"/></clipPath></defs>
<rect width="320" height="180" rx="12" fill="${escapeXml(palette.background)}"/>
<rect x="10" y="10" width="74" height="160" rx="8" fill="${escapeXml(palette.panel)}" stroke="${escapeXml(palette.line)}"/>
<circle cx="24" cy="25" r="4" fill="${escapeXml(palette.accent)}"/><rect x="34" y="21" width="38" height="8" rx="4" fill="${escapeXml(palette.line)}"/>
<rect x="20" y="48" width="54" height="6" rx="3" fill="${escapeXml(palette.line)}"/><rect x="20" y="64" width="43" height="6" rx="3" fill="${escapeXml(palette.line)}"/><rect x="20" y="80" width="50" height="6" rx="3" fill="${escapeXml(palette.line)}"/>
<rect x="94" y="10" width="216" height="160" rx="8" fill="${escapeXml(palette.panel)}" stroke="${escapeXml(palette.line)}"/>
<g clip-path="url(#project-thumb-text)">
<text x="108" y="34" fill="${escapeXml(palette.text)}" font-family="system-ui,sans-serif" font-size="13" font-weight="600">${project}</text>
<text x="108" y="52" fill="${escapeXml(palette.muted)}" font-family="system-ui,sans-serif" font-size="10">${stage} · ${escapeXml(activity)}</text>
<text x="108" y="86" fill="${escapeXml(palette.muted)}" font-family="system-ui,sans-serif" font-size="10">${session}</text>
</g>
<rect x="108" y="68" width="188" height="1" fill="${escapeXml(palette.line)}"/>
<circle cx="115" cy="105" r="5" fill="${escapeXml(palette.accent)}" opacity=".8"/><rect x="128" y="99" width="118" height="7" rx="3.5" fill="${escapeXml(palette.line)}"/>
<rect x="128" y="113" width="156" height="6" rx="3" fill="${escapeXml(palette.line)}" opacity=".8"/><rect x="128" y="125" width="132" height="6" rx="3" fill="${escapeXml(palette.line)}" opacity=".65"/>
</svg>`;
  return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`;
}

/** Captures application state into the dedicated project-card preview. */
export function captureProjectThumbnail(
  projectId: string,
  model: ProjectThumbnailModel,
): void {
  const paintKey = activeThemePaintKey();
  const startedAt = performance.now?.() ?? Date.now();
  perfMark("thumbnail.capture:start", { kind: "semantic" });
  const dataUrl = renderProjectThumbnail(model);
  recordProjectThumbnail(projectId, dataUrl, paintKey);
  perfMark("thumbnail.capture:end", {
    wall_ms: Math.round((performance.now?.() ?? Date.now()) - startedAt),
  });
}
