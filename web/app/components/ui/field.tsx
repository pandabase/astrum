import { useId, type ComponentProps, type ReactNode } from "react";
import { cn } from "~/lib/cn";

const control = "w-full border border-line-strong bg-canvas px-2 text-ink placeholder:text-muted aria-invalid:border-danger";

export function Input({ className, ...props }: ComponentProps<"input">) {
  return <input className={cn(control, "h-8", className)} {...props} />;
}

export function Textarea({ className, ...props }: ComponentProps<"textarea">) {
  return <textarea className={cn(control, "min-h-24 py-1.5", className)} {...props} />;
}

export function Checkbox({ label, hint, ...props }: ComponentProps<"input"> & { label: string; hint?: ReactNode }) {
  return (
    <label className="flex items-start gap-2">
      <input type="checkbox" className="mt-0.5 size-4 accent-ink" {...props} />
      <span>
        <span className="font-medium">{label}</span>
        {hint && <span className="block text-muted">{hint}</span>}
      </span>
    </label>
  );
}

export function Select({ className, ...props }: ComponentProps<"select">) {
  return <select className={cn(control, "h-8", className)} {...props} />;
}

type FieldProps = {
  label: string;
  hint?: ReactNode;
  error?: string;
  children: (props: { id: string; "aria-invalid"?: true; "aria-describedby"?: string }) => ReactNode;
};

/** Labels a control and wires its hint and error for screen readers. */
export function Field({ label, hint, error, children }: FieldProps) {
  const id = useId();
  const noteId = `${id}-note`;
  const note = error ?? hint;
  return (
    <div className="grid gap-1">
      <label htmlFor={id} className="font-medium">
        {label}
      </label>
      {children({ id, "aria-invalid": error ? true : undefined, "aria-describedby": note ? noteId : undefined })}
      {note && (
        <p id={noteId} className={error ? "text-danger" : "text-muted"}>
          {note}
        </p>
      )}
    </div>
  );
}
