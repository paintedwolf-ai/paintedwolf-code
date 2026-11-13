import { createEffect, createRoot, createSignal, onCleanup, type Accessor } from "solid-js";
import type { LycaonClient } from "../api/client.ts";
import type { ReplaceProjectViewRequest } from "../api/types.ts";
import { getLycaonClient } from "../platform/connection/app-connection.ts";
import { clientIdentity } from "../platform/connection/client-identity.ts";
import { useResidentPresence } from "../ui/resident-presence-context.tsx";

/** What one stage surface shows of its project. */
export type ViewFacet = Partial<Pick<ReplaceProjectViewRequest, "editor" | "diff" | "folder" | "search" | "findings">>;

type Offer = { projectId: Accessor<string | undefined>; facet: Accessor<ViewFacet | null> };

/** Renewal stays well inside the host's one-minute expiry. */
export const VIEW_RENEW_MS = 25_000;
const SEND_DELAY_MS = 150;

const [offers, setOffers] = createSignal<readonly Offer[]>([]);
const [stageShown, setStageShownSignal] = createSignal(false);

/** The window shows its stage column; a hidden split pane hides every stage surface. */
export function setStageShown(shown: boolean): void {
  setStageShownSignal(shown);
}

/** Offers a stage surface's view while that surface is the one presented. */
export function offerViewFacet(projectId: Accessor<string | undefined>, facet: Accessor<ViewFacet | null>): void {
  const presence = useResidentPresence();
  const offer: Offer = { projectId, facet: () => (presence() === "active" && stageShown() ? facet() : null) };
  setOffers((all) => [...all, offer]);
  onCleanup(() => setOffers((all) => all.filter((entry) => entry !== offer)));
}

/** Each project's merged view; later offers fill only what earlier ones left out. */
export function composeViews(entries: readonly Offer[]): Map<string, ViewFacet> {
  const views = new Map<string, ViewFacet>();
  for (const entry of entries) {
    const projectId = entry.projectId()?.trim();
    const facet = entry.facet();
    if (!projectId || !facet) continue;
    const view = views.get(projectId) ?? {};
    for (const key of ["editor", "diff", "folder", "search", "findings"] as const) {
      const value = facet[key];
      if (value === undefined || view[key] !== undefined) continue;
      if (Array.isArray(value) && value.length === 0) continue;
      Object.assign(view, { [key]: value });
    }
    views.set(projectId, view);
  }
  return views;
}

function windowVisibility(): { visible: Accessor<boolean>; focused: Accessor<boolean> } {
  const [visible, setVisible] = createSignal(document.visibilityState === "visible");
  const [focused, setFocused] = createSignal(document.hasFocus());
  const onVisibility = () => setVisible(document.visibilityState === "visible");
  const onFocus = () => setFocused(true);
  const onBlur = () => setFocused(false);
  document.addEventListener("visibilitychange", onVisibility);
  window.addEventListener("focus", onFocus);
  window.addEventListener("blur", onBlur);
  onCleanup(() => {
    document.removeEventListener("visibilitychange", onVisibility);
    window.removeEventListener("focus", onFocus);
    window.removeEventListener("blur", onBlur);
  });
  return { visible, focused };
}

/**
 * Reports what this window shows of each project, renewing while it holds and
 * withdrawing a project once nothing of it is in view. Returns a stop function.
 */
export function startViewReporter(client: Accessor<LycaonClient | null> = () => getLycaonClient() ?? null): () => void {
  return createRoot((dispose) => {
    const { visible, focused } = windowVisibility();
    const sent = new Map<string, string>();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let closing = false;
    const send = (renew: boolean) => {
      const connected = client();
      if (!connected) return;
      const views = visible() && !closing ? composeViews(offers()) : new Map<string, ViewFacet>();
      for (const projectId of new Set([...sent.keys(), ...views.keys()])) {
        const view = views.get(projectId) ?? {};
        const report: ReplaceProjectViewRequest = { client_id: clientIdentity(), focused: focused(), ...view };
        const signature = JSON.stringify(report);
        if (Object.keys(view).length === 0) {
          if (!sent.has(projectId)) continue;
          sent.delete(projectId);
        } else {
          if (sent.get(projectId) === signature && !renew) continue;
          sent.set(projectId, signature);
        }
        void connected.replaceProjectView(projectId, report).catch(() => {
          // The next change or renewal retries.
          sent.delete(projectId);
        });
      }
    };
    const onPageHide = () => {
      closing = true;
      send(false);
    };
    window.addEventListener("pagehide", onPageHide);
    createEffect(() => {
      visible();
      focused();
      for (const entry of offers()) {
        entry.projectId();
        entry.facet();
      }
      clearTimeout(timer);
      timer = setTimeout(() => send(false), SEND_DELAY_MS);
    });
    const renewal = setInterval(() => send(true), VIEW_RENEW_MS);
    onCleanup(() => {
      clearTimeout(timer);
      clearInterval(renewal);
      window.removeEventListener("pagehide", onPageHide);
    });
    return dispose;
  });
}
