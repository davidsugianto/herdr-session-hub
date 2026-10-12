import { signal } from "@preact/signals";
import type { AuthState } from "./types";

/** Own module so api.ts and store.ts do not import each other. Re-exported from store.ts. */
export const auth = signal<AuthState>("unknown");
