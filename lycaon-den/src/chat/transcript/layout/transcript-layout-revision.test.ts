import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import assert from "node:assert/strict";
import path from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { transcriptLayoutPlugin, transcriptLayoutRevision } from "../../../../vite.transcript-layout.ts";

const directories: string[] = [];
afterEach(() => { for (const directory of directories.splice(0)) rmSync(directory, { recursive: true }); });
function fixture() {
  const root = mkdtempSync(path.join(tmpdir(), "transcript-layout-"));
  directories.push(root);
  mkdirSync(path.join(root, "src"));
  mkdirSync(path.join(root, "shared"));
  mkdirSync(path.join(root, "public/fonts"), { recursive: true });
  writeFileSync(path.join(root, "bun.lock"), "dependencies");
  writeFileSync(path.join(root, "src", "card.css"), ".card { padding: 4px; }");
  return root;
}

describe("transcript renderer revision", () => {
  it("invalidates heights for CSS, markup, fonts and dependency changes without shipping their contents", () => {
    const root = fixture();
    let before = transcriptLayoutRevision(root);
    expect(before).toMatch(/^[a-f0-9]{64}$/);
    for (const [file, content] of [
      ["src/card.css", ".card { padding: 8px; }"],
      ["src/card.tsx", "const card = <div class='p-4'/>"],
      ["shared/layout.ts", "export const padding = 16"],
      ["bun.lock", "new dependencies"],
      ["public/fonts/Body.woff2", "new glyph metrics"],
    ]) {
      writeFileSync(path.join(root, file!), content!);
      const after = transcriptLayoutRevision(root);
      expect(after).not.toBe(before);
      expect(transcriptLayoutRevision(root)).toBe(after);
      before = after;
    }
  });
  it("ignores test changes and checkout location", () => {
    const first = fixture();
    const second = fixture();
    writeFileSync(path.join(first, "src", "card.test.tsx"), "test code");
    expect(transcriptLayoutRevision(first)).toBe(transcriptLayoutRevision(second));
  });
});


it("serves the current renderer generation after a hot update and a page reload", async () => {
  const root = fixture();
  const plugin = transcriptLayoutPlugin(root);
  assert(typeof plugin.transform === "function" && typeof plugin.hotUpdate === "function", "Layout plugin hooks are unavailable");
  const runtime = path.join(root, "src/chat/transcript/layout/transcript-layout-revision.ts");
  const code = readFileSync(new URL("./transcript-layout-revision.ts", import.meta.url), "utf8");
  const transform = () => plugin.transform instanceof Function
    ? plugin.transform.call({} as never, code, runtime)
    : undefined;
  let before = await transform();
  const module = { id: runtime };
  const invalidateModule = vi.fn();
  const send = vi.fn();
  for (const [file, content] of [["src/card.css", ".card { padding: 30px; }"], ["bun.lock", "updated dependencies"]] as const) {
    invalidateModule.mockClear();
    send.mockClear();
    writeFileSync(path.join(root, file), content);
    await plugin.hotUpdate.call({ environment: {
      name: "client", moduleGraph: { getModuleById: () => module, invalidateModule }, hot: { send },
    } } as never, { file: path.join(root, file) } as never);
    const revision = transcriptLayoutRevision(root);
    expect(invalidateModule).toHaveBeenCalledExactlyOnceWith(module);
    expect(send).toHaveBeenCalledExactlyOnceWith({ type: "custom", event: "transcript-layout-revision", data: revision });
    const after = await transform();
    assert(after && typeof after === "object", "Renderer module was not transformed");
    expect(after.code).toContain(JSON.stringify(revision));
    expect(after.code).not.toContain("__TRANSCRIPT_LAYOUT_REVISION__");
    expect(after).not.toEqual(before);
    before = after;
  }
});
