import js from "@eslint/js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";
import globals from "globals";

export default tseslint.config(
  { ignores: ["dist", "coverage", "node_modules"] },
  {
    files: ["src/**/*.{ts,tsx}"],
    extends: [
      js.configs.recommended,
      ...tseslint.configs.recommended,
      reactHooks.configs.flat["recommended-latest"],
    ],
    languageOptions: {
      globals: globals.browser,
    },
    rules: {
      // Vitest/test files legitimately use empty mocks and any-casts; keep
      // signal high in src, don't fight the test idiom.
      "@typescript-eslint/no-explicit-any": "warn",
      // Mock function signatures (e.g. `vi.fn(async (_input, _init) => ...)`)
      // must match the real signature they stand in for even when a param is
      // unused; leading-underscore is the standard "intentionally unused" and
      // is the least invasive way to keep signal on genuinely dead bindings.
      "@typescript-eslint/no-unused-vars": ["error", { argsIgnorePattern: "^_" }],
    },
  },
);
