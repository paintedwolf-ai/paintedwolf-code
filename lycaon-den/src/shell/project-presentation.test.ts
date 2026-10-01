import { describe, expect, it } from "vitest";
import {
  deriveProjectPresentation,
  retainMountedProjectPresentation,
  isHomePresentation,
  type ProjectPresentationFacts,
} from "./project-presentation.ts";
import type { StageScope } from "./stage-scope.ts";

const READY_SCOPE: StageScope = {
  project_id: "project-a",
  session_id: "session-a",
  phase: "ready",
};

const SWITCHING_SCOPE: StageScope = {
  project_id: "project-a",
  session_id: null,
  phase: "switching",
};

function readyFacts(): ProjectPresentationFacts {
  return {
    projectId: "project-a",
    projectsLoaded: true,
    recentsLoaded: true,
    contributionsReady: true,
    contentSettled: true,
    scope: READY_SCOPE,
  };
}

describe("project presentation", () => {
  it("opens the workspace once every data source is coherent", () => {
    expect(deriveProjectPresentation(readyFacts())).toEqual({
      projectId: "project-a",
      opening: false,
      ready: { identity: true, mounted: true, open: true },
    });
  });

  it("holds only the regions whose data sources are missing", () => {
    const cases: {
      patch: Partial<ProjectPresentationFacts>;
      holds: ("identity" | "mounted" | "open")[];
    }[] = [
      { patch: { projectsLoaded: false }, holds: ["identity"] },
      // Late content holds only the reveal.
      { patch: { contentSettled: false }, holds: ["open"] },
      { patch: { contributionsReady: false }, holds: ["mounted", "open"] },
      { patch: { recentsLoaded: false }, holds: ["mounted", "open"] },
      { patch: { scope: SWITCHING_SCOPE }, holds: ["mounted", "open"] },
    ];
    for (const { patch, holds } of cases) {
      const ready = deriveProjectPresentation({
        ...readyFacts(),
        ...patch,
      }).ready;
      expect({ patch, ready }).toEqual({
        patch,
        ready: {
          identity: !holds.includes("identity"),
          mounted: !holds.includes("mounted"),
          open: !holds.includes("open"),
        },
      });
    }
  });

  it("keeps the identity cluster painted while the workspace is still opening", () => {
    // Identity can settle before project content.
    const opening = deriveProjectPresentation({
      ...readyFacts(),
      contributionsReady: false,
      contentSettled: false,
      scope: SWITCHING_SCOPE,
    });
    expect(opening.ready.identity).toBe(true);
    expect(opening.ready.mounted).toBe(false);
    expect(opening.ready.open).toBe(false);
  });

  it("treats no project as Home without inventing project loading", () => {
    const home = deriveProjectPresentation({
      ...readyFacts(),
      projectId: null,
    });
    expect(home).toEqual({
      projectId: null,
      opening: false,
      ready: { identity: false, mounted: false, open: false },
    });
    expect(isHomePresentation(home)).toBe(true);
    expect(isHomePresentation(deriveProjectPresentation(readyFacts()))).toBe(
      false,
    );
  });

  it("treats a workspace ahead of its project id as neither Home nor a project", () => {
    const opening = deriveProjectPresentation({
      ...readyFacts(),
      projectId: null,
      scope: { project_id: null, session_id: null, phase: "opening" },
    });
    expect(opening).toEqual({
      projectId: null,
      opening: true,
      ready: { identity: false, mounted: false, open: false },
    });
    expect(isHomePresentation(opening)).toBe(false);

    // Dependents compare by identity, so Home and opening must not collapse.
    const home = deriveProjectPresentation({ ...readyFacts(), projectId: null });
    const homeScope: StageScope = { project_id: null, session_id: null, phase: "home" };
    expect(retainMountedProjectPresentation(home, opening, homeScope)).toBe(opening);
    expect(retainMountedProjectPresentation(opening, home, homeScope)).toBe(home);
  });

  it("opens a coherent project with no sessions", () => {
    expect(
      deriveProjectPresentation({
        ...readyFacts(),
        scope: {
          project_id: "project-a",
          session_id: null,
          phase: "project-empty",
        },
      }).ready.open,
    ).toBe(true);
  });

  it("reuses the previous object when readiness is unchanged", () => {
    const ready = deriveProjectPresentation(readyFacts());
    const again = retainMountedProjectPresentation(
      ready,
      deriveProjectPresentation(readyFacts()),
      READY_SCOPE,
    );
    expect(again).toBe(ready);
  });

  it("keeps columns mounted but leaves visible latching to the reveal controller", () => {
    const open = deriveProjectPresentation(readyFacts());
    const switching = deriveProjectPresentation({
      ...readyFacts(),
      contentSettled: false,
      scope: SWITCHING_SCOPE,
    });
    const held = retainMountedProjectPresentation(
      open,
      switching,
      SWITCHING_SCOPE,
    );
    expect(held.ready.open).toBe(false);
    expect(held.ready.mounted).toBe(true);
  });

  it("accepts a late first-open hold from a newly mounted region", () => {
    const initiallySettled = deriveProjectPresentation(readyFacts());
    const childRegistered = deriveProjectPresentation({
      ...readyFacts(),
      contentSettled: false,
    });
    const held = retainMountedProjectPresentation(
      initiallySettled,
      childRegistered,
      READY_SCOPE,
    );

    expect(held.ready.mounted).toBe(true);
    expect(held.ready.open).toBe(false);
  });

  it("mounts through a same-project switch even before the first open", () => {
    const mounted = deriveProjectPresentation({
      ...readyFacts(),
      contentSettled: false,
    });
    const switching = deriveProjectPresentation({
      ...readyFacts(),
      contentSettled: false,
      scope: SWITCHING_SCOPE,
    });
    const held = retainMountedProjectPresentation(
      mounted,
      switching,
      SWITCHING_SCOPE,
    );
    expect(held.ready.mounted).toBe(true);
    expect(held.ready.open).toBe(false);
  });

  it("veils the next project rather than carrying the open workspace over", () => {
    const open = deriveProjectPresentation(readyFacts());
    const otherScope: StageScope = {
      project_id: "project-b",
      session_id: null,
      phase: "switching",
    };
    const other = deriveProjectPresentation({
      ...readyFacts(),
      projectId: "project-b",
      contentSettled: false,
      scope: otherScope,
    });
    const held = retainMountedProjectPresentation(open, other, otherScope);
    expect(held.ready.open).toBe(false);
    expect(held.ready.mounted).toBe(false);
  });
});
