import {
  chmodSync,
  mkdirSync,
  readFileSync,
  writeFileSync,
} from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { expect, type Page } from "@playwright/test";
import {
  openProjectFilesFixture,
  webE2e,
} from "./helpers.ts";

const screenshotDir = path.resolve(
  process.env.LYCAON_E2E_OUTPUT_DIR ?? path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../test-results"),
  "file-truth-encoding-screenshots",
);
mkdirSync(screenshotDir, { recursive: true });

async function openTreeFile(page: Page, name: string) {
  const file = page.getByTestId("files-tree-file").filter({ hasText: name });
  await expect(file).toBeVisible({ timeout: 15_000 });
  await file.click();
}

type EncodingCase = {
  id: string;
  chip: string | null;
  eol: "\n" | "\r\n";
  encode: (text: string) => Buffer;
  openAs?: "UTF-16 LE" | "UTF-16 BE";
};

function utf16(text: string, bigEndian: boolean, bom: boolean): Buffer {
  const body = Buffer.from(text, "utf16le");
  if (bigEndian) {
    for (let i = 0; i < body.length; i += 2) {
      [body[i], body[i + 1]] = [body[i + 1]!, body[i]!];
    }
  }
  if (!bom) return body;
  return Buffer.concat([
    Buffer.from(bigEndian ? [0xfe, 0xff] : [0xff, 0xfe]),
    body,
  ]);
}

const encodings: EncodingCase[] = [
  { id: "utf-8", chip: null, eol: "\n", encode: (text) => Buffer.from(text) },
  {
    id: "utf-8-bom",
    chip: "UTF-8 BOM",
    eol: "\r\n",
    encode: (text) =>
      Buffer.concat([Buffer.from([0xef, 0xbb, 0xbf]), Buffer.from(text)]),
  },
  {
    id: "utf-16le",
    chip: "UTF-16 LE",
    eol: "\n",
    encode: (text) => utf16(text, false, false),
    openAs: "UTF-16 LE",
  },
  {
    id: "utf-16le-bom",
    chip: "UTF-16 LE BOM",
    eol: "\r\n",
    encode: (text) => utf16(text, false, true),
  },
  {
    id: "utf-16be",
    chip: "UTF-16 BE",
    eol: "\n",
    encode: (text) => utf16(text, true, false),
    openAs: "UTF-16 BE",
  },
  {
    id: "utf-16be-bom",
    chip: "UTF-16 BE BOM",
    eol: "\r\n",
    encode: (text) => utf16(text, true, true),
  },
];

webE2e(
  "file truth: every supported encoding opens, identifies, and saves losslessly",
  async ({ page, request }) => {
    const seeded = await openProjectFilesFixture(page, request, {
      prefix: "file-truth",
      name: "FileTruth",
      seed: (root) => {
        for (const encoding of encodings) {
          writeFileSync(
            path.join(root, `${encoding.id}.txt`),
            encoding.encode(`hello${encoding.eol}世界 🐺${encoding.eol}A\u030a`),
          );
        }
      },
    });

    for (const encoding of encodings) {
      await openTreeFile(page, `${encoding.id}.txt`);
      if (encoding.openAs) {
        await page
          .getByRole("button", { name: `Open as ${encoding.openAs}` })
          .click();
      }
      const host = page.getByTestId("files-editor-host").filter({ visible: true });
      await expect(host).toBeVisible({ timeout: 15_000 });
      const chip = page.getByTestId("files-editor-encoding-chip").filter({ visible: true });
      if (encoding.chip === null) {
        await expect(chip).toHaveCount(0);
      } else {
        await expect(chip).toHaveText(encoding.chip);
      }

      const edited = `edited ${encoding.id}\n世界 🐺\nA\u030a`;
      const saved = edited.replaceAll("\n", encoding.eol);
      const content = host.locator(".cm-content");
      await expect(content).toBeEditable();
      await content.click();
      await page.keyboard.press("ControlOrMeta+A");
      await page.keyboard.insertText(edited);
      await expect(content).toContainText(`edited ${encoding.id}`);
      await page.getByTestId("files-editor-save").click();
      await expect(page.getByTestId("files-editor-save")).toHaveCount(0, {
        timeout: 15_000,
      });
      await expect.poll(
        () =>
          readFileSync(
            path.join(seeded.root, `${encoding.id}.txt`),
          ).toString("hex"),
        {
          message: `${encoding.id} exact disk bytes`,
          timeout: 15_000,
        },
      ).toBe(encoding.encode(saved).toString("hex"));
    }

  },
);

webE2e("file truth: read-only identity and action", async ({ page, request }) => {
  await openProjectFilesFixture(page, request, {
    prefix: "file-truth",
    name: "FileTruth",
    seed: (root) => {
      const abs = path.join(root, "ro.txt");
      writeFileSync(abs, "locked\n", { mode: 0o444 });
      chmodSync(abs, 0o444);
    },
  });
  await openTreeFile(page, "ro.txt");
  await expect(page.getByTestId("files-editor-host").filter({ visible: true })).toBeVisible({
    timeout: 15_000,
  });
  await expect(page.getByTestId("files-editor-mode")).toHaveText(
    "Read-only file",
    { timeout: 15_000 },
  );
  const makeEditable = page.getByTestId("files-editor-make-editable");
  await expect(makeEditable).toHaveAttribute(
    "aria-label",
    "Make editable",
  );
  await makeEditable.click();
  await expect(page.locator(".den-files-editor__status:visible")).toHaveAttribute("data-editor-identity", "editing");
  await expect(makeEditable).toHaveCount(0);
});

