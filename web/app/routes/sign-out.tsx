import { redirect } from "react-router";
import { signOut } from "~/lib/auth";

// Signing out changes state, so only a POST does it; visiting the URL just goes home.
export function clientLoader() {
  return redirect("/");
}

export function clientAction() {
  signOut();
  return redirect("/sign-in");
}
