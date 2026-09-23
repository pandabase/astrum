import { api, path } from "./api";
import { formError, text } from "./forms";
import { isId } from "./ids";

/** Handles the retry intent that DeliveriesTable submits. */
export async function retryDelivery(form: FormData, signal: AbortSignal) {
  const id = text(form, "delivery_id");
  if (!isId(id, "wd")) return { error: "Unknown delivery." };
  try {
    await api(path`/v1/webhook_deliveries/${id}/retry`, { method: "POST", signal });
    return null;
  } catch (err) {
    return formError(err);
  }
}
