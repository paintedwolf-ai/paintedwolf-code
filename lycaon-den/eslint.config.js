import solid from "eslint-plugin-solid/configs/typescript";
import tailwind from "eslint-plugin-tailwindcss";
import tseslint from "typescript-eslint";
import laws from "./eslint/laws/index.js";
import den from "./eslint/plugin.js";

export default tseslint.config(
  {
    ignores: [
      "dist/**",
      "node_modules/**",
      "src-tauri/**",
      "src/api/mocks/**",
      "playwright-report/**",
      "test-results/**",
    ],
  },
  {
    files: ["src/**/*.{ts,tsx}", "shared/**/*.ts"],
    extends: [...tseslint.configs.recommended, solid],
  },
  {
    // Specs are held to the source laws only.
    files: ["e2e/**/*.ts"],
    languageOptions: { parser: tseslint.parser },
  },
  {
    // Type information exposes unhandled promises.
    files: ["src/**/*.{ts,tsx}", "shared/**/*.ts"],
    languageOptions: {
      parserOptions: {
        projectService: true,
        tsconfigRootDir: import.meta.dirname,
      },
    },
    rules: {
      "@typescript-eslint/no-floating-promises": "error",
      "@typescript-eslint/no-misused-promises": "error",
      "@typescript-eslint/await-thenable": "error",
      "@typescript-eslint/no-for-in-array": "error",
      "@typescript-eslint/no-array-delete": "error",
      "@typescript-eslint/no-implied-eval": "error",
      "@typescript-eslint/only-throw-error": "error",
      "@typescript-eslint/prefer-promise-reject-errors": "error",
      "@typescript-eslint/no-misused-spread": "error",
      "@typescript-eslint/related-getter-setter-pairs": "error",
      // The rule disagrees with the compiler about required DOM narrows.
      "@typescript-eslint/no-unnecessary-type-assertion": "off",
      "@typescript-eslint/no-duplicate-type-constituents": "error",
      "@typescript-eslint/no-redundant-type-constituents": "error",
    },
  },
  {
    files: ["src/**/*.{ts,tsx}"],
    extends: [tailwind.configs.recommended],
    settings: {
      tailwindcss: {
        cssConfigPath: "./src/tailwind.css",
        functions: ["cn"],
        parseKeyFunctions: ["cn"],
      },
    },
    rules: {
      // Domain CSS hooks use stable custom class names.
      "tailwindcss/no-custom-classname": "off",
      "tailwindcss/no-contradicting-classname": "error",
    },
  },
  {
    files: ["src/**/*.tsx"],
    rules: {
      // Component accessors may become null during teardown.
      "@typescript-eslint/no-non-null-assertion": "error",
    },
  },
  {
    files: ["src/**/*.{ts,tsx}"],
    rules: {
      "prefer-const": "error",

      // Preserve reactive property access.
      "solid/no-destructure": "error",
      "solid/no-react-deps": "error",
      "solid/jsx-no-duplicate-props": "error",
      // Stable callback references can trigger this rule.
      "solid/reactivity": "warn",
      // Early returns are safe before reactive state is created.
      "solid/components-return-once": "warn",

      "solid/event-handlers": "error",
      "solid/imports": "error",
      "solid/jsx-no-undef": "error",
      "solid/jsx-uses-vars": "error",
      "solid/no-innerhtml": "error",
      "solid/no-unknown-namespaces": "error",
      "solid/prefer-classlist": "error",
      "solid/prefer-for": "error",
      "solid/prefer-show": "error",
      "solid/self-closing-comp": "error",
      "solid/style-prop": "error",

      "@typescript-eslint/no-unused-vars": [
        "error",
        // Underscores mark intentionally unused parameters.
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_" },
      ],
      "@typescript-eslint/no-explicit-any": "off",
      // Bare accessor reads register subscriptions.
      "@typescript-eslint/no-unused-expressions": "off",
    },
  },
  {
    files: ["src/**/*.{ts,tsx}", "shared/**/*.ts", "e2e/**/*.{ts,tsx}"],
    plugins: { den },
    rules: {
      "den/source-laws": ["error", laws],
    },
  },
  {
    files: ["src/**/*.{ts,tsx}"],
    ignores: ["src/**/*.test.{ts,tsx}"],
    rules: {
      "den/no-computation-in-jsx-callback": "error",
      "den/no-http-error-status-branching": "error",
      "den/no-keyed-settings-draft": "error",
      "den/no-native-smooth-scroll": "error",
      "den/no-show-accessor-prop-leak": "error",
    },
  },
  {
    // Tests permit setup assertions and non-reactive fixtures.
    files: ["src/**/*.test.{ts,tsx}", "vitest.setup.ts", "eslint.config.js"],
    rules: {
      "@typescript-eslint/no-non-null-assertion": "off",
      "solid/reactivity": "off",
      "solid/no-destructure": "off",
    },
  },
);
