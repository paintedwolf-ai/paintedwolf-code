import { expect } from "@playwright/test";
import { openProjectFilesFixture, webE2e } from "./helpers.ts";

webE2e("double-click restores the Files tree width without pointer capture", async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 });
  await openProjectFilesFixture(page, page.request, {
    prefix: "files-tree-resize",
    name: "Files tree resize",
    seed: () => {},
  });

  const grip = page.getByTestId("files-tree-resize");
  const pane = page.getByTestId("files-tree-pane");
  await grip.press("Enter");
  const defaultWidth = (await pane.boundingBox())!.width;
  const box = (await grip.boundingBox())!;
  const x = box.x + box.width / 2;
  const y = box.y + box.height / 2;

  await page.mouse.move(x, y);
  await page.mouse.down();
  await page.mouse.move(x + 48, y);
  await page.mouse.up();
  await expect.poll(async () => (await pane.boundingBox())!.width).toBe(defaultWidth + 48);

  // Exercise the window-listener fallback when capture cannot be acquired.
  await grip.evaluate((el) => {
    el.setPointerCapture = () => {
      throw new DOMException("Pointer capture unavailable", "NotFoundError");
    };
  });
  await grip.dblclick();
  await expect.poll(async () => (await pane.boundingBox())!.width).toBe(defaultWidth);
});
