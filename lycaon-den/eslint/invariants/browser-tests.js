export default [
  {
    name: "browser-fixtures-load-without-vitest",
    files: ["e2e/**/*.{ts,tsx}", "src/test/client-fixture.ts", "src/test/local-generation-fixture.ts", "src/files/review/review-lens-fixtures.ts"],
    syntax: [
      "ImportDeclaration[source.value='vitest'][specifiers.length=0]",
      "ImportDeclaration[source.value='vitest'][importKind!='type'] > :matches(ImportDefaultSpecifier, ImportNamespaceSpecifier, ImportSpecifier[importKind!='type'])",
      "ExportAllDeclaration[source.value='vitest'][exportKind!='type']",
      "ExportNamedDeclaration[source.value='vitest'][exportKind!='type'] > ExportSpecifier[exportKind!='type']",
    ],
    message: "Load Vitest runtime helpers inside the Vitest environment guard so browser fixtures can initialize.",
    seed: { file: "src/test/client-fixture.ts", code: `import { vi } from "vitest"; export const probe = vi.importActual;` },
  },
  {
    name: "no-fixed-browser-waits",
    files: ["e2e/**/*.{ts,tsx}"],
    tests: true,
    syntax: [
      "CallExpression[callee.property.name='waitForTimeout']",
      "CallExpression[callee.property.value='waitForTimeout']",
    ],
    message: "Wait on the state or event the test needs.",
    seed: { file: "e2e/approval-grants-revoke.spec.ts", code: "await page.waitForTimeout(100);" },
  },
];
