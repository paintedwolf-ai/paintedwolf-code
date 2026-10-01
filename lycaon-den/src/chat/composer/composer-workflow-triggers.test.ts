import { describe, expect, it } from "vitest";
import type { WorkflowSummary } from "../../api/types.ts";
import {
  filterWorkflowSlashSuggestions,
  workflowSlashSuggestionsFromCatalog,
} from "./composer-workflow-triggers.ts";

const catalog: WorkflowSummary[] = [
  { id: "plan", version: "1.0.0", name: "Plan", trigger: "/plan" },
  { id: "options", version: "1.0.0", name: "Design options", trigger: "/options" },
  { id: "recon-pack", version: "1.0.0", name: "Recon", trigger: "/recon" },
  { id: "security-survey", version: "1.0.0", name: "Security", trigger: "/security-survey" },
  { id: "bugbash", version: "1.0.0", name: "Bugbash", trigger: "/bugbash" },
];

describe("composer-workflow-triggers", () => {
  it("builds structured slash suggestions and filters by prefix", () => {
    const rows = workflowSlashSuggestionsFromCatalog(catalog);
    expect(rows.find((r) => r.trigger === "/options")?.label).toBe("Design options");
    expect(rows.find((r) => r.trigger === "/plan")?.workflow_id).toBe("plan");
    expect(rows.find((r) => r.trigger === "/plan")?.preset_id).toBeUndefined();
    expect(filterWorkflowSlashSuggestions("/opt", catalog).map((r) => r.trigger)).toEqual([
      "/options",
    ]);
  });
});
