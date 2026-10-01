export function formatSentenceCase(value: string): string {
  const normalized = value.trim().replaceAll("_", " ");
  if (!normalized) return "";
  return normalized.charAt(0).toUpperCase() + normalized.slice(1);
}
