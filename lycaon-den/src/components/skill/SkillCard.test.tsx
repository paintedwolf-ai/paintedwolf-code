import { afterEach, describe, expect, it } from "vitest";
import { render, fireEvent } from "@solidjs/testing-library";
import { SkillCard } from "./SkillCard.tsx";
import { ToolPartCard } from "../tool/ToolPartCard.tsx";
import type { SkillActivation } from "../../api/types.ts";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { clearTranscriptEntryMemory } from "../../chat/transcript/presentation/transcript-entry.ts";

const activation = (over: Partial<SkillActivation> = {}): SkillActivation => ({
  name: "verify-a-change",
  description: "Run the scoped checks before handoff.",
  instructions: "# Verify\n\nRun the scoped checks.\n",
  dir: "/abs/skills/verify-a-change",
  resources: ["references/FORMAT.md"],
  resources_omitted: 0,
  ...over,
});

const part = (over: Partial<ToolPartView> = {}): ToolPartView => ({
  id: "a1:tc-skill",
  toolCallId: "tc-skill",
  assistantMessageId: "assistant-message",
  messageId: "m1",
  tool: "skills_read",
  kind: "skill",
  status: "completed",
  args: { need: "verify-a-change" },
  output: "# Verify\n\nSkill directory: /abs/skills/verify-a-change\n",
  error: null,
  skill: activation(),
  ...over,
});

afterEach(() => {
  clearTranscriptEntryMemory();
});

describe("SkillCard chicklet", () => {
  it("is the shared tool chicklet — same details shell, dot, and status data", () => {
    const { container } = render(() => (
      <SkillCard part={part()} layout="chat" sessionId="s1" />
    ));
    const details = container.querySelector("details") as HTMLDetailsElement;
    expect(details).toBeTruthy();
    expect(details.classList.contains("den-tool-part")).toBe(true);
    expect(details.classList.contains("den-tool-part-card")).toBe(true);
    expect(details.dataset.status).toBe("done");
    expect(details.open).toBe(false);
    expect(container.querySelector(".den-tool-part-status-dot")).toBeTruthy();
    expect(container.querySelector(".den-tool-part-chicklet")).toBeTruthy();
  });

  it("names the family, not the tool id, and titles with the skill", () => {
    const { container } = render(() => (
      <SkillCard part={part()} layout="chat" sessionId="s1" />
    ));
    expect(
      container.querySelector(".den-tool-part-name")?.textContent,
    ).toBe("Skill");
    expect(
      container.querySelector(".den-tool-part-title")?.textContent,
    ).toBe("verify-a-change");
  });

  it("titles from the host projection when the args snapshot disagrees", () => {
    const { container } = render(() => (
      <SkillCard
        part={part({ args: {}, title: undefined })}
        layout="chat"
        sessionId="s1"
      />
    ));
    expect(
      container.querySelector(".den-tool-part-title")?.textContent,
    ).toBe("verify-a-change");
  });

  it("distinguishes a procedure read from its skill overview", () => {
    const { container, getByTestId, getByRole, queryByTestId } = render(() => (
      <SkillCard
        part={part({ args: { need: "verify-a-change", resource: "references/build_a_container_image.md" } })}
        layout="chat"
        sessionId="s1"
      />
    ));
    expect(container.querySelector(".den-tool-part-title")?.textContent).toBe(
      "verify-a-change · build a container image",
    );
    fireEvent.click(container.querySelector("summary")!);
    expect(getByTestId("skill-card-resource").textContent).toBe(
      "references/build_a_container_image.md",
    );
    expect(queryByTestId("skill-card-description")).toBeNull();
    expect(queryByTestId("skill-card-dir")).toBeNull();
    expect(getByRole("button", { name: "Resource content in Files" })).toBeTruthy();
    expect(getByTestId("skill-card-body").textContent).not.toContain("Skill files");
  });
});

