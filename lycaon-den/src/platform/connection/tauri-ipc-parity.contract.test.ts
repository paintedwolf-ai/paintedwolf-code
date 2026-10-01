import { readFileSync } from "node:fs";
import { join } from "node:path";
import * as ts from "typescript";
import { describe, expect, it } from "vitest";
import {
  denRoot,
  denSourceRoot,
  loadTypeScriptCorpus,
  type TypeScriptSourceFile,
} from "../../test/source-corpus.ts";

const tauriSourceRoot = join(denRoot, "src-tauri", "src");

function rustModuleSource(modulePath: readonly string[]): string {
  const file = modulePath.length === 0 ? "lib" : join(...modulePath);
  return readFileSync(join(tauriSourceRoot, `${file}.rs`), "utf8");
}

const COMMAND_FN =
  /#\[tauri::command(?:\(([^)]*)\))?\]\s*(?:#\[[^\]]*\]\s*)*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?fn\s+(\w+)/g;

/** Maps each command function in a module to the name the IPC bridge registers. */
function commandNames(source: string): Map<string, string> {
  const names = new Map<string, string>();
  for (const [, args, fn] of source.matchAll(COMMAND_FN)) {
    names.set(fn!, args?.match(/\brename\s*=\s*"([^"]+)"/)?.[1] ?? fn!);
  }
  return names;
}

function registeredCommands(): Set<string> {
  const lib = rustModuleSource([]);
  const block = lib.match(/tauri::generate_handler!\[([\s\S]*?)\]/)?.[1];
  if (block === undefined) throw new Error("generate_handler! not found in lib.rs");
  const commands = new Set<string>();
  const entries = block.replace(/\/\/.*$/gm, "").split(",").map((entry) => entry.trim());
  for (const entry of entries.filter(Boolean)) {
    const path = entry.split("::");
    const fn = path.pop()!;
    const name = commandNames(rustModuleSource(path)).get(fn);
    if (!name) expect.fail(`${entry} has no #[tauri::command] declaration`);
    commands.add(name);
  }
  return commands;
}

type Forwarder = { name: string; index: number };

/** The named function whose parameter `param` a call forwards. */
function forwardingFunction(call: ts.Node, param: string): Forwarder | undefined {
  for (let node = call.parent; node; node = node.parent) {
    if (!ts.isFunctionLike(node)) continue;
    const index = node.parameters.findIndex(
      (p) => ts.isIdentifier(p.name) && p.name.text === param,
    );
    if (index < 0) continue;
    if (ts.isFunctionDeclaration(node) && node.name) return { name: node.name.text, index };
    if (ts.isVariableDeclaration(node.parent) && ts.isIdentifier(node.parent.name)) {
      return { name: node.parent.name.text, index };
    }
    return undefined;
  }
  return undefined;
}

function callsTo(file: TypeScriptSourceFile, name: string, visit: (call: ts.CallExpression) => void): void {
  const walk = (node: ts.Node) => {
    if (ts.isCallExpression(node) && ts.isIdentifier(node.expression) && node.expression.text === name) {
      visit(node);
    }
    ts.forEachChild(node, walk);
  };
  walk(file.ast);
}

function location(file: TypeScriptSourceFile, node: ts.Node): string {
  const { line } = file.ast.getLineAndCharacterOfPosition(node.getStart(file.ast));
  return `${file.rel}:${line + 1}`;
}

/** Commands Den sends, including those passed through a wrapper's parameter. */
function invokedCommands(): { commands: Set<string>; unresolved: string[] } {
  const files = loadTypeScriptCorpus(denSourceRoot, { excludeTests: true }).files.filter((file) =>
    file.text.includes("@tauri-apps/api/core"),
  );
  const commands = new Set<string>();
  const unresolved: string[] = [];
  const forwarders = new Map<string, number>();
  for (const file of files) {
    callsTo(file, "invoke", (call) => {
      // A Tauri invoke names its command first; other shapes are local callbacks named invoke.
      const [command] = call.arguments;
      if (command && ts.isStringLiteralLike(command)) {
        commands.add(command.text);
      } else if (command && ts.isIdentifier(command)) {
        const forwarder = forwardingFunction(call, command.text);
        if (forwarder) forwarders.set(forwarder.name, forwarder.index);
        else unresolved.push(location(file, call));
      }
    });
  }
  for (const file of files) {
    for (const [name, index] of forwarders) {
      callsTo(file, name, (call) => {
        const command = call.arguments[index];
        if (command && ts.isStringLiteralLike(command)) commands.add(command.text);
        else unresolved.push(location(file, call));
      });
    }
  }
  return { commands, unresolved };
}

describe("Tauri IPC parity", () => {
  it("Den invokes exactly the commands generate_handler! registers", () => {
    const { commands, unresolved } = invokedCommands();
    expect(unresolved).toEqual([]);
    expect(commands.size).toBeGreaterThan(0);
    expect([...commands].sort()).toEqual([...registeredCommands()].sort());
  });

  it("every coded SidecarStartError variant is a SidecarStartFailure", () => {
    const sidecar = rustModuleSource(["sidecar"]);
    const body = sidecar.match(/pub enum SidecarStartError \{([^}]*)\}/)?.[1];
    if (body === undefined) throw new Error("SidecarStartError not found in sidecar.rs");
    expect(sidecar).toMatch(/#\[serde\(rename_all = "snake_case"[^\]]*\)\]\s*pub enum SidecarStartError/);
    const rustCodes = [...body.matchAll(/^\s*(\w+)\(/gm)]
      .map(([, variant]) => variant!)
      // Failed carries an uncoded message and shows as a generic start failure.
      .filter((variant) => variant !== "Failed")
      .map((variant) => variant.replace(/[A-Z]/g, (c, i: number) => (i > 0 ? "_" : "") + c.toLowerCase()));

    const backend = readFileSync(join(denSourceRoot, "platform", "connection", "backend.ts"), "utf8");
    const union = backend.match(/export type SidecarStartFailure =([^;]*);/)?.[1];
    if (union === undefined) throw new Error("SidecarStartFailure not found in backend.ts");
    const tsCodes = [...union.matchAll(/"([^"]+)"/g)].map(([, code]) => code!);

    expect(tsCodes.sort()).toEqual(rustCodes.sort());
  });
});
