import { createContext, useContext, type JSX } from "solid-js";
import type { NoticeReporter } from "./notice-store.ts";

/** A scoped reporter captured when the operation starts. */
const NoticeReporterContext = createContext<NoticeReporter>();

export function NoticeReporterProvider(props: {
  reporter: NoticeReporter;
  children: JSX.Element;
}): JSX.Element {
  return (
    <NoticeReporterContext.Provider value={props.reporter}>
      {props.children}
    </NoticeReporterContext.Provider>
  );
}

/** The enclosing reporter; throws when there is none. The provider is installed once per stage. */
export function useNotices(): NoticeReporter {
  const reporter = useContext(NoticeReporterContext);
  if (!reporter) {
    throw new Error("useNotices requires a NoticeReporterProvider ancestor");
  }
  return reporter;
}

/** Discards reports. Shared so the optional path allocates nothing per call. */
const NO_REPORTER: NoticeReporter = Object.freeze({
  reportError: () => {},
  publish: () => {},
});

/**
 * The enclosing reporter, or a no-op when there is none. For components rendered
 * both under and outside a provider (the transcript row in the worker drawer and a11y harnesses).
 */
export function useNoticesOptional(): NoticeReporter {
  return useContext(NoticeReporterContext) ?? NO_REPORTER;
}
