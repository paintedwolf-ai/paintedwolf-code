/** Cost settings describe estimates. */
export const COST_SETTINGS_COPY = {
  title: "Cost",
  intro:
    "Estimate token spend from public pricing feeds. Figures are estimates, not invoices.",
  mainLabel: "Track estimated cost",
  mainHint:
    "When off, Painted Wolf does not fetch pricing feeds and cost views stay quiet.",
  mainOffSourcesHint:
    "Tracking is off — the selected feed stays idle until tracking is enabled.",
  needsSource:
    "Select one pricing source before turning cost tracking on.",
  sourcesHeading: "Pricing sources",
  sourcesHint:
    "Choose one feed for catalog pricing. Live provider rates still take precedence when available.",
  refresh: "Refresh",
  refreshing: "Refreshing…",
  fetching: "Fetching rates…",
  freshnessPrefix: "Updated",
  loadError: "Could not load cost settings.",
  saveError: "Could not save cost settings.",
  statusHint: {
    ok: "Feed is current.",
    stale: "Cached rates may be outdated — refresh when online.",
    error: "Last refresh failed; prior cache is kept when present.",
    offline: "No rates loaded yet.",
  } as Record<string, string>,
};
