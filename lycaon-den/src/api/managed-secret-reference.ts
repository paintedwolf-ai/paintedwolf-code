const REFERENCE_PATTERN = /^\{\{paintedwolf-secret:([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\}\}$/u;

export function managedSecretId(reference: string): string | null {
  return REFERENCE_PATTERN.exec(reference.trim())?.[1] ?? null;
}
