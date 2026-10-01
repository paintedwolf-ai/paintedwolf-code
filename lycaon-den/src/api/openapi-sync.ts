import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), "../../..");

/** Generated types.ts via ./task codegen:den-types; helpers compose it. */
export function openAPIBundlePath(): string {
  return join(repoRoot, "docs/openapi.yaml");
}

export function readOpenAPIBundle(): string {
  return readFileSync(openAPIBundlePath(), "utf8");
}

/** Parse string enums from generated types.ts (openapi-typescript schema members). */
export function parseTSEnumUnions(source: string): Map<string, string[]> {
  const out = new Map<string, string[]>();
  // Schema-level members are indented 8 spaces under components.schemas. The
  // alternation is `*` so a single-value enum, emitted as one bare string literal, still parses.
  const nested =
    /^        (\w+):\s("(?:[^"]+)"(?:\s*\|\s*"(?:[^"]+)")*);/gm;
  let m: RegExpExecArray | null;
  while ((m = nested.exec(source)) !== null) {
    const name = m[1];
    if (!name) continue;
    const values = (m[2] ?? "")
      .split("|")
      .map((seg) => seg.trim().replace(/^"|"$/g, ""))
      .filter(Boolean);
    if (values.length > 0) out.set(name, values);
  }
  return out;
}

/** Parse OpenAPI component enum schemas from bundled yaml. */
export function parseOpenAPIEnums(yaml: string): Map<string, string[]> {
  const out = new Map<string, string[]>();
  const lines = yaml.split("\n");
  for (let i = 0; i < lines.length; i++) {
    const nameMatch = /^    (\w+):$/.exec(lines[i] ?? "");
    if (!nameMatch || lines[i + 1] !== "      type: string") continue;
    const name = nameMatch[1];
    if (!name) continue;
    let j = i + 2;
    while (j < lines.length && lines[j] !== "      enum:") j++;
    if (j >= lines.length) continue;
    j++;
    const values: string[] = [];
    while (j < lines.length && (lines[j] ?? "").startsWith("        - ")) {
      values.push(
        (lines[j] ?? "")
          .slice("        - ".length)
          .trim()
          .replace(/^["']|["']$/g, ""),
      );
      j++;
    }
    if (values.length > 0) out.set(name, values);
  }
  return out;
}

export function sortedSetEqual(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false;
  const set = new Set(a);
  return b.every((v) => set.has(v));
}

export function openAPISchemaProperties(yaml: string, schemaName: string): string[] {
  const start = yaml.indexOf(`    ${schemaName}:\n`);
  if (start < 0) return [];
  const propsIdx = yaml.indexOf("      properties:\n", start);
  if (propsIdx < 0) return [];
  const slice = yaml.slice(propsIdx + "      properties:\n".length);
  const props: string[] = [];
  for (const line of slice.split("\n")) {
    if (!line.startsWith("        ")) break;
    const match = /^        (\w+):/.exec(line);
    if (match?.[1]) props.push(match[1]);
  }
  return props;
}
