import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@solidjs/testing-library";
import { APP_SCOPE, projectScope, type NoticeScope } from "../notices/notice-scope.ts";
import { createNoticeStore } from "../notices/notice-store.ts";
import { NotificationStack } from "./NotificationStack.tsx";

afterEach(() => {
  document.body.replaceChildren();
});

function publish(
  store: ReturnType<typeof createNoticeStore>,
  scope: NoticeScope,
  title: string,
) {
  store.publish({ title, message: `${title} detail` }, scope);
}

function mount(
  store: ReturnType<typeof createNoticeStore>,
  overrides: {
    activeProjectId?: string;
    onOpenProject?: (projectId: string) => void;
  } = {},
) {
  const names: Record<string, string> = {
    "proj-a": "Alpha",
    "proj-b": "Beta",
  };
  return render(() => (
    <NotificationStack
      index={store.index()}
      activeProjectId={overrides.activeProjectId}
      projectName={(projectId) => names[projectId]}
      onOpenProject={overrides.onOpenProject ?? (() => undefined)}
      onDismiss={(id) => store.dismiss(id)}
      onDismissApp={() => store.dismissScope(APP_SCOPE)}
    />
  ));
}

describe("NotificationStack", () => {
  it("shows app notices first and every project's notices after them", () => {
    const store = createNoticeStore();
    publish(store, projectScope("proj-a"), "Alpha condition");
    publish(store, APP_SCOPE, "App condition");
    publish(store, projectScope("proj-b"), "Beta condition");

    mount(store, { activeProjectId: "proj-a" });

    const stack = screen.getByTestId("notification-stack");
    expect(stack.children[0]?.classList.contains("den-notice-rail")).toBe(true);
    expect(
      [...stack.querySelectorAll<HTMLElement>("[data-project-id]")].map(
        (group) => group.dataset.projectId,
      ),
    ).toEqual(["proj-a", "proj-b"]);
    expect(screen.getByText("App condition")).toBeTruthy();
    expect(screen.getByText("Alpha condition")).toBeTruthy();
    expect(screen.getByText("Beta condition")).toBeTruthy();
  });

  it("names a project and opens it from the attribution link", () => {
    const store = createNoticeStore();
    const onOpenProject = vi.fn();
    publish(store, projectScope("proj-a"), "Needs attention");

    mount(store, { onOpenProject });

    screen.getByLabelText("Open project Alpha").click();
    expect(onOpenProject).toHaveBeenCalledWith("proj-a");
  });

  it("renders project card content and dismisses it", () => {
    const store = createNoticeStore();
    store.publish(
      {
        title: "Project trust changed",
        message: "Nothing was enabled.",
        suggestedAction: "Review the updated project settings.",
      },
      projectScope("proj-a"),
    );

    mount(store);

    const group = screen.getByTestId("project-notices");
    expect(within(group).getByTestId("project-notice-card")).toBeTruthy();
    expect(within(group).getByText("Nothing was enabled.")).toBeTruthy();
    expect(within(group).getByText("Review the updated project settings.")).toBeTruthy();
    within(group).getByLabelText("Dismiss").click();
    expect(screen.queryByTestId("notification-stack")).toBeNull();
  });

  it("dismisses all app notices without touching project notices", () => {
    const store = createNoticeStore();
    publish(store, APP_SCOPE, "App one");
    publish(store, APP_SCOPE, "App two");
    publish(store, projectScope("proj-a"), "Project condition");

    mount(store);
    screen.getByText("Dismiss all").click();

    expect(screen.queryByText("App one")).toBeNull();
    expect(screen.queryByText("App two")).toBeNull();
    expect(screen.getByText("Project condition")).toBeTruthy();
  });

  it("keeps an orphaned notice visible but does not offer broken navigation", () => {
    const store = createNoticeStore();
    publish(store, projectScope("missing"), "Still actionable");

    mount(store);

    expect(screen.getByText("Unavailable project")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Open project/ })).toBeNull();
    expect(screen.getByText("Still actionable")).toBeTruthy();
  });
});
