import type { MonitorField, MonitorOperator } from "./types";

export const monitorFields: Record<MonitorField, string> = {
  available_balance_amount: "available",
  pending_balance_amount: "pending",
  posted_balance_amount: "posted",
};

export const monitorOperators: Record<MonitorOperator, string> = {
  lt: "<",
  lte: "≤",
  eq: "=",
  not_eq: "≠",
  gte: "≥",
  gt: ">",
};
