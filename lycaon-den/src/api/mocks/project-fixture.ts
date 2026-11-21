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
        admitted: true,
        admission_code: null,
      },
    ],
    outside_root_id: "0e5f1c2a-6b7d-4e8f-9a0b-1c2d3e4f5a6b",
    roots_generation: 0,
    session_count: 0,
    starred: false,
    is_draft: false,
    promotion: null,
    last_opened_at: ts,
    created_at: ts,
  };
}
