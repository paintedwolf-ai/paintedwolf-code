import {
  windowEdgeChrome,
  type WindowChromeSide,
  type WindowEdgeChrome,
} from "../../platform/windows/window-chrome-layout.ts";

/** A surface that can sit against a window edge, under that edge's controls. */
export type WindowEdgeHost = "nav" | "stage" | "chat" | "drawer";

/** Which surface holds one window edge now, and once the narrow band drops a split column. */
export type WindowEdgeHosts = { owner: WindowEdgeHost; narrow: WindowEdgeHost };

/** The shell layout facts that decide which surface sits against each window edge. */
export type WindowEdgeLayout = {
  /** The side the navigation sidebar is on, while it shows. */
  navSide: WindowChromeSide | null;
  /** Stage and conversation share the window. */
  split: boolean;
  /** The stage column holds content; otherwise a lone column is the conversation. */
  stageOccupied: boolean;
  /** The split's stage is hidden. */
  contextHidden: boolean;
  /** The split's conversation is hidden. */
  conversationHidden: boolean;
  stageSide: WindowChromeSide;
  /** The column the stylesheet keeps below the narrow band. */
  narrowSurvivor: "stage" | "chat";
  /** The side of its column an open session drawer covers. */
  drawerSide: WindowChromeSide | null;
};

/** One surface's hold on a window edge that carries controls. */
export type WindowEdgeClaim = {
  side: WindowChromeSide;
  chrome: WindowEdgeChrome;
  /** This surface sits against the edge. */
  owner: boolean;
  /** This surface keeps the edge once the narrow band drops its neighbor. */
  narrow: boolean;
};

function columnHosts(edge: WindowChromeSide, layout: WindowEdgeLayout): WindowEdgeHosts {
  if (layout.navSide === edge) return { owner: "nav", narrow: "nav" };
  if (!layout.split) {
    const host = layout.stageOccupied ? "stage" : "chat";
    return { owner: host, narrow: host };
  }
  if (layout.contextHidden) return { owner: "chat", narrow: "chat" };
  if (layout.conversationHidden) return { owner: "stage", narrow: "stage" };
  return {
    owner: layout.stageSide === edge ? "stage" : "chat",
    narrow: layout.narrowSurvivor,
  };
}

/** The column an open session drawer covers: the conversation while it shows, otherwise the stage. */
function drawerColumn(layout: WindowEdgeLayout): "stage" | "chat" {
  if (!layout.split) return layout.stageOccupied ? "stage" : "chat";
  return layout.conversationHidden ? "stage" : "chat";
}

/** The surface against one window edge. An open drawer takes the edge its column held on that side. */
export function windowEdgeHosts(edge: WindowChromeSide, layout: WindowEdgeLayout): WindowEdgeHosts {
  const columns = columnHosts(edge, layout);
  if (layout.drawerSide !== edge) return columns;
  return {
    owner: columns.owner === drawerColumn(layout) ? "drawer" : columns.owner,
    narrow: columns.narrow === "nav" ? "nav" : "drawer",
  };
}

/** One surface's claims on the window edges that carry controls. */
export function windowEdgeClaims(
  host: WindowEdgeHost,
  layout: WindowEdgeLayout,
): WindowEdgeClaim[] {
  const claims: WindowEdgeClaim[] = [];
  for (const side of ["left", "right"] as const) {
    const chrome = windowEdgeChrome(side);
    if (!chrome) continue;
    const hosts = windowEdgeHosts(side, layout);
    const owner = hosts.owner === host;
    const narrow = hosts.narrow === host;
    if (owner || narrow) claims.push({ side, chrome, owner, narrow });
  }
  return claims;
}

/** A claim the surface holds only below, or only above, the narrow band. */
export function windowEdgeBandAttributes(claim: Pick<WindowEdgeClaim, "owner" | "narrow">) {
  return {
    "data-narrow-only": !claim.owner && claim.narrow ? "" : undefined,
    "data-narrow-hidden": claim.owner && !claim.narrow ? "" : undefined,
  };
}

/** Marks a surface with the window edge it holds, for the rules that clear that edge's controls. */
export function windowEdgeAttributes(claim: WindowEdgeClaim | undefined) {
  if (!claim) return {};
  return {
    "data-window-edge": claim.side,
    "data-window-edge-chrome": claim.chrome,
    ...windowEdgeBandAttributes(claim),
  };
}

/** Marks a surface that sits under the OS's own window buttons, which take no room in its layout. */
export function nativeWindowEdgeAttributes(claims: readonly WindowEdgeClaim[]) {
  return windowEdgeAttributes(claims.find((claim) => claim.chrome === "native"));
}
