import type { Project } from "../types.ts";

export function wireProject(
  path: string,
  id = "proj-1",
  name = "demo",
): Project {
  const ts = "2025-01-01T00:00:00Z";
  return {
    id,
    name,
    roots: [
      {
        id: `${id}-root`,
        path,
        label: path.split(/[/\\]/).filter(Boolean).pop() ?? "root",
        is_primary: true,
        added_at: ts,
        kind: "attached",
      },
    ],
    roots_generation: 0,
    session_count: 0,
    starred: false,
    is_draft: false,
    promotion: null,
    last_opened_at: ts,
    created_at: ts,
  };
}
