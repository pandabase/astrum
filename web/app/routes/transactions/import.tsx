import { useState } from "react";
import { Form, redirect, useNavigation } from "react-router";
import { PageHeader, Section } from "~/components/page";
import { IdLink } from "~/components/refs";
import { Button } from "~/components/ui/button";
import { Checkbox, Field, Textarea } from "~/components/ui/field";
import { Notice } from "~/components/ui/notice";
import { Table, Td, Th } from "~/components/ui/table";
import { api, errorMessage } from "~/lib/api";
import { formError, text } from "~/lib/forms";
import { parseImport } from "~/lib/import";
import { useIdempotencyKey } from "~/lib/idempotency";
import { title } from "~/lib/meta";
import type { BatchResponse, BulkRequest } from "~/lib/types";
import type { Route } from "./+types/import";

export const meta: Route.MetaFunction = () => title("Import transactions");

const limits = { batch: 1_000, bulk: 10_000 } as const;

export async function clientAction({ request }: Route.ClientActionArgs) {
  const form = await request.formData();
  const mode = text(form, "mode") === "bulk" ? "bulk" : "batch";
  const file = form.get("file");
  const source = file instanceof File && file.size > 0 ? await file.text() : String(form.get("json") ?? "");
  const parsed = parseImport(source, limits[mode]);
  if (!parsed.ok) return { error: parsed.error, batch: null };
  const idempotencyKey = text(form, "idempotency_key");
  try {
    if (mode === "bulk") {
      const bulk = await api<BulkRequest>("/v1/bulk_requests", {
        method: "POST",
        body: { transactions: parsed.transactions },
        idempotencyKey,
        signal: request.signal,
      });
      throw redirect(`/bulk/${bulk.id}`);
    }
    const batch = await api<BatchResponse>("/v1/transactions/batch", {
      method: "POST",
      body: { transactions: parsed.transactions, atomic: form.get("atomic") === "on" },
      idempotencyKey,
      signal: request.signal,
    });
    return { error: null, batch };
  } catch (err) {
    if (err instanceof Response) throw err;
    return { ...formError(err), batch: null };
  }
}

const example = `{"transactions": [
  {"description": "Top-up", "entries": [
    {"account_id": "acct_…", "side": "debit", "amount": "5000"},
    {"account_id": "acct_…", "side": "credit", "amount": "5000"}]}
]}`;

export default function ImportTransactions({ actionData }: Route.ComponentProps) {
  // A new key per page load: resubmitting the same import replays it instead of posting it twice.
  const idempotencyKey = useIdempotencyKey(actionData);
  const [mode, setMode] = useState<"batch" | "bulk">("batch");
  const submitting = useNavigation().state === "submitting";
  const batch = actionData?.batch;
  const succeeded = batch?.results.filter((r) => r.transaction).length ?? 0;

  return (
    <div className="grid gap-8">
      <PageHeader title="Import transactions" />
      <Form method="post" encType="multipart/form-data" className="grid max-w-3xl gap-5">
        <input type="hidden" name="idempotency_key" value={idempotencyKey} />
        {actionData?.error && <Notice tone="danger">{actionData.error}</Notice>}
        <p className="text-muted">
          Transactions use the same JSON as <code className="font-mono">POST /v1/transactions</code>: account IDs and amounts in
          minor units, such as <code className="font-mono">"5000"</code> for USD 50.00.
        </p>
        <fieldset className="grid gap-2">
          <legend className="mb-1 font-medium">Mode</legend>
          <label className="flex items-start gap-2">
            <input type="radio" name="mode" value="batch" checked={mode === "batch"} onChange={() => setMode("batch")} className="mt-0.5 accent-ink" />
            <span>
              Batch <span className="text-muted">— up to 1,000, posted now. Results show here.</span>
            </span>
          </label>
          <label className="flex items-start gap-2">
            <input type="radio" name="mode" value="bulk" checked={mode === "bulk"} onChange={() => setMode("bulk")} className="mt-0.5 accent-ink" />
            <span>
              Bulk <span className="text-muted">— up to 10,000, posted in the background. Each succeeds or fails on its own.</span>
            </span>
          </label>
        </fieldset>
        {mode === "batch" && (
          <Checkbox name="atomic" defaultChecked label="All or nothing" hint="If any transaction fails, none are posted." />
        )}
        <Field label="File" hint="A .json file. It is used instead of the text below when chosen.">
          {(props) => <input {...props} name="file" type="file" accept="application/json,.json" className="text-sm" />}
        </Field>
        <Field label="Or paste JSON">
          {(props) => <Textarea {...props} name="json" placeholder={example} spellCheck={false} className="min-h-48 font-mono text-xs" />}
        </Field>
        <div>
          <Button type="submit" variant="primary" disabled={submitting}>
            {submitting ? "Importing…" : "Import"}
          </Button>
        </div>
      </Form>

      {batch && (
        <Section title={`Batch results: ${succeeded} posted, ${batch.results.length - succeeded} failed`}>
          <Table>
            <thead>
              <tr>
                <Th numeric>#</Th>
                <Th>Result</Th>
              </tr>
            </thead>
            <tbody>
              {batch.results.map((result, i) => (
                <tr key={i}>
                  <Td numeric>{i}</Td>
                  <Td>
                    {result.transaction ? (
                      <IdLink id={result.transaction.id} />
                    ) : (
                      <span className="text-danger">{errorMessage(result.error?.code, result.error?.detail) ?? result.error?.code}</span>
                    )}
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
        </Section>
      )}
    </div>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
