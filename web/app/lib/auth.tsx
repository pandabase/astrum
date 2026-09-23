import { createContext, useContext } from "react";
import { redirect } from "react-router";
import { api } from "./api";
import { clearToken, readToken, saveToken, signInPath } from "./session";
import type { ApiKey } from "./types";

// A key's role never changes, so it is looked up once per key rather than on every navigation.
let current: { token: string; key: ApiKey } | undefined;

/** Returns the signed-in key, or redirects to sign-in and back to request's page afterwards. */
export async function requireKey(request: Request): Promise<ApiKey> {
  const token = readToken();
  if (!token) {
    const url = new URL(request.url);
    throw redirect(signInPath(url.pathname + url.search));
  }
  if (current?.token !== token) {
    current = { token, key: await api<ApiKey>("/v1/me", { signal: request.signal }) };
  }
  return current.key;
}

/** Checks token against the API and keeps it only if the API accepts it. */
export async function signIn(token: string, signal?: AbortSignal): Promise<ApiKey> {
  const key = await api<ApiKey>("/v1/me", { token, signal });
  saveToken(token);
  current = { token, key };
  return key;
}

export function signOut() {
  clearToken();
  current = undefined;
}

const KeyContext = createContext<ApiKey | null>(null);

export const KeyProvider = KeyContext.Provider;

/** The signed-in key, for components under the app layout. */
export function useApiKey(): ApiKey {
  const key = useContext(KeyContext);
  if (!key) throw new Error("useApiKey must be used under the app layout");
  return key;
}
