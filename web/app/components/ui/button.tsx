import type { ComponentProps } from "react";
import { cn } from "~/lib/cn";

type Variant = "primary" | "secondary" | "danger";

const variants: Record<Variant, string> = {
  primary: "border-ink bg-ink text-canvas hover:opacity-90",
  secondary: "border-line-strong bg-canvas text-ink hover:bg-sunken",
  danger: "border-danger bg-canvas text-danger hover:bg-sunken",
};

type ButtonProps = ComponentProps<"button"> & { variant?: Variant };

export function Button({ variant = "secondary", className, type = "button", ...props }: ButtonProps) {
  return (
    <button
      type={type}
      className={cn(
        "inline-flex min-h-9 items-center justify-center gap-2 border px-3 py-1.5 text-xs font-medium transition-colors motion-reduce:transition-none disabled:cursor-not-allowed disabled:opacity-50",
        variants[variant],
        className,
      )}
      {...props}
    />
  );
}
