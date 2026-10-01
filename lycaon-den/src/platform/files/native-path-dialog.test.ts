import { beforeEach, describe, expect, it } from "vitest";
import {
  nativePathDialog,
  setNativePathDialogBusyForTests,
} from "./native-path-dialog.ts";

describe("nativePathDialog", () => {
  beforeEach(() => {
    setNativePathDialogBusyForTests(false);
  });

  it("returns null while another pick is already open", async () => {
    setNativePathDialogBusyForTests(true);
    await expect(
      nativePathDialog("Open project folder", { kind: "folder" }),
    ).resolves.toBeNull();
  });
});
