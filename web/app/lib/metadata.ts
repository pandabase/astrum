import type { Metadata } from "./types";

type Parsed = { ok: true; value: Metadata } | { ok: false; error: string };

/** Parses the metadata field of a form: blank is an empty object, anything else must be a JSON object. */
export function parseMetadata(text: string): Parsed {
  if (text.trim() === "") return { ok: true, value: {} };
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    return { ok: false, error: "Metadata must be valid JSON." };
  }
  if (!isObject(value)) return { ok: false, error: "Metadata must be a JSON object, such as {\"team\": \"payments\"}." };
  return { ok: true, value };
}

export function formatMetadata(metadata: Metadata | null | undefined): string {
  return metadata && Object.keys(metadata).length > 0 ? JSON.stringify(metadata, null, 2) : "";
}

/**
 * Returns the RFC 7396 merge patch that turns before into after: removed keys become null and nested objects are
 * diffed, since the API merges rather than replaces them.
 */
export function mergePatch(before: Metadata, after: Metadata): Metadata {
  const patch: Metadata = {};
  for (const name of Object.keys(before)) {
    if (!(name in after)) patch[name] = null;
  }
  for (const [name, next] of Object.entries(after)) {
    const prev = before[name];
    if (isObject(prev) && isObject(next)) {
      const nested = mergePatch(prev, next);
      if (Object.keys(nested).length > 0) patch[name] = nested;
    } else if (JSON.stringify(prev) !== JSON.stringify(next)) {
      patch[name] = next;
    }
  }
  return patch;
}

function isObject(value: unknown): value is Metadata {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
