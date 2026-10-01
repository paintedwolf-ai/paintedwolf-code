export type WebResearchPrefsSnapshot = {
  warming: boolean;
  guess_domains: boolean;
  search_enabled: boolean;
  enabled_providers: string[];
};

export function prefsSnapshot(
  warming: boolean,
  guessDomains: boolean,
  searchEnabled: boolean,
  enabledIds: ReadonlySet<string>,
): WebResearchPrefsSnapshot {
  return {
    warming,
    guess_domains: guessDomains,
    search_enabled: searchEnabled,
    enabled_providers: [...enabledIds].sort(),
  };
}
