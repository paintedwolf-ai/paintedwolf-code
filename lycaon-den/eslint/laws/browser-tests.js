export default [
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
