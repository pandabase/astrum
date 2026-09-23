/** Converts a datetime-local input value, which is in the browser's time zone, to an RFC 3339 time for the API. */
export function toApiTime(local: string): string | null {
  if (local.trim() === "") return null;
  const date = new Date(local);
  return Number.isNaN(date.getTime()) ? null : date.toISOString();
}

/** Formats an API time as a datetime-local input value in the browser's time zone. */
export function toLocalInput(iso: string | null | undefined): string {
  if (!iso) return "";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}
