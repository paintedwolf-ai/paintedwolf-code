import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { denRoot, loadSourceCorpus, requireNonEmpty } from "../test/source-corpus.ts";

const root = denRoot;

type TauriSecurity = {
  csp?: string;
  devCsp?: string;
};

function security(): TauriSecurity {
  const conf = JSON.parse(
    readFileSync(join(root, "src-tauri/tauri.conf.json"), "utf8"),
  ) as { app?: { security?: TauriSecurity } };
  const sec = conf.app?.security;
  expect(sec, "tauri.conf.json is missing app.security").toBeTruthy();
  return sec as TauriSecurity;
}

const source = loadSourceCorpus(join(root, "src"), {
  extensions: [".ts", ".tsx"],
  excludeTests: true,
});

describe("webview CSP", () => {
  // Dynamic editor styles require the page nonce.
  it("every CodeMirror editor state carries the page style nonce", () => {
    const creators = requireNonEmpty("CodeMirror editor creators", source.files.filter((file) =>
      file.text.includes("EditorState.create("),
    ));
    const bare = creators.filter(
      (file) => !file.text.includes("editorCspNonce()"),
    );
    expect(bare.map((file) => file.rel), "editor states created without editorCspNonce()").toEqual([]);
  });

  /**
   * Runtime styles need the page nonce under the packaged CSP.
   */
  it("every runtime-created style carries the page nonce", () => {
    const NONCE_TOKENS = [
      // Harvests the nonce off a served tag.
      "documentStyleNonce()",
      // Editor stylesheet nonce.
      "editorCspNonce()",
      // Direct assignment onto the created element.
      ".nonce =",
    ];
    const files = [...source.files, {
      path: join(root, "index.html"),
      rel: "index.html",
      text: readFileSync(join(root, "index.html"), "utf8"),
    }];
    const bare = files.filter((file) => {
      const text = file.text;
      if (!/createElement\(\s*["']style["']\s*\)/.test(text)) return false;
      return !NONCE_TOKENS.some((token) => text.includes(token));
    });
    expect(bare.map((file) => file.rel), "runtime-created <style> without the page nonce").toEqual([]);
  });

  it("ships without unsafe-eval; Vite HMR keeps it on the dev policy only", () => {
    const { csp, devCsp } = security();
    expect(csp, "production csp must be set").toBeTruthy();
    expect(csp).toContain("script-src 'self'");
    expect(csp).not.toMatch(/unsafe-eval/);
    expect(devCsp, "devCsp must exist so HMR is not silently using production csp").toBeTruthy();
    expect(devCsp).toContain("script-src 'self' 'unsafe-eval'");
  });
});
