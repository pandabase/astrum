import { type RouteConfig, index, layout, route } from "@react-router/dev/routes";

export default [
  route("sign-in", "routes/sign-in.tsx"),
  route("sign-out", "routes/sign-out.tsx"),
  layout("routes/app-layout.tsx", [
    index("routes/overview.tsx"),

    route("ledgers", "routes/ledgers/index.tsx"),
    route("ledgers/new", "routes/ledgers/new.tsx"),
    route("ledgers/:ledgerId", "routes/ledgers/detail.tsx"),
    route("ledgers/:ledgerId/edit", "routes/ledgers/edit.tsx"),
    route("ledgers/:ledgerId/accounts/new", "routes/accounts/new.tsx"),
    route("accounts/:accountId", "routes/accounts/detail.tsx"),
    route("accounts/:accountId/edit", "routes/accounts/edit.tsx"),
    route("currencies", "routes/currencies.tsx"),

    route("transactions", "routes/transactions/index.tsx"),
    route("transactions/new", "routes/transactions/new.tsx"),
    route("transactions/import", "routes/transactions/import.tsx"),
    route("transactions/:transactionId", "routes/transactions/detail.tsx"),
    route("transactions/:transactionId/edit", "routes/transactions/edit.tsx"),
    route("bulk/:bulkId", "routes/bulk/detail.tsx"),
    route("entries", "routes/entries/index.tsx"),
    route("holds", "routes/holds/index.tsx"),
    route("holds/new", "routes/holds/new.tsx"),
    route("holds/:holdId", "routes/holds/detail.tsx"),
    route("scheduled", "routes/scheduled/index.tsx"),
    route("scheduled/new", "routes/scheduled/new.tsx"),
    route("scheduled/:scheduleId", "routes/scheduled/detail.tsx"),

    route("statements", "routes/statements/index.tsx"),
    route("statements/new", "routes/statements/new.tsx"),
    route("statements/:statementId", "routes/statements/detail.tsx"),
    route("categories", "routes/categories/index.tsx"),
    route("categories/new", "routes/categories/new.tsx"),
    route("categories/:categoryId", "routes/categories/detail.tsx"),
    route("categories/:categoryId/edit", "routes/categories/edit.tsx"),
    route("settlements", "routes/settlements/index.tsx"),
    route("settlements/new", "routes/settlements/new.tsx"),
    route("settlements/:settlementId", "routes/settlements/detail.tsx"),

    route("monitors", "routes/monitors/index.tsx"),
    route("monitors/new", "routes/monitors/new.tsx"),
    route("monitors/:monitorId", "routes/monitors/detail.tsx"),
    route("events", "routes/events/index.tsx"),
    route("events/:eventId", "routes/events/detail.tsx"),
    route("webhooks", "routes/webhooks/index.tsx"),
    route("webhooks/new", "routes/webhooks/new.tsx"),
    route("webhooks/:endpointId", "routes/webhooks/detail.tsx"),
    route("integrity", "routes/integrity.tsx"),

    route("api-keys", "routes/api-keys.tsx"),

    route("*", "routes/not-found.tsx"),
  ]),
] satisfies RouteConfig;
