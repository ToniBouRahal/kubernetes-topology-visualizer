/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Overrides the API origin. Empty in production: the frontend is served beside the API. */
  readonly VITE_API_BASE?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
