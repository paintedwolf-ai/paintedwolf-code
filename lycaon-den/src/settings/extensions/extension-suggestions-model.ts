import type {
  ExtensionSuggestion,
  ExtensionSuggestionsResponse,
} from "../../api/types.ts";

export function installableExtensionSuggestions(
  response: ExtensionSuggestionsResponse | null | undefined,
): ExtensionSuggestion[] {
  return (response?.suggestions ?? []).filter(
    (suggestion) =>
      suggestion.status === "available" || suggestion.status === "version_mismatch",
  );
}

export function hasInstallableExtensionSuggestions(
  response: ExtensionSuggestionsResponse | null | undefined,
): boolean {
  return installableExtensionSuggestions(response).length > 0;
}

export function extensionSuggestionDetail(suggestion: ExtensionSuggestion): string {
  if (suggestion.status === "version_mismatch" && suggestion.contributes) {
    return suggestion.contributes;
  }
  const target = suggestion.ref || suggestion.version;
  return [suggestion.source, target].filter(Boolean).join(" @ ");
}
