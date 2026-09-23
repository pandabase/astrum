import { data } from "react-router";

export type IdPrefix = "ldg" | "acct" | "txn" | "hold" | "sched" | "stmt" | "cat" | "bm" | "blk" | "stl" | "evt" | "we" | "wd" | "key";

/** TypeIDs: a prefix, an underscore and 26 base32 characters whose first is at most 7. */
export function isId(value: string | undefined, prefix: IdPrefix): value is string {
  return value !== undefined && new RegExp(`^${prefix}_[0-7][0-9a-hjkmnp-tv-z]{25}$`).test(value);
}

/** Returns a route parameter that must be an id of one kind; anything else is a 404 before the API is called. */
export function routeId(value: string | undefined, prefix: IdPrefix): string {
  if (!isId(value, prefix)) throw data(null, { status: 404 });
  return value;
}
