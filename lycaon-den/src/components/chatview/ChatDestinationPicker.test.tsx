import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  chooseChatDestination,
  resetChatDestinationForTests,
  registerChatDestinationOpenSink,
} from "../../chat/composer/chat-destination.ts";
import { ChatDestinationPicker } from "./ChatDestinationPicker.tsx";

const listProjects = vi.hoisted(() => vi.fn());
const listProjectSessions = vi.hoisted(() => vi.fn());
const createSessionForProject = vi.hoisted(() => vi.fn());
const recordCreatedSessionInRecents = vi.hoisted(() => vi.fn());
const refreshProjectSessions = vi.hoisted(() => vi.fn());

vi.mock("../../platform/connection/app-connection.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../platform/connection/app-connection.ts")>()),
  getLycaonClient: () => ({ listProjects, listProjectSessions }),
}));

vi.mock("../../chat/session/session-lifecycle.ts", () => ({
  createSessionForProject,
  recordCreatedSessionInRecents,
}));

const pickerProps = {
  recents: {} as never,
  projectSessions: { refresh: refreshProjectSessions } as never,
};

beforeEach(() => {
  vi.clearAllMocks();
  resetChatDestinationForTests();
  recordCreatedSessionInRecents.mockResolvedValue(undefined);
  refreshProjectSessions.mockResolvedValue(undefined);
  listProjects.mockResolvedValue([
    { id: "project-1", name: "Painted Wolf", roots: [] },
  ]);
  listProjectSessions.mockResolvedValue({
    sessions: [
      {
        id: "session-1",
        project_id: "project-1",
        title: "Visible chat",
        message_count: 4,
      },
      {
        id: "session-2",
        project_id: "project-1",
        title: "Destination chat",
        message_count: 8,
      },
    ],
  });
});

describe("ChatDestinationPicker", () => {
  it.each([0, 1, 2])("labels a destination with %i messages correctly", async (count) => {
    listProjectSessions.mockResolvedValue({ sessions: [{
      id: "session-1", project_id: "project-1", title: "Destination chat", message_count: count,
    }] });
    render(() => <ChatDestinationPicker {...pickerProps} />);
    const chosen = chooseChatDestination({ projectId: "project-1" });
    expect(await screen.findByText(`${count} ${count === 1 ? "message" : "messages"}`)).toBeTruthy();
    fireEvent.click(screen.getByText("Destination chat"));
    await expect(chosen).resolves.toEqual({ projectId: "project-1", sessionId: "session-1" });
  });

  it("highlights the visible chat but returns the exact row chosen", async () => {
    render(() => <ChatDestinationPicker {...pickerProps} />);
    const chosen = chooseChatDestination({
      projectId: "project-1",
      suggested: { projectId: "project-1", sessionId: "session-1" },
    });

    expect(await screen.findByText("Current chat · 4 messages")).toBeTruthy();
    fireEvent.click(await screen.findByText("Destination chat"));

    await expect(chosen).resolves.toEqual({
      projectId: "project-1",
      sessionId: "session-2",
    });
    expect(screen.queryByTestId("chat-destination-picker")).toBeNull();
  });

  it("opens a newly created chat before returning it as the attachment destination", async () => {
    let finishOpening!: () => void;
    const opening = new Promise<void>((resolve) => { finishOpening = resolve; });
    const open = vi.fn(() => opening);
    const unregister = registerChatDestinationOpenSink(open);
    createSessionForProject.mockResolvedValue({
      id: "session-new",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "project-1",
      posture: "build",
      status: "preparing",
      created_at: "2025-01-01T00:00:00Z",
      activity_at: "2025-01-01T00:00:00Z",
      updated_at: "2025-01-01T00:00:00Z",
    });
    render(() => <ChatDestinationPicker {...pickerProps} />);
    const chosen = chooseChatDestination({ projectId: "project-1" });

    fireEvent.click(await screen.findByTestId("chat-destination-new"));

    await waitFor(() => expect(open).toHaveBeenCalledWith({
      projectId: "project-1", sessionId: "session-new",
    }));
    expect(screen.queryByTestId("chat-destination-picker")).not.toBeNull();
    finishOpening();

    await expect(chosen).resolves.toEqual(
      expect.objectContaining({
        projectId: "project-1",
        sessionId: "session-new",
      }),
    );
    expect(createSessionForProject).toHaveBeenCalledWith(
      expect.anything(),
      "project-1",
    );
    expect(recordCreatedSessionInRecents).toHaveBeenCalledWith(
      pickerProps.recents,
      expect.objectContaining({
        projectId: "project-1",
        sessionId: "session-new",
      }),
    );
    expect(refreshProjectSessions).toHaveBeenCalledTimes(1);
    unregister();
  });

  it("cancels a pending destination when its host unmounts", async () => {
    const rendered = render(() => <ChatDestinationPicker {...pickerProps} />);
    const chosen = chooseChatDestination({ projectId: "project-1" });

    rendered.unmount();

    await expect(chosen).resolves.toBeNull();
  });
});
