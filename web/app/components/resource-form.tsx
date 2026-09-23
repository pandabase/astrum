import type { ReactNode } from "react";
import { Form, useNavigation } from "react-router";
import { formatMetadata } from "~/lib/metadata";
import type { Metadata } from "~/lib/types";
import { Button } from "./ui/button";
import { Field, Input, Textarea } from "./ui/field";
import { ButtonLink } from "./ui/link";
import { Notice } from "./ui/notice";

type ResourceFormProps = {
  error?: string;
  submitLabel: string;
  cancelTo: string;
  /** Sent as a hidden field so resubmitting the same form replays instead of creating twice. */
  idempotencyKey?: string;
  children: ReactNode;
};

/** A create or edit form: fields, one error message and the submit and cancel actions. */
export function ResourceForm({ error, submitLabel, cancelTo, idempotencyKey, children }: ResourceFormProps) {
  const submitting = useNavigation().state === "submitting";
  return (
    <Form method="post" className="grid max-w-xl gap-4">
      {idempotencyKey && <input type="hidden" name="idempotency_key" value={idempotencyKey} />}
      {error && <Notice tone="danger">{error}</Notice>}
      {children}
      <div className="flex gap-2">
        <Button type="submit" variant="primary" disabled={submitting}>
          {submitting ? "Saving…" : submitLabel}
        </Button>
        <ButtonLink to={cancelTo}>Cancel</ButtonLink>
      </div>
    </Form>
  );
}

type DescriptiveFieldsProps = {
  defaults?: { name?: string; description?: string; metadata?: Metadata };
  nameRequired?: boolean;
};

/**
 * Name, description and metadata, which ledgers and accounts share. When editing, the loaded values travel with the
 * form so the update changes only what the user changed.
 */
export function DescriptiveFields({ defaults, nameRequired }: DescriptiveFieldsProps) {
  return (
    <>
      {defaults && (
        <input
          type="hidden"
          name="original"
          value={JSON.stringify({ name: defaults.name ?? "", description: defaults.description ?? "", metadata: defaults.metadata ?? {} })}
        />
      )}
      <Field label="Name">
        {(props) => <Input {...props} name="name" defaultValue={defaults?.name} required={nameRequired} maxLength={255} />}
      </Field>
      <Field label="Description">
        {(props) => <Textarea {...props} name="description" defaultValue={defaults?.description} className="min-h-16" />}
      </Field>
      <Field label="Metadata" hint='A JSON object, such as {"team": "payments"}. Leave blank for none.'>
        {(props) => (
          <Textarea
            {...props}
            name="metadata"
            defaultValue={formatMetadata(defaults?.metadata)}
            spellCheck={false}
            className="font-mono text-xs"
          />
        )}
      </Field>
    </>
  );
}
