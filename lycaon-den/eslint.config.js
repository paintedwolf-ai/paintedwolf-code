import solid from "eslint-plugin-solid/configs/typescript";
import tailwind from "eslint-plugin-tailwindcss";
import tseslint from "typescript-eslint";

export default tseslint.config(
  {
    ignores: [
      "dist/**",
      "node_modules/**",
      "src-tauri/**",
      "src/api/mocks/**",
      "src/styling/fixtures/**",
      "playwright-report/**",
      "test-results/**",
    ],
  },
  ...tseslint.configs.recommended,
  solid,
  tailwind.configs.recommended,
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
    // Tests permit setup assertions and non-reactive fixtures.
    files: ["src/**/*.test.{ts,tsx}", "vitest.setup.ts", "eslint.config.js"],
    rules: {
      "@typescript-eslint/no-non-null-assertion": "off",
      "solid/reactivity": "off",
      "solid/no-destructure": "off",
    },
  },
);
