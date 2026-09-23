import { isRouteErrorResponse, useRouteError } from "react-router";
import { ApiError } from "~/lib/api";
import { Notice } from "./ui/notice";

/** Explains a failed route in one line; pages re-export it as their ErrorBoundary so the shell stays in place. */
export function RouteError() {
  const error = useRouteError();
  const { title, detail, requestId } = describe(error);
  return (
    <div className="grid gap-3">
      <h1 className="text-base font-semibold">{title}</h1>
      <Notice tone="danger">
        {detail}
        {requestId && <span className="block font-mono text-xs text-muted">Request {requestId}</span>}
      </Notice>
    </div>
  );
}

function describe(error: unknown): { title: string; detail: string; requestId?: string } {
  if (error instanceof ApiError) {
    if (error.status === 404) return { title: "Not found", detail: error.message, requestId: error.requestId };
    if (error.status === 403) {
      return { title: "Not allowed", detail: "Your API key's role does not allow this.", requestId: error.requestId };
    }
    return { title: "Something went wrong", detail: error.message, requestId: error.requestId };
  }
  if (isRouteErrorResponse(error)) {
    return error.status === 404
      ? { title: "Not found", detail: "This page does not exist." }
      : { title: `Error ${error.status}`, detail: error.statusText || "The request failed." };
  }
  if (error instanceof TypeError) {
    return { title: "Cannot reach Astrum", detail: "Check your connection and that the server is running." };
  }
  return { title: "Something went wrong", detail: error instanceof Error ? error.message : "An unexpected error occurred." };
}
