/** Reactive reads are re-evaluated; a narrowing made on one read does not hold for the next. */
export default [
  {
    name: "reactive-calls-are-not-asserted-non-null",
    message: "A reactive call can return null on the next read. Snapshot it once, or use the keyed Show callback.",
    files: ["src/**/*.{ts,tsx}"],
    syntax: ["TSNonNullExpression[expression.type='CallExpression']"],
    seed: { file: "src/components/TaskCard.tsx", code: `declare const task: () => { id: string } | undefined; export const id = () => task()!.id;` },
  },
  {
    name: "reactive-calls-are-not-cast-to-a-variant",
    message: "An Extract cast on a reactive call narrows nothing at run time. Use a kind-narrowing helper with the keyed Match callback.",
    files: ["src/**/*.{ts,tsx}"],
    syntax: ["TSAsExpression[expression.type='CallExpression'][typeAnnotation.typeName.name='Extract']"],
    seed: {
      file: "src/components/TaskCard.tsx",
      code: `type Item = { kind: "a" } | { kind: "b" }; declare const item: () => Item; export const a = () => item() as Extract<Item, { kind: "a" }>;`,
    },
  },
];
