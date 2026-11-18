import hostAuthority from "./host-authority.js";
import nativeChrome from "./native-chrome.js";
import platformBoundaries from "./platform-boundaries.js";
import reactivity from "./reactivity.js";
import shellLayout from "./shell-layout.js";

/**
 * Source laws: bans the compiler cannot express, each scoped to the files it
 * governs. A law names the globs it governs (`files`, less `ignores`), the
 * selectors (`syntax`) and modules (`imports`) it bans there, and a `seed`
 * that violates it. Test files are governed only when `tests` is true.
 */
export default [...hostAuthority, ...platformBoundaries, ...shellLayout, ...nativeChrome, ...reactivity];
