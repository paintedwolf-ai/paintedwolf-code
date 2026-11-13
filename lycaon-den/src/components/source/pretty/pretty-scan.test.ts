import { Text } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import { prettyPlanFor, prettyTargetFor } from "./pretty-recognition.ts";
import { PrettyScanner } from "./pretty-scan.ts";

function scan(doc: string, path: string, every: number): string[] {
  const text = Text.of(doc.split("\n"));
  const facts = new Map<number, boolean>();
  const out: string[] = [];
  const scanner = new PrettyScanner(
    {
      doc: text,
      plan: prettyPlanFor(text, prettyTargetFor(path)!),
      tabSize: 4,
      indentUnit: 2,
      facts: { get: (open) => facts.get(open), set: (open, _end, block) => void (facts.has(open) || facts.set(open, block)) },
    },
    { break: (pos) => out.push(`break ${pos}`), indent: (from, to, columns) => out.push(`line ${from}-${to} at ${columns}`) },
  );
  scanner.run(0, 0, (pos) => pos + every);
  return out;
}

const SAMPLES: Array<[string, string]> = [
  ['[{"a":"x/*y","b":[1,2,{"c":"\\"//"}]},' + '{"id":1,"tags":["a","b"]},'.repeat(40) + "{}]", "data.json"],
  ['{"a":1, /* note */ "b":[1,2], // tail\n"c":{"d":\'e\'}}', "config.jsonc"],
  ['const a=/x\\/y/g,b=`t${c+`u${d}`}v`;function f(){return /re[/]/.test(s)}' + "if(a){b()}else{c()};".repeat(20), "bundle.js"],
  [".a{b:url(//x/y.png);c:'d}'}/* x{ */.e{f:g}" + "@media (x){.h{i:j;k:l}}".repeat(20), "site.scss"],
  [`<div><!-- a > b --><p class="x>y">${"<b>t</b>".repeat(40)}</p><script>if(a<b){}</script></div>`, "page.html"],
  ["# t\n\n```json\n" + '{"a":[1,2,3],"b":{"c":"d"}}'.repeat(12) + "\n```\n", "notes.md"],
];

describe("pretty scanner", () => {
  it("lays out the same wherever scanning pauses", () => {
    for (const [doc, path] of SAMPLES) {
      const continuous = scan(doc, path, 1 << 30);
      expect(continuous.length, path).toBeGreaterThan(0);
      for (const every of [1, 2, 3, 7, 64]) {
        expect(scan(doc, path, every), `${path} paused every ${every}`).toEqual(continuous);
      }
    }
  });
});
