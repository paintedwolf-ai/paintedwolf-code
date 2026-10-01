/** Fixtures seed the shipped contribution frame. */
import { CONTEXT_NAV_CATALOG } from "../../shared/app-state-types.ts";
import { FOCUS_REGION_IDS } from "../shortcuts/focus-region.ts";
import { seedContributionFrameForTest } from "./contribution-store.ts";
import { setShellFactState, type ShellFactState } from "./shell-facts.ts";
import { STOCK_FRAME } from "./stock-frame.generated.ts";

/** The stock provider prefix every shipped contribution id carries. */
export const STOCK_PROVIDER = "painted-wolf/platform";

/** Contribution id for a stock unit name, e.g. `search-open`. */
export function stockId(name: string): string {
  return `${STOCK_PROVIDER}:${name}`;
}

/** Keybinding declaration id for a stock command stem. */
export function stockBindingId(name: string): string {
  return stockId(`key-${name}`);
}

/** Every gate holds — for cases about some rule other than availability. */
export const ALL_SHELL_FACTS: ShellFactState = {
  activeView: "chat",
  mountedRegions: [...FOCUS_REGION_IDS],
  workspaceKind: "main",
  isPeerWorkspace: true,
  contextFeatures: [...CONTEXT_NAV_CATALOG],
  composerFocused: true,
  chatNavigable: true,
  contextNavigable: true,
  sidebarExpanded: true,
  contextCollapsed: true,
  conversationCollapsed: true,
  splitLive: true,
  filesStageActive: true,
  filesTreeCollapsed: true,
  fileSummariesOn: true,
  fileSummariesKnown: true,
  filesVersionHistorical: true,
  peerOpenAvailable: true,
  peerViewsPresent: true,
  projectOpen: true,
  sessionExists: true,
  sessionIdle: true,
  activityLive: true,
  editor: {
    active: true,
    editable: true,
    hasSelection: true,
    symbol: "symbol",
    findingId: "finding",
    language: "typescript",
  },
  requirementReady: () => true,
  configurationOn: () => true,
};

/** Seed the shipped frame and, unless overridden, make every gate hold. */
export function seedStockFrame(facts?: Partial<ShellFactState>): void {
  seedContributionFrameForTest(STOCK_FRAME);
  setShellFactState({ ...ALL_SHELL_FACTS, ...facts });
}
