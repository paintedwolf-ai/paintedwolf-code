/** Shared identity for untitled sessions across navigation and review surfaces. */
export function sessionTitle(title: string | null | undefined): string {
  return title?.trim() || "Untitled chat";
}
