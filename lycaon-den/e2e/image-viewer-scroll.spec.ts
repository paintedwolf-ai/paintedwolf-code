import { writeFileSync } from "node:fs";
import path from "node:path";
import { crc32, deflateSync } from "node:zlib";
import { expect } from "@playwright/test";
import { modelIndependentWebE2e, openProjectFilesFixture } from "./helpers.ts";

function largePng(): Buffer {
  const chunk = (name: string, data: Buffer) => {
    const bytes = Buffer.concat([Buffer.from(name), data]);
    const length = Buffer.alloc(4);
    length.writeUInt32BE(data.length);
    const checksum = Buffer.alloc(4);
    checksum.writeUInt32BE(crc32(bytes));
    return Buffer.concat([length, bytes, checksum]);
  };
  const header = Buffer.alloc(13);
  header.writeUInt32BE(2400, 0);
  header.writeUInt32BE(1600, 4);
  header[8] = 8;
  header[9] = 2;
  return Buffer.concat([
    Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]),
    chunk("IHDR", header),
    chunk("IDAT", deflateSync(Buffer.alloc((2400 * 3 + 1) * 1600))),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

modelIndependentWebE2e("image pixels pan natively and remain decoded across navigation and resize", async ({ page, request }) => {
  await openProjectFilesFixture(page, request, {
    prefix: "image-scroll", name: "Image scrolling",
    seed: (root) => {
      writeFileSync(path.join(root, "large.png"), largePng());
      writeFileSync(path.join(root, "note.txt"), "Another resident file");
    },
  });
  const openFile = async (name: string) => {
    await page.getByTestId("files-tree-file").filter({ hasText: name }).dblclick();
  };
  await openFile("large.png");
  const viewer = page.getByTestId("files-image-viewer").filter({ visible: true });
  const image = viewer.getByTestId("files-image-img");
  const stage = viewer.getByTestId("files-image-stage");
  const pan = stage.locator(":scope > .den-scrollport__viewport");
  await expect(image).toHaveAttribute("width", "2400");
  const source = await image.getAttribute("src");
  await viewer.getByTestId("files-image-zoom-actual").click();
  await stage.hover();
  await page.mouse.wheel(350, 500);
  await expect.poll(() => pan.evaluate((el) => el.scrollTop)).toBeGreaterThan(100);
  await expect.poll(() => pan.evaluate((el) => el.scrollLeft)).toBeGreaterThan(100);
  const offset = await pan.evaluate((el) => ({ x: el.scrollLeft, y: el.scrollTop }));
  await openFile("note.txt");
  await expect(page.getByTestId("files-editor-host").filter({ visible: true })).toBeVisible();
  await openFile("large.png");
  await expect(image).toHaveAttribute("src", source!);
  await expect.poll(() => pan.evaluate((el) => ({ x: el.scrollLeft, y: el.scrollTop }))).toEqual(offset);
  for (const width of [1180, 1440, 1100, 1320]) {
    await page.setViewportSize({ width, height: 850 });
    await expect(image).toHaveAttribute("src", source!);
    await expect.poll(() => image.evaluate((el: HTMLImageElement) => el.complete && el.naturalWidth === 2400)).toBe(true);
  }
  await stage.dblclick();
  await expect(stage).toHaveClass(/--fit/);
  await expect.poll(() => pan.evaluate((el) => ({
    x: el.scrollWidth <= el.clientWidth + 1,
    y: el.scrollHeight <= el.clientHeight + 1,
  }))).toEqual({ x: true, y: true });
});
