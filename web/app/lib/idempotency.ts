import { useState } from "react";
import { newIdempotencyKey } from "./api";

/**
 * The idempotency key for a form. It stays the same across double submits of one attempt and is replaced once the
 * attempt finishes: the API remembers each outcome under its key, so the next attempt from a page that stays open,
 * such as a corrected resubmission or another import, needs a key of its own.
 */
export function useIdempotencyKey(result: unknown): string {
  const [key, setKey] = useState(newIdempotencyKey);
  const [seen, setSeen] = useState(result);
  if (result !== seen) {
    setSeen(result);
    if (result !== undefined && result !== null) setKey(newIdempotencyKey());
  }
  return key;
}
