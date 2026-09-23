import { useFetcher } from "react-router";
import { PageHeader } from "~/components/page";
import { Button } from "~/components/ui/button";
import { Notice } from "~/components/ui/notice";
import { api } from "~/lib/api";
import { formError } from "~/lib/forms";
import { title } from "~/lib/meta";
import type { IntegrityReport } from "~/lib/types";
import type { Route } from "./+types/integrity";

export const meta: Route.MetaFunction = () => title("Integrity");

// The check rereads every balance and the whole seal chain, so it runs only when asked, not on page load.
export async function clientAction({ request }: Route.ClientActionArgs) {
  try {
    return { error: null, report: await api<IntegrityReport>("/v1/integrity", { signal: request.signal }) };
  } catch (err) {
    return { ...formError(err), report: null };
  }
}

export default function Integrity() {
  const fetcher = useFetcher<typeof clientAction>();
  const report = fetcher.data?.report;
  const running = fetcher.state !== "idle";
  return (
    <div className="grid max-w-3xl gap-6">
      <PageHeader title="Integrity" />
      <p className="text-muted">
        Recomputes every account balance from the journal and verifies the tamper-evident seal chain. On a large ledger this takes a while.
      </p>
      <fetcher.Form method="post">
        <Button type="submit" variant="primary" disabled={running}>
          {running ? "Checking…" : "Run check"}
        </Button>
      </fetcher.Form>
      {fetcher.data?.error && <Notice tone="danger">{fetcher.data.error}</Notice>}
      {report && !running && (
        <div className="grid gap-3">
          {report.ok ? <Notice>Every balance matches the journal and the seal chain is intact.</Notice> : <Notice tone="danger">{report.issues.length} problem(s) found.</Notice>}
          {report.issues.length > 0 && (
            <ul className="grid gap-1 border border-danger p-3 font-mono text-xs text-danger">
              {report.issues.map((issue, i) => (
                <li key={i}>{issue}</li>
              ))}
            </ul>
          )}
          <p className="text-muted">
            Chain head <code className="font-mono text-xs">{report.chain_head}</code>
          </p>
        </div>
      )}
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
