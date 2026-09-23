import { data } from "react-router";

export function clientLoader() {
  throw data(null, { status: 404 });
}

export default function NotFound() {
  return null;
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
