import {
  catalogEntry,
  type WebResearchProviderId,
} from "../web-research-catalog.generated.ts";

export type WebResearchSettingsTab = "providers" | "index";

export const WEB_RESEARCH_SETTINGS_COPY = {
  title: "Web research",
  tabProviders: "Providers",
  tabIndex: "Local index",
  tabsAriaLabel: "Web research sections",
  enableLabel: "Allow web research",
  intro: "Outbound web search, pages, and the research worker.",
  providersIntro: "",
  disabledNote: "Off — outbound web tools stay unavailable.",
  builtinGroupLabel: "Built in",
  directHint:
    "Local index plus built-in publisher and vertical sources. Works without keys; optional source credentials raise limits and unlock authenticated search.",
  directSeedsNote: "",
  domainGuessLabel: "Guess domains",
  domainGuessHint:
    "When results are thin, guess domains to fill the index.",
  accessHeading: "Access",
  indexHeading: "Local web index",
  indexIntro:
    "Searches fill a local cache so repeat topics stay fast — even when providers are down.",
  warmingLabel: "Warming",
  warmingHint:
    "Pre-fills and expands the index from session titles and web activity — only while the app is open and in use.",
  indexStorageLabel: "Storage",
  indexStorageEmpty: "No pages indexed yet.",
  indexActivityHeading: "Recent activity",
  indexActivityAriaLabel: "Recent web index activity",
  indexActivityEmpty: "No background activity yet.",
  clearIndex: "Clear",
  clearingIndex: "Clearing…",
  clearIndexConfirm:
    "Clear all pages and warming history from your local web index? Later searches will rebuild it from scratch.",
  clearIndexError: "Could not clear the local web index.",
  indexHealthLabel: "Engine",
  indexUnavailable: "Index unavailable in this build.",
  indexStatsSummary: (args: {
    pages: number;
    hosts: number;
    megabytes: number;
  }) => `${args.pages} pages · ${args.hosts} hosts · ${args.megabytes} MB`,
  indexStatsDetail: (args: {
    verified: number;
    warmed: number;
    warmHits: number;
  }) =>
    `${args.verified} verified · ${args.warmed} warmed · ${args.warmHits} warm hits`,
  indexHealthSummary: (args: {
    writesApplied: number;
    writesDropped: number;
    lastSearchMs: number;
    maxSearchMs: number;
  }) => {
    const dropped =
      args.writesDropped > 0 ? ` (${args.writesDropped} dropped)` : "";
    return `${args.writesApplied} writes${dropped} · search ${args.lastSearchMs} ms (max ${args.maxSearchMs} ms)`;
  },
  directReady: "Ready",
  directNotEnabled: "Not enabled",
  directLabelFallback: "Direct search",
  statusReady: "Ready",
  statusNeedsKey: "Needs API key",
  statusNeedsConfig: "Needs configuration",
  statusNeedsEndpoint: "Needs endpoint",
  kindKeyed: "API key providers",
  kindKeyedExtra: "API key + extra fields",
  kindKeylessEndpoint: "Endpoint providers",
  kindKeyless: "No key needed",
  privateEndpointWarning:
    "This provider may use a LAN or private address. Do not point it at cloud metadata (169.254.169.254), loopback, or other hosts on this machine.",
  providerLabel: (id: WebResearchProviderId) => catalogEntry(id).label,
  providerHint: (id: WebResearchProviderId) => catalogEntry(id).hint,
  apiKeyPlaceholder: "Paste API key",
  /** Bullet mask — makes a stored key read as filled without revealing it. */
  apiKeyPlaceholderStored: "••••••••••••••••",
  apiKeyStoredStatus: "Key saved",
  apiKeyMissingStatus: "No key saved",
  apiKeyEnvironmentStatus: "Environment key",
  apiKeyOptionalStatus: "Optional",
  endpointPlaceholder: "https://…",
  save: "Save key",
  clear: "Clear key",
  saveConfig: "Save settings",
  remove: "Remove",
  addProviderHeading: "Add provider",
  addProvider: "Add provider",
  filterProviders: "Filter providers…",
  noFilterMatches: "No providers match.",
  cancel: "Cancel",
  providersBack: "Providers",
  chooseProvider: "No providers yet — add one to enable web research.",
  addGroupRequiresApiKey: "Requires API key",
  addGroupEndpoint: "Self-hosted endpoint",
  addGroupNoKey: "No key needed",
  testSearch: "Test search",
  testing: "Testing…",
  saveError: "Could not save API key.",
  clearError: "Could not clear API key.",
  configError: "Could not save provider settings.",
  prefsError: "Could not save web research settings.",
  loadError: "Could not load web research settings.",
  testError: "Test search failed.",
  envKeyHint:
    "Using an API key from your environment. Save a key below to store it in app settings.",
} as const;
