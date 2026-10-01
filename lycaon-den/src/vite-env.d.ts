/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly TAURI_ENV_PLATFORM?: string;
  readonly VITE_LYCAON_API_TOKEN?: string;
  readonly VITE_LYCAON_API_URL?: string;
  readonly VITE_LYCAON_PROXY?: string;
  /** Set by `./task den:dev` — enables scroll/reveal debug + JSONL capture. */
  readonly VITE_DEN_SCROLL_DEBUG?: string;
  /** Absolute path echoed at den:dev startup; used by Vite middleware. */
  readonly VITE_DEN_SCROLL_DEBUG_FILE?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
