/** Every event type the API emits, for filters and webhook subscriptions. */
export const eventTypes = [
  "transaction.created",
  "transaction.updated",
  "transaction.posted",
  "transaction.archived",
  "account.created",
  "account.updated",
  "hold.created",
  "hold.captured",
  "hold.voided",
  "hold.expired",
  "balance_monitor.triggered",
  "bulk_request.completed",
  "settlement.created",
] as const;

/** Prefix subscriptions such as transaction.* cover every type in a group. */
export const eventGroups = ["transaction.*", "account.*", "hold.*"] as const;