describe("SkillCard body", () => {
  it("renders the skill's details from the host projection", () => {
    const { getByTestId, container, getByRole } = render(() => (
      <SkillCard part={part()} layout="chat" sessionId="s1" />
    ));
    fireEvent.click(container.querySelector("summary")!);
    expect(getByTestId("skill-card-name").textContent).toBe("verify-a-change");
    expect(getByTestId("skill-card-description").textContent).toBe(
      "Run the scoped checks before handoff.",
    );
    expect(getByTestId("skill-card-dir").textContent).toContain(
      "/abs/skills/verify-a-change",
    );
    expect(getByRole("button", {name:"Skill files in Files"})).toBeTruthy();
  });

  it("links to instructions without mounting their body in chat", () => {
    const { getByTestId, container, getByRole } = render(() => (
      <SkillCard part={part()} layout="chat" sessionId="s1" />
    ));
    fireEvent.click(container.querySelector("summary")!);
    const body = getByTestId("skill-card-body");
    expect(getByRole("button", {name:"Instructions in Files"})).toBeTruthy();
    expect(body.querySelector("h1")).toBeNull();
    // The trailer is the model's envelope; the card composes its own rows.
    expect(body.textContent).not.toContain("Skill directory:");
  });

  it("counts resources the activation had to omit", () => {
    const { getByTestId, container } = render(() => (
      <SkillCard
        part={part({ skill: activation({ resources_omitted: 3 }) })}
        layout="chat"
        sessionId="s1"
      />
    ));
    fireEvent.click(container.querySelector("summary")!);
    expect(getByTestId("skill-card-body").textContent).toContain(
      "+3 more not listed",
    );
  });

  it("says declared tools are not enforced", () => {
    const { getByTestId, container } = render(() => (
      <SkillCard
        part={part({ skill: activation({ allowed_tools: "read, grep" }) })}
        layout="chat"
        sessionId="s1"
      />
    ));
    fireEvent.click(container.querySelector("summary")!);
    expect(getByTestId("skill-card-allowed-tools").textContent).toContain(
      "not enforced",
    );
  });

  it("falls back to the generic body while the activation is in flight", () => {
    const { container, queryByTestId } = render(() => (
      <SkillCard
        part={part({ status: "running", output: null, skill: null })}
        layout="chat"
        sessionId="s1"
      />
    ));
    expect(queryByTestId("skill-card-body")).toBeNull();
    // The chicklet still reads as a skill rather than going blank.
    expect(
      container.querySelector(".den-tool-part-name")?.textContent,
    ).toBe("Skill");
    expect(
      (container.querySelector("details") as HTMLDetailsElement).dataset.status,
    ).toBe("running");
  });

  it("names a resource while its read is still in flight", () => {
    const { container } = render(() => (
      <SkillCard
        part={part({ status: "running", output: null, skill: null,
          args: { need: "verify-a-change", resource: "references/build_a_container_image.md" } })}
        layout="chat"
        sessionId="s1"
      />
    ));
    expect(container.querySelector(".den-tool-part-title")?.textContent).toBe(
      "verify-a-change · build a container image",
    );
  });

  it("falls back to the generic body when no skill resolved", () => {
    const { queryByTestId } = render(() => (
      <SkillCard
        part={part({ status: "error", skill: null, error: "SKILL_UNKNOWN" })}
        layout="chat"
        sessionId="s1"
      />
    ));
    expect(queryByTestId("skill-card-body")).toBeNull();
  });
});

// Tool cards share one chicklet structure.
describe("chicklet parity with the generic tool card", () => {
  const chickletShape = (root: HTMLElement) => {
    const summary = root.querySelector("summary") as HTMLElement;
    return {
      detailsClass: (root.querySelector("details") as HTMLElement).className,
      summaryHtml: summary.innerHTML
        .replace(/verify-a-change/g, "TITLE")
        .replace(/main\.go/g, "TITLE")
        .replace(/>Skill</g, ">NAME<")
        .replace(/>Read</g, ">NAME<"),
    };
  };

  it("renders the same header markup as a generic tool row", () => {
    const skill = render(() => (
      <SkillCard part={part()} layout="chat" sessionId="s1" />
    ));
    const generic = render(() => (
      <ToolPartCard
        part={part({
          id: "a1:tc-read",
          toolCallId: "tc-read",
          assistantMessageId: "assistant-message",
          tool: "read",
          kind: "read",
          args: { path: "main.go" },
          title: "main.go",
          skill: null,
        })}
        layout="chat"
        sessionId="s1"
      />
    ));
    expect(chickletShape(skill.container)).toEqual(
      chickletShape(generic.container),
    );
  });
});

describe("ToolPartCard routing", () => {
  it("routes a skill-family part to the skill card", () => {
    const { getByTestId, container } = render(() => (
      <ToolPartCard part={part()} layout="chat" sessionId="s1" />
    ));
    expect(getByTestId("skill-part-card")).toBeTruthy();
    fireEvent.click(container.querySelector("summary")!);
    expect(getByTestId("skill-card-body")).toBeTruthy();
  });

  it("leaves other tools on the generic card", () => {
    const { queryByTestId, getByTestId } = render(() => (
      <ToolPartCard
        part={part({ tool: "read", kind: "read", skill: null })}
        layout="chat"
        sessionId="s1"
      />
    ));
    expect(queryByTestId("skill-part-card")).toBeNull();
    expect(getByTestId("tool-part-card")).toBeTruthy();
  });
});
