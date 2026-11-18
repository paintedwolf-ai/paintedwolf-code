const JUMP_BEHAVIORS = new Set(["instant", "auto"]);
const NATIVE_SCROLL_METHODS = new Set(["scroll", "scrollTo", "scrollBy", "scrollIntoView", "scrollIntoViewIfNeeded"]);

function keyName(property) {
  return property.key.type === "Identifier" ? property.key.name : property.key.value;
}

/**
 * Coordinate pairs take no options; a lone options bag names an animated behavior or hides its contents.
 * `scroll` is also a common non-DOM method name, so only its literal options count.
 */
function mayRequestBehavior(method, args) {
  const [options] = args;
  if (args.length !== 1) return false;
  if (options.type === "Literal" && typeof options.value === "boolean") return false;
  if (options.type !== "ObjectExpression") return method !== "scroll";
  return options.properties.some((property) => {
    if (property.type === "SpreadElement") return true;
    if (property.computed || keyName(property) !== "behavior") return false;
    if (property.shorthand) return true;
    // A jump is safe to request by name; anything else may animate.
    return !(property.value.type === "Literal" && JUMP_BEHAVIORS.has(property.value.value));
  });
}

export default {
  meta: {
    type: "problem",
    docs: { description: "Native smooth scrolling is never requested." },
    schema: [],
    messages: {
      // WebKit animates it on the scrolling thread; a scroll write during it
      // leaves the page painted at one offset and hit tested at another.
      smooth: "A native scroll here may animate. Glide with ScrollportMotion.revealOffset or glideScrollLeft.",
    },
  },
  create(context) {
    const text = (node) => context.sourceCode.getText(node);
    const setsSmooth = (target, value) => /\bscrollBehavior\b/.test(text(target)) && /["'`]smooth["'`]/.test(text(value));
    return {
      CallExpression(node) {
        const callee = node.callee;
        if (callee.type !== "MemberExpression" || callee.computed) return;
        if (NATIVE_SCROLL_METHODS.has(callee.property.name) && mayRequestBehavior(callee.property.name, node.arguments)) {
          context.report({ node, messageId: "smooth" });
        }
      },
      Property(node) {
        if (node.parent.type === "ObjectExpression" && !node.shorthand && setsSmooth(node.key, node.value)) {
          context.report({ node, messageId: "smooth" });
        }
      },
      AssignmentExpression(node) {
        if (node.operator === "=" && setsSmooth(node.left, node.right)) context.report({ node, messageId: "smooth" });
      },
    };
  },
};

const ANIMATED = [
  `declare const viewport: HTMLElement; viewport.scrollTo({ top: 4, behavior: "smooth" });`,
  `declare const list: HTMLElement; declare const reduced: boolean; list.scrollTo({ top: 4, behavior: reduced ? "auto" : "smooth" });`,
  `declare const scroller: HTMLElement | undefined; declare const options: ScrollToOptions; scroller?.scrollTo({ top: 4, behavior: options.behavior });`,
  `declare const scrollport: () => HTMLElement; declare const behavior: ScrollBehavior; scrollport().scrollTo({ top: 4, behavior });`,
  `declare const tab: HTMLElement; tab.scrollIntoView({ inline: "nearest", block: "nearest", behavior: "smooth" });`,
  `declare const element: HTMLElement; element.scrollTo({ left: 4, behavior: "smooth" });`,
  `declare const element: HTMLElement; declare const options: ScrollToOptions; element.scrollTo(options);`,
  `declare const element: HTMLElement; declare const options: ScrollToOptions; element.scrollBy({ ...options, top: 4 });`,
  `declare const element: HTMLElement; element.scroll({ top: 4, behavior: "smooth" });`,
  `declare const viewport: HTMLElement; viewport.style.scrollBehavior = "smooth";`,
];

const JUMPS = [
  `declare const viewport: HTMLElement; viewport.scrollTo({ top: 40 });`,
  `declare const viewport: HTMLElement; viewport.scrollBy(0, 4);`,
  `declare const viewport: HTMLElement; viewport.scrollTo(0, 4);`,
  `declare const element: HTMLElement; element.scrollIntoView({ block: "nearest", inline: "nearest" });`,
  `declare const element: HTMLElement; element.scrollIntoView(false);`,
  `declare const element: HTMLElement; element.scrollIntoView({ block: "center", behavior: "instant" });`,
  `declare const element: HTMLElement; element.scrollIntoView();`,
  `declare const motion: { commit(top: number, reason: string): void }; motion.commit(40, "reveal");`,
  `declare const split: { scroll(editor: object): void } | undefined; split?.scroll({});`,
  `declare const virtualizer: { scrollToOffset(offset: number, options: object): void }; virtualizer.scrollToOffset(4, { behavior: "smooth" });`,
];

export const cases = [
  ...ANIMATED.map((code) => ({ code, errors: 1 })),
  ...JUMPS.map((code) => ({ code, errors: 0 })),
].map((entry) => ({ ...entry, file: "src/platform/scrolling/scrollport-motion.ts" }));
