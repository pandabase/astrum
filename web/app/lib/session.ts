import { createContext } from "react-router";
import type { ApiKey, Role } from "./types";

// Session storage keeps the key to this tab and drops it when the tab closes.
const storageKey = "astrum.key";

export function readToken(): string | null {
  return sessionStorage.getItem(storageKey);
}

export function saveToken(token: string) {
  sessionStorage.setItem(storageKey, token);
}

export function clearToken() {
  sessionStorage.removeItem(storageKey);
}

/** The key the interface is signed in with, set by the app layout's middleware. */
export const sessionContext = createContext<ApiKey>();

export function canWrite(role: Role) {
  return role === "admin" || role === "write";
}

export function isAdmin(role: Role) {
  return role === "admin";
}

/** Only same-origin paths are followed after sign-in, so a crafted link cannot send the user elsewhere. */
export function safeNext(next: string | null): string {
  if (!next || !next.startsWith("/") || next.startsWith("//") || next.startsWith("/\\")) {
    return "/";
  }
  return next;
}

export function signInPath(next: string): string {
  const target = safeNext(next);
  return target === "/" ? "/sign-in" : `/sign-in?next=${encodeURIComponent(target)}`;
}
