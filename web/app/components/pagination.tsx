import { Link, useLocation, useNavigation } from "react-router";

/** Links to the first and next page of a cursor-paged list, keeping the page's other filters. */
export function Pagination({ nextCursor }: { nextCursor: string | null }) {
  const { pathname, search } = useLocation();
  const navigation = useNavigation();
  const pending = navigation.state !== "idle" && navigation.location?.pathname === pathname;
  const params = new URLSearchParams(search);
  const onFirstPage = !params.has("cursor");
  if (onFirstPage && !nextCursor) return null;

  const to = (cursor: string | null) => {
    const next = new URLSearchParams(params);
    if (cursor) next.set("cursor", cursor);
    else next.delete("cursor");
    const query = next.toString();
    return query ? `${pathname}?${query}` : pathname;
  };

  const link = "inline-flex min-h-9 items-center border border-line-strong px-3 py-1.5 text-xs hover:bg-sunken aria-disabled:opacity-50";
  return (
    <nav aria-label="Pages" className="flex items-center justify-end gap-2">
      <span role="status" className="mr-2 text-xs text-muted">{pending ? "Loading page…" : ""}</span>
      {!onFirstPage && (
        <Link to={to(null)} className={link} aria-disabled={pending} onClick={(event) => { if (pending) event.preventDefault(); }}>
          First page
        </Link>
      )}
      {nextCursor && (
        <Link to={to(nextCursor)} className={link} aria-disabled={pending} onClick={(event) => { if (pending) event.preventDefault(); }}>
          Next page
        </Link>
      )}
    </nav>
  );
}
