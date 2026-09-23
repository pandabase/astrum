import { Form, redirect, useNavigation } from "react-router";
import { Button } from "~/components/ui/button";
import { Field, Input } from "~/components/ui/field";
import { ApiError } from "~/lib/api";
import { signIn } from "~/lib/auth";
import { title } from "~/lib/meta";
import { readToken, safeNext } from "~/lib/session";
import type { Route } from "./+types/sign-in";

export const meta: Route.MetaFunction = () => title("Sign in");

function next(request: Request) {
  return safeNext(new URL(request.url).searchParams.get("next"));
}

export async function clientLoader({ request }: Route.ClientLoaderArgs) {
  if (readToken()) throw redirect(next(request));
  return null;
}

export async function clientAction({ request }: Route.ClientActionArgs) {
  const token = String((await request.formData()).get("key") ?? "").trim();
  if (!token.startsWith("sk_")) {
    return { error: "API keys start with sk_." };
  }
  try {
    await signIn(token, request.signal);
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      return { error: "This key is invalid, expired or revoked." };
    }
    throw err;
  }
  throw redirect(next(request));
}

export default function SignIn({ actionData }: Route.ComponentProps) {
  const submitting = useNavigation().state === "submitting";
  return (
    <main className="grid min-h-dvh place-items-center p-6">
      <Form method="post" className="grid w-full max-w-sm gap-4">
        <h1 className="text-base font-semibold">Sign in to Astrum</h1>
        <Field
          label="API key"
          error={actionData?.error}
          hint={
            <>
              Create one with <code className="font-mono">astrum keys create -name &lt;name&gt;</code>. It is kept in
              this tab only.
            </>
          }
        >
          {(props) => (
            <Input
              {...props}
              name="key"
              type="password"
              autoComplete="off"
              spellCheck={false}
              required
              autoFocus
              className="font-mono"
            />
          )}
        </Field>
        <Button type="submit" variant="primary" disabled={submitting}>
          {submitting ? "Signing in…" : "Sign in"}
        </Button>
      </Form>
    </main>
  );
}

export { RouteError as ErrorBoundary } from "~/components/route-error";
