/** Builds a path with a query string, leaving out empty values. */
export function withQuery(path: string, params: Record<string, string | number | null | undefined>): string {
  const query = new URLSearchParams();
  for (const [name, value] of Object.entries(params)) {
    if (value !== null && value !== undefined && value !== "") query.set(name, String(value));
  }
  const encoded = query.toString();
  return encoded ? `${path}?${encoded}` : path;
}

/** Reads the page cursor and the named filters from a page URL. */
export function searchParams<const K extends string>(request: Request, names: readonly K[]) {
  const search = new URL(request.url).searchParams;
  const filters = Object.fromEntries(names.map((name) => [name, search.get(name) ?? ""])) as Record<K, string>;
  return { cursor: search.get("cursor") ?? "", filters };
}
