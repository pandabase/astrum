import type { ReactNode } from "react";
import { Form, useLocation, useNavigation } from "react-router";
import { Button } from "./ui/button";
import { ButtonLink } from "./ui/link";

/**
 * A GET form whose fields become the page's query string, so filtered views can be shared and bookmarked. It
 * remounts on every new query so the fields always show the filters actually applied.
 */
export function FilterBar({ children, active, label }: { children: ReactNode; active: boolean; label: string }) {
  const { pathname, search } = useLocation();
  const navigation = useNavigation();
  const pending = navigation.state !== "idle" && navigation.location?.pathname === pathname;
  return (
    <Form key={search} aria-label={label} className="flex flex-wrap items-end gap-2">
      {children}
      <Button type="submit" disabled={pending}>{pending ? "Filtering…" : "Filter"}</Button>
      {active && <ButtonLink to={pathname}>Clear</ButtonLink>}
    </Form>
  );
}
