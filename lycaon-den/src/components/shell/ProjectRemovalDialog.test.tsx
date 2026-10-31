import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";
import type { ProjectRemovalAssessment } from "../../api/types.ts";
import { ProjectRemovalDialog, createProjectRemovalReview } from "./ProjectRemovalDialog.tsx";

const assessment: ProjectRemovalAssessment = {
  project_id:"p",assessment_token:"review",extension_revision:"device",complete:false,
  checks:[{project_id:"other",name:"Other project",state:"unknown",revision:"",reason:"manifest_unreadable"}],
  extensions:[
    {pack_id:"acme/eligible",disposition:"eligible",reasons:[]},
    {pack_id:"acme/unknown",disposition:"unknown",reasons:[{code:"incomplete_project_evidence",subject_id:""}]},
    {pack_id:"acme/shared",disposition:"retained",reasons:[{code:"extension_dependency",subject_id:"acme/parent"}]},
  ],
};
function setup(value: ProjectRemovalAssessment | null) {
  let review!: ReturnType<typeof createProjectRemovalReview>;
  const view=render(()=>{review=createProjectRemovalReview();return <ProjectRemovalDialog review={review}/>;});
  const result=review.show(value);
  return {...view,review,result};
}
describe("project removal review",()=>{
  it("defaults to keeping extensions and only offers eligible checkboxes",async()=>{
    const f=setup(assessment);
    const available=screen.getByRole("checkbox",{name:"acme/eligible"}) as HTMLInputElement;
    expect(available.checked).toBe(false);
    expect((screen.getByRole("checkbox",{name:"acme/unknown"}) as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByRole("checkbox",{name:"acme/shared"}) as HTMLInputElement).disabled).toBe(true);
    expect(screen.getByText(/Other project: Extension suggestions could not be read/)).toBeTruthy();
    fireEvent.click(available);
    fireEvent.click(screen.getByRole("button",{name:"Delete project and remove 1 extension"}));
    await expect(f.result).resolves.toEqual(["acme/eligible"]);
  });
  it("keeps every extension when assessment fails",async()=>{
    const f=setup(null);
    expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button",{name:"Delete project and keep extensions"}));
    await expect(f.result).resolves.toEqual([]);
  });
  it("cancels without authorizing deletion",async()=>{
    const f=setup(assessment);fireEvent.click(screen.getByRole("button",{name:"Cancel"}));await expect(f.result).resolves.toBeNull();
  });
  it("confirms project deletion when there are no extensions", async () => {
    const f = setup({ ...assessment, extensions: [] });
    expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Delete project" }));
    await expect(f.result).resolves.toEqual([]);
  });
  it("settles pending review on disposal",async()=>{
    const f=setup(assessment);const next=f.review.show(null);f.unmount();await expect(f.result).resolves.toBeNull();await expect(next).resolves.toBeNull();
  });
  it("lists every running background command in the project and offers Open Running", async () => {
    let openedRunning = 0;
    let review!: ReturnType<typeof createProjectRemovalReview>;
    render(() => {
      review = createProjectRemovalReview();
      return <ProjectRemovalDialog review={review} />;
    });
    const result = review.show(
      {
        ...assessment,
        extensions: [],
        background_processes: [
          {
            process_id: "bg_1",
            project_id: "p",
            session_id: "s-1",
            session_title: "Dev server",
            session_archived: false,
            command: "python3 -m http.server 8000",
            working_dir: ".",
            started_at: "2026-10-30T12:00:00Z",
            running: true,
            ports: ["127.0.0.1:8000"],
          },
          {
            process_id: "bg_2",
            project_id: "p",
            session_id: "s-2",
            session_title: "Archived chat",
            session_archived: true,
            command: "npm run watch",
            working_dir: ".",
            started_at: "2026-10-30T12:01:00Z",
            running: true,
            ports: [],
          },
        ],
      },
      "Alpha",
      () => {
        openedRunning += 1;
      },
    );
    const section = screen.getByTestId("project-removal-background-processes");
    expect(section.textContent).toContain(
      "2 background commands are running in this project and will stop when the project is deleted:",
    );
    expect(section.textContent).toContain("python3 -m http.server 8000 (127.0.0.1:8000) — Dev server");
    expect(section.textContent).toContain("npm run watch — Archived chat (archived)");
    fireEvent.click(screen.getByTestId("project-removal-open-running"));
    expect(openedRunning).toBe(1);
    await expect(result).resolves.toBeNull();
  });
});
