/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Overrides the API origin. Empty in production: the frontend is served beside the API. */
  readonly VITE_API_BASE?: string;
  /** Overrides the canvas edge cap. Only the scale benchmark sets it (bench/README.md). */
  readonly VITE_MAX_RENDERED_EDGES?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
