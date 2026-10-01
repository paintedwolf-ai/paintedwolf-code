import { describe, expect, it } from "vitest";
import {
  isSkillToolPart,
  skillActivationFromPart,
  skillOriginLabel,
  skillResourceListing,
} from "./skill-card-model.ts";
import { classifyToolKind } from "../tool/tool-part-model.ts";
import type { SkillActivation } from "../../api/types.ts";
import type { ToolPartView } from "../tool/tool-part-model.ts";

const activation = (over: Partial<SkillActivation> = {}): SkillActivation => ({
  name: "verify-a-change",
  instructions: "body",
  ...over,
});

const part = (over: Partial<ToolPartView> = {}): ToolPartView => ({
  id: "a1:tc",
  toolCallId: "tc",
  assistantMessageId: "assistant-message",
  messageId: "m1",
  tool: "skills_read",
  kind: "skill",
  status: "completed",
  ...over,
});

describe("skill routing", () => {
  // Routing is the catalog's rendering family, so a renamed or added skill tool
  // reaches the card without a frontend list edit.
  it("comes from the tool catalog", () => {
    expect(classifyToolKind("skills_read")).toBe("skill");
    expect(isSkillToolPart(part())).toBe(true);
    expect(isSkillToolPart(part({ kind: "read", tool: "read" }))).toBe(false);
  });
});

describe("skillActivationFromPart", () => {
  it("returns the host projection", () => {
    expect(skillActivationFromPart(part({ skill: activation() }))?.name).toBe(
      "verify-a-change",
    );
  });

  it("treats an absent or unnamed projection as nothing to render", () => {
    expect(skillActivationFromPart(part())).toBeNull();
    expect(skillActivationFromPart(part({ skill: null }))).toBeNull();
    expect(
      skillActivationFromPart(part({ skill: activation({ name: "  " }) })),
    ).toBeNull();
  });
});

describe("skillResourceListing", () => {
  it("drops blank entries and reports the omitted count", () => {
    expect(
      skillResourceListing(
        activation({ resources: ["a.md", "  ", "b.md"], resources_omitted: 2 }),
      ),
    ).toEqual({ files: ["a.md", "b.md"], omitted: 2 });
  });

  it("is absent when the skill bundles nothing", () => {
    expect(skillResourceListing(activation())).toBeNull();
    expect(skillResourceListing(activation({ resources: [] }))).toBeNull();
  });

  // A skill whose whole listing was cut still has to say so.
  it("survives an all-omitted listing", () => {
    expect(
      skillResourceListing(activation({ resources: [], resources_omitted: 9 })),
    ).toEqual({ files: [], omitted: 9 });
  });
});

describe("skillOriginLabel", () => {
  it("distinguishes project, pack, and host skills", () => {
    expect(skillOriginLabel(activation({ project: true }))).toBe("Project");
    expect(skillOriginLabel(activation({ pack_id: "painted-wolf/platform" }))).toBe(
      "Pack · painted-wolf/platform",
    );
    expect(skillOriginLabel(activation())).toBe("Host");
  });

  // Project provenance outranks the pack a skill was shadowed from.
  it("prefers project over pack", () => {
    expect(
      skillOriginLabel(activation({ project: true, pack_id: "p" })),
    ).toBe("Project");
  });
});