webE2e(
  "file truth: unsupported legacy encoding info card",
  async ({ page, request }) => {
    await openProjectFilesFixture(page, request, {
      prefix: "file-truth",
      name: "FileTruth",
      seed: (root) => writeFileSync(
          path.join(root, "legacy.txt"),
          Buffer.from([0x63, 0x61, 0x66, 0xe9]),
        ),
    });
    await openTreeFile(page, "legacy.txt");
    await expect(page.getByTestId("files-info-card")).toBeVisible({
      timeout: 15_000,
    });
    await expect(page.getByTestId("files-info-title")).toContainText(
      "Can't open this file safely",
    );
    await expect(page.getByTestId("files-info-explain")).toContainText(
      "unsupported or malformed text encoding",
    );
  },
);

webE2e("file truth: ordinary source is never promoted to binary", async ({ page, request }) => {
  await openProjectFilesFixture(page, request, {
    prefix: "file-truth",
    name: "FileTruth",
    seed: (root) => {
      writeFileSync(path.join(root, "Cargo.toml"), '[package]\nname = "hello_registry"\nversion = "0.1.1"\n');
      writeFileSync(path.join(root, "bitmap-prefix.txt"), "BM is ordinary text\n");
      writeFileSync(path.join(root, "unicode.txt"), "a".repeat(8191) + "🐺\n");
      writeFileSync(path.join(root, "empty.txt"), "");
    },
  });
  for (const name of ["Cargo.toml", "bitmap-prefix.txt", "unicode.txt", "empty.txt"]) {
    await openTreeFile(page, name);
    await expect(page.getByTestId("files-editor-host").filter({ visible: true })).toBeVisible({ timeout: 15_000 });
    await expect(page.getByTestId("files-info-card")).toHaveCount(0);
    await expect(page.getByTestId("files-image-viewer")).toHaveCount(0);
  }
  await page.screenshot({ path: path.join(screenshotDir, "source-opening.png") });
});

webE2e("file truth: open tabs recover as external file types change", async ({ page, request }) => {
  const seeded = await openProjectFilesFixture(page, request, {
    prefix: "file-truth",
    name: "FileTruth",
    seed: (root) => writeFileSync(
      path.join(root, "changing.txt"),
      Buffer.from([0, 1, 2, 3]),
    ),
  });
  await openTreeFile(page, "changing.txt");
  const file = path.join(seeded.root, "changing.txt");
  const explanation = page.getByTestId("files-info-explain");
  await expect(explanation).toContainText("binary", { timeout: 15_000 });

  writeFileSync(file, "now editable\n");
  const host = page.getByTestId("files-editor-host").filter({ visible: true });
  await expect(host).toBeVisible({ timeout: 15_000 });
  await expect(host.locator(".cm-content")).toContainText("now editable");

  writeFileSync(file, Buffer.from([0x63, 0x61, 0x66, 0xe9]));
  await expect(explanation).toContainText("unsupported", { timeout: 15_000 });
  writeFileSync(file, "converted to UTF-8\n");
  await expect(host).toBeVisible({ timeout: 15_000 });
  await expect(host.locator(".cm-content")).toContainText("converted to UTF-8");

  writeFileSync(file, "x".repeat(4 * 1024 * 1024 + 1));
  await expect(explanation).toContainText("4 MB", { timeout: 15_000 });
  writeFileSync(file, "small again\n");
  await expect(host).toBeVisible({ timeout: 15_000 });
  await expect(host.locator(".cm-content")).toContainText("small again");
  await page.screenshot({ path: path.join(screenshotDir, "file-type-recovery.png") });
});

webE2e("file truth: remembered byte order survives refresh and reopening", async ({ page, request }) => {
  const seeded = await openProjectFilesFixture(page, request, {
    prefix: "file-truth",
    name: "FileTruth",
    seed: (root) => writeFileSync(
      path.join(root, "wide.txt"),
      utf16("hello\n", false, false),
    ),
  });
  await openTreeFile(page, "wide.txt");
  await page.getByRole("button", { name: "Open as UTF-16 LE", exact: true }).click();
  const host = page.getByTestId("files-editor-host").filter({ visible: true });
  await expect(host).toBeVisible({ timeout: 15_000 });
  writeFileSync(path.join(seeded.root, "wide.txt"), utf16("changed externally\n", false, false));
  await expect(host.locator(".cm-content")).toContainText("changed externally", { timeout: 15_000 });
  await page.getByTestId("files-tab-close").click();
  await expect(host).toHaveCount(0);
  await page.keyboard.press("ControlOrMeta+Shift+T");
  await expect(host).toBeVisible({ timeout: 15_000 });
  await expect(page.getByTestId("files-editor-encoding-chip").filter({ visible: true })).toHaveText("UTF-16 LE");
  await expect(host.locator(".cm-content")).toContainText("changed externally");

  writeFileSync(path.join(seeded.root, "wide.txt"), "converted to UTF-8\n");
  await expect(host.locator(".cm-content")).toContainText("converted to UTF-8", { timeout: 15_000 });
  await expect(page.getByTestId("files-editor-encoding-chip").filter({ visible: true })).toHaveCount(0);
});
