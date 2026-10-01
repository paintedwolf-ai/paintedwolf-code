import { describe, expect, it } from "vitest";
import type { ItemWindowView } from "../platform/windows/item-windows.ts";
import { peerWindowsForSubject, type PeerOpenSubject } from "./peer-view-subject.ts";

const SUBJECT: PeerOpenSubject = {
  kind: "file",
  projectId: "p1",
  rootId: "r1",
  path: "src/a.ts",
  title: "a.ts",
};

const fileView = (label: string, viewNumber: number, path = "src/a.ts"): ItemWindowView => ({
  label,
  title: path.split("/").pop()!,
  viewNumber,
  kind: "file",
  projectId: "p1",
  rootId: "r1",
  path,
});

const VIEWS = [fileView("file:x:2", 2), fileView("file:x:3", 3), fileView("file:y:4", 4, "src/b.ts")];

describe("peerWindowsForSubject", () => {
  it("lists other windows for the subject from the main window", () => {
    const windows = peerWindowsForSubject(VIEWS, SUBJECT, {
      selfClientId: "window:main",
      mainPresents: true,
    });
    expect(windows.map((window) => window.label)).toEqual(["Window 2", "Window 3"]);
  });

  it("leaves out the current peer window and leads with the main window", () => {
    const windows = peerWindowsForSubject(VIEWS, SUBJECT, {
      selfClientId: "window:file:x:2",
      mainPresents: true,
    });
    expect(windows.map((window) => [window.label, window.nativeLabel, window.title])).toEqual([
      ["Main window", "main", "a.ts"],
      ["Window 3", "file:x:3", "a.ts"],
    ]);
  });

  it("omits the main window when it does not present the subject", () => {
    const windows = peerWindowsForSubject(VIEWS, SUBJECT, {
      selfClientId: "window:file:x:2",
      mainPresents: false,
    });
    expect(windows.map((window) => window.label)).toEqual(["Window 3"]);
  });
});
