import { redirect } from "react-router";
import { clearToken, readToken, signInPath } from "./session";
import type { List, Problem } from "./types";

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId?: string;

  constructor(status: number, problem: Problem) {
    super(message(status, problem));
    this.name = "ApiError";
    this.status = status;
    this.code = problem.code ?? "unknown";
    this.requestId = problem.request_id;
  }
}

type RequestOptions = {
  method?: "GET" | "POST" | "PATCH" | "PUT" | "DELETE";
  body?: unknown;
  /** Required by the API for requests that move money; reuse it when retrying the same request. */
  idempotencyKey?: string;
  /** Overrides the stored key, for checking a key before saving it. */
  token?: string;
  signal?: AbortSignal;
};

/**
 * Calls the Astrum API on the same origin. A rejected key signs the user out and redirects to sign-in, so loaders
 * and actions never handle 401 themselves.
 */
export async function api<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const token = options.token ?? readToken();
  const headers = new Headers({ Accept: "application/json" });
  if (token) headers.set("Authorization", `Bearer ${token}`);
  if (options.idempotencyKey) headers.set("Idempotency-Key", options.idempotencyKey);
  if (options.body !== undefined) headers.set("Content-Type", "application/json");

  const response = await fetch(path, {
    method: options.method ?? "GET",
    headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    signal: options.signal,
    // The API never redirects; following one could land on a different endpoint than the one asked for.
    redirect: "error",
  });

  if (response.status === 401 && options.token === undefined) {
    clearToken();
    throw redirect(signInPath(location.pathname + location.search));
  }
  if (!response.ok) {
    throw new ApiError(response.status, await problem(response));
  }
  if (response.status === 204) {
    return undefined as T;
  }
  return (await response.json()) as T;
}

// Plain wording for the errors people meet in the interface; others use the API's own detail.
const messages: Record<string, string> = {
  account_exists: "An account with this code already exists in the ledger.",
  account_not_empty: "The account must have a zero balance and no holds before it can be closed.",
  balance_lock_failed: "A balance condition on one of the entries was not met, so nothing was posted.",
  account_not_open: "The account is frozen or closed, so money cannot move through it.",
  currency_exists: "This currency is already registered.",
  idempotency_key_in_use: "The same request is still being processed. Try again in a moment.",
  idempotency_key_reused: "This form was already submitted with different values. Reload the page and try again.",
  insufficient_funds: "The account does not have enough available balance.",
  lock_version_conflict: "The account changed after it was loaded. Reload and try again.",
  unbalanced_transaction: "Debits and credits must be equal in each currency.",
  unknown_currency: "That currency is not registered. Register it on the Currencies page first.",
  unknown_ledger: "That ledger does not exist.",
};

/** Plain wording for an error code and detail, also used for per-item errors in batch and bulk results. */
export function errorMessage(code: string | undefined, detail: string | undefined): string | null {
  const known = code && messages[code];
  if (known) return known;
  // Details start with the component that failed, such as "ledger: ", which means nothing to a person.
  const cleaned = detail?.replace(/^([a-z]+: )+/, "");
  if (!cleaned) return null;
  return cleaned.charAt(0).toUpperCase() + cleaned.slice(1) + (/[.!?]$/.test(cleaned) ? "" : ".");
}

function message(status: number, problem: Problem): string {
  return errorMessage(problem.code, problem.detail) ?? problem.title ?? `Request failed with status ${status}.`;
}

async function problem(response: Response): Promise<Problem> {
  try {
    return (await response.json()) as Problem;
  } catch {
    return { title: response.statusText };
  }
}

/** A fresh idempotency key; create it once per form so resubmitting the same form cannot apply it twice. */
export function newIdempotencyKey(): string {
  return crypto.randomUUID();
}

/**
 * Builds an API path with every interpolated value encoded, so an id taken from the URL cannot change which endpoint
 * is called: path`/v1/ledgers/${id}`.
 */
export function path(strings: TemplateStringsArray, ...values: (string | undefined)[]): string {
  return strings.reduce((out, part, i) => out + part + (i < values.length ? encodeURIComponent(values[i] ?? "") : ""), "");
}

/** Follows next_cursor until the list ends or max items are loaded, for pickers that need a whole collection. */
export async function fetchAll<T>(listPath: string, options: { signal?: AbortSignal; max?: number } = {}): Promise<T[]> {
  const max = options.max ?? 1000;
  const items: T[] = [];
  let cursor: string | null = null;
  do {
    const url = new URL(listPath, "http://astrum.invalid");
    url.searchParams.set("limit", "100");
    if (cursor) url.searchParams.set("cursor", cursor);
    const page: List<T> = await api<List<T>>(url.pathname + url.search, { signal: options.signal });
    items.push(...page.data);
    cursor = page.next_cursor;
  } while (cursor && items.length < max);
  return items.slice(0, max);
}
