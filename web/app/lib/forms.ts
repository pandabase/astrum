import { ApiError } from "./api";
import { mergePatch, parseMetadata } from "./metadata";
import type { Metadata } from "./types";

export type FormResult = { error: string };

/**
 * Turns an API rejection of the submitted values into a message for the form. Anything else, such as a server
 * failure, is rethrown for the route's error boundary.
 */
export function formError(err: unknown): FormResult {
  if (err instanceof ApiError && [400, 403, 404, 409, 413, 422].includes(err.status)) {
    return { error: err.status === 403 ? "Your API key's role does not allow this." : err.message };
  }
  throw err;
}

export function text(form: FormData, name: string): string {
  const value = form.get(name);
  return typeof value === "string" ? value.trim() : "";
}

export type Descriptive = { name: string; description: string; metadata: Metadata };

/** Reads the name, description and metadata fields that ledgers and accounts share. */
export function readDescriptive(form: FormData): { ok: true; value: Descriptive } | { ok: false; error: string } {
  const metadata = parseMetadata(text(form, "metadata"));
  if (!metadata.ok) return metadata;
  return { ok: true, value: { name: text(form, "name"), description: text(form, "description"), metadata: metadata.value } };
}

/** The values an edit form was loaded with, carried in its hidden original field. */
export function readOriginal(form: FormData): Descriptive {
  return JSON.parse(text(form, "original") || "{}") as Descriptive;
}

/** The PATCH body that changes only what the form changed; metadata is sent as a merge patch. */
export function descriptivePatch(current: Descriptive, next: Descriptive) {
  const body: { name?: string; description?: string; metadata?: Metadata } = {};
  if (next.name !== current.name) body.name = next.name;
  if (next.description !== current.description) body.description = next.description;
  const metadata = mergePatch(current.metadata ?? {}, next.metadata);
  if (Object.keys(metadata).length > 0) body.metadata = metadata;
  return body;
}
