import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import YAML from "yaml";
import { describe, expect, it } from "vitest";
import { parseBinding, bindingIsProducible } from "./chord.ts";
import { defaultBindingAllowedOnPlatform } from "./binding-policy.ts";
import { DEFAULT_NAV_LEADER_CHORD } from "./keymap.ts";
import {
  reservedReason,
  TAURI_PLATFORMS,
} from "./platform.ts";
import type { TauriPlatform } from "../platform/runtime.ts";
import { STOCK_FRAME } from "../contributions/stock-frame.generated.ts";

type Declaration = {
  file: string;
  id: string;
  bindings: Record<string, string[]>;
};

const packsRoot = resolve(import.meta.dirname, "../../../lycaon/config/packs");

// Walks every bundled pack, not only the frame's platform pack.
function keybindingFiles(dir: string): string[] {
  const out: string[] = [];
  for (const name of readdirSync(dir).sort()) {
    const path = join(dir, name);
    if (!statSync(path).isDirectory()) continue;
    if (name === "keybindings" && dir.endsWith("contributions")) {
      for (const file of readdirSync(path).sort()) {
        if (file.endsWith(".yaml")) out.push(join(path, file));
      }
      continue;
    }
    out.push(...keybindingFiles(path));
  }
  return out;
}

const declarations: Declaration[] = keybindingFiles(packsRoot).map((path) => {
  const body = YAML.parse(readFileSync(path, "utf8")) as Omit<Declaration, "file">;
  return { ...body, file: relative(packsRoot, path) };
});

function expandLeader(declared: string): string {
  return declared.startsWith("Leader ")
    ? `${DEFAULT_NAV_LEADER_CHORD} ${declared.slice("Leader ".length)}`
    : declared;
}

function violation(binding: string, platform: TauriPlatform): string | null {
  if (defaultBindingAllowedOnPlatform(binding, platform)) return null;
  const parsed = parseBinding(binding);
  if (!parsed) return "does not parse as a chord or leader sequence";
  const steps = parsed.kind === "chord" ? [parsed.chord] : [parsed.leader, parsed.secondKey];
  for (const step of steps) {
    const reason = reservedReason(step, platform);
    if (reason === "os") {
      return `${step} is consumed by ${platform} before the app sees it (OS_INTERCEPTED_CHORDS)`;
    }
    if (reason === "assistive") {
      return `${step} is an assistive-navigation key (ASSISTIVE_RESERVED_KEYS)`;
    }
  }
  if (!bindingIsProducible(binding, platform)) {
    return `cannot be produced on a ${platform} keyboard`;
  }
  return "refused by defaultBindingAllowedOnPlatform";
}

describe("every shipped keybinding against the reserved-chord tables", () => {
  it("walks the YAML behind every keybinding the stock frame ships", () => {
    expect(declarations.length).toBeGreaterThan(0);
    const walked = new Set(declarations.map((declaration) => declaration.id));
    const missing = STOCK_FRAME.keybindings
      .map((binding) => binding.id)
      .filter((id) => !walked.has(id));
    expect(missing, "stock frame bindings absent from config/packs").toEqual([]);
  });

  it("declares bindings only for Tauri platforms", () => {
    const known = new Set<string>(TAURI_PLATFORMS);
    const unknown = declarations.flatMap((declaration) =>
      Object.keys(declaration.bindings ?? {})
        .filter((platform) => !known.has(platform))
        .map((platform) => `${declaration.file}: ${platform}`),
    );
    expect(unknown).toEqual([]);
  });

  it("ships no default the OS swallows or an assistive key claims, on any platform", () => {
    const failures: string[] = [];
    for (const declaration of declarations) {
      for (const platform of TAURI_PLATFORMS) {
        for (const declared of declaration.bindings?.[platform] ?? []) {
          const reason = violation(expandLeader(declared), platform);
          if (reason) {
            failures.push(
              `${declaration.file} (${declaration.id}) ${platform} "${declared}": ${reason}. ` +
                "Rule: stock defaults may not claim OS-intercepted or assistive chords. " +
                "Fix: rebind it in that YAML to a chord reservedReason leaves free.",
            );
          }
        }
      }
    }
    expect(failures).toEqual([]);
  });
});
