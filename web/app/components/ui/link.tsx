import { Link, type LinkProps } from "react-router";
import { cn } from "~/lib/cn";

const buttonLink = {
  primary: "border-ink bg-ink text-canvas hover:opacity-90",
  secondary: "border-line-strong bg-canvas hover:bg-sunken",
};

/** A link styled as a button, for actions that open another page. */
export function ButtonLink({ variant = "secondary", className, ...props }: LinkProps & { variant?: keyof typeof buttonLink }) {
  return <Link className={cn("inline-flex min-h-9 items-center border px-3 py-1.5 text-xs font-medium transition-colors motion-reduce:transition-none", buttonLink[variant], className)} {...props} />;
}

export function TextLink({ className, ...props }: LinkProps) {
  return <Link className={cn("underline decoration-line-strong underline-offset-2 hover:decoration-ink", className)} {...props} />;
}
