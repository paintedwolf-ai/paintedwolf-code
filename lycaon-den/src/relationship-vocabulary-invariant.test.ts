import { describe, expect, it } from "vitest";
import { denRoot, loadSourceCorpus } from "./test/source-corpus.ts";

const scanRoots = ["src", "shared", "e2e", "src-tauri/src"];
const excluded = new Set([
  "src/relationship-vocabulary-invariant.test.ts",
  "src/api/operations.generated.ts",
  "src/api/types.ts",
  "src/chat/tool/tool-presentation.generated.ts",
]);

const corpus = loadSourceCorpus(denRoot, {
  extensions: [".css", ".json", ".plist", ".rs", ".ts", ".tsx", ".yaml", ".yml"],
});

function permittedTerm(path: string, line: string): boolean {
  // The person role "owner" and its wire field name the people domain's role.
  if (/\bowner_person_id\b/.test(line)) return true;
  if (/\brole:\s*"owner"/.test(line)) return true;
  // Rust standard, tokio, and serde API names.
  if (/\b(?:into_owned|to_owned|spawn_argv_owned|lock_owned|try_lock_owned|read_owned|DeserializeOwned)\b/.test(line)) return true;
  if (/\b(?:getOwnPropertyDescriptor|hasOwn|hasOwnProperty|ownerDocument)\b/.test(line)) {
    return true;
  }
  if (/\bowner-(?:bound|readable|write|writable)\b/i.test(line)) return true;
  if (/\b(?:reactive|Solid) owner\b/.test(line)) return true;
  if (/\b(?:getOwner|runWithOwner)\b/.test(line)) return true;
  if (
    path.startsWith("src-tauri/src/") &&
    /\bDeviceOwnerAuthentication\b/.test(line)
  ) {
    return true;
  }
  if (
    path === "src/components/nav/TabBar.test.tsx" &&
    /\b(?:getOwner|OwnedPanel|panelOwner)\b/.test(line)
  ) {
    return true;
  }
  if (/github\.com\/owner\/repo/.test(line)) return true;
  if (/\bchown\b.*\bowner\b/.test(line)) return true;
  if (/data-invocation-owner|invocation\?\.owner/.test(line)) return true;
  if (/\bowner:\s*"filesystem"/.test(line)) return true;
  if (/"owner":\s*"(?:scans|workflows)"/.test(line)) return true;
  if (/"owner_ref"|"recovery_policy":\s*"owner"/.test(line)) return true;
  if (
    /^(?:src\/settings\/extensions\/extensions-chips|src\/components\/settings\/extensions\/(?:ExtensionsSettingsPanel|extension-actions))/.test(
      path,
    ) &&
    /(?:"owned"|\bsetUnitOwn\b)/.test(line)
  ) {
    return true;
  }
  return false;
}

describe("relationship vocabulary", () => {
  it("reserves owner terms for subsystems and native domains", () => {
    const findings: string[] = [];
    const term =
      /(?<![A-Za-z])(?:[Oo]wner(?:s|ship)?|[Oo]wns|[Oo]wned)(?![A-Za-z])|(?<=[a-z0-9])(?:Owner(?:s|ship)?|Own(?:s|ed)?)(?![a-z])|\b(?:own|owned|owner)(?=[A-Z])/;
    const declaration =
      /\b(?:class|const|function|interface|let|type|var)\s+(?:own\b|[A-Za-z0-9_$]*Own[A-Za-z0-9_$]*)/;

    for (const root of scanRoots) {
      for (const file of corpus.under(root)) {
        const path = file.rel;
        if (excluded.has(path)) continue;
        const lines = file.text.split("\n");
        lines.forEach((line, index) => {
          if (!term.test(line) && !declaration.test(line)) return;
          if (permittedTerm(path, line)) return;
          findings.push(`${path}:${index + 1}: ${line.trim()}`);
        });
      }
    }

    expect(findings).toEqual([]);
  });
});
