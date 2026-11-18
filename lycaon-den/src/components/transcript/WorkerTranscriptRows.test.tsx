import { render, screen } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";
import { WorkerTranscriptMessageRow } from "./WorkerTranscriptRows.tsx";

const envelope = `<task job_id="job-1" child_session_id="child-1" state="complete">
  <summary>Short</summary>
  <task_result>## Done

- item one
- item two</task_result>
</task>`;

describe("WorkerTranscriptMessageRow", () => {
  it.each(["user", "assistant"] as const)("renders a completion envelope as its %s-row report, not as markup", (role) => {
    render(() => <WorkerTranscriptMessageRow role={role} content={envelope} />);
    const article = screen.getByTestId(`transcript-article-${role}`);

    expect(article.querySelector("h2")?.textContent).toBe("Done");
    expect([...article.querySelectorAll("li")].map((item) => item.textContent)).toEqual(["item one", "item two"]);
    expect(article.textContent).not.toContain("job_id");
    expect(article.textContent).not.toContain("task_result");
  });
});
