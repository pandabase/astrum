# Astrum Web

The interface for Astrum, a financial kernel with a double-entry core.

## Design direction

Extremely minimal, basic, and sharp. Use Base UI as a reference for restrained, straightforward components. The interface should feel like a precise financial tool: quiet, utilitarian, and pleasant to use. The edgy feel should come from square geometry, crisp borders, and typography.

- Use neutral colors: black, white, and grays. Keep light and dark themes equally readable. Reserve color for meaningful states such as errors and warnings.
- Use square corners everywhere. No rounded buttons, inputs, cards, menus, badges, or containers.
- Prefer flat surfaces, thin borders, and whitespace. Avoid gradients, shadows, glass effects, textures, and decorative backgrounds.
- Keep typography simple, with a small number of sizes and weights. Use tabular numerals for financial data and monospace where useful for IDs.
- Keep layouts sparse and aligned. Prefer plain sections and tables over grids of cards. Show only information and controls that serve the current task.
- Make buttons, inputs, tabs, and menus look basic and consistent. Use short labels and small, functional icons only when they improve clarity.
- Avoid oversized headings, marketing copy, decorative illustrations, excessive badges, and unnecessary animation.
- Charts are optional. Add them only when they explain useful financial data; keep them flat, neutral, and easy to read, with minimal gridlines and clear labels.

## Interaction

- Make common actions obvious and require as few steps as practical.
- Preserve visible keyboard focus, readable contrast, and comfortable click targets despite the minimal styling.
- Provide clear loading, empty, success, and error states using concise text.
- Keep financial values easy to scan: align numeric columns and show currencies consistently.

## Implementation

- Use client-side rendering and client-side data fetching. Do not use server-side rendering.
- Treat the Base UI reference as a visual direction, not a requirement to add a dependency.
- Choose the simplest component and layout that meet the task. Do not add features or decoration just to fill space.

## Development

Run the API on `:8080`, then:

```sh
pnpm install
pnpm dev         # http://localhost:5173, proxies /v1 to ASTRUM_API (default http://localhost:8080)
pnpm test        # unit and route tests
pnpm typecheck
pnpm build       # writes build/client
```

In production the Go server serves `build/client` itself when `WEB_DIR` points at it, so the interface and API share one origin.

## Layout

| Path                                 | Holds                                                                                                                                                                                                                                   |
| ------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `app/routes.ts`                      | Every route. Pages live in `app/routes/`, one folder per resource with `index`, `new`, `detail` and `edit`.                                                                                                                             |
| `app/routes/app-layout.tsx`          | The signed-in shell. Its middleware redirects to sign-in and provides the key.                                                                                                                                                          |
| `app/nav.ts`                         | Sidebar groups; add a page here to show it, with `role: "admin"` to hide it from other keys.                                                                                                                                            |
| `app/lib/api.ts`                     | The only place that calls the API. A `401` signs out; other failures throw `ApiError` with a readable message. Build paths with ``path`/v1/ledgers/${id}` `` so ids are encoded, and use `fetchAll` for pickers that need a whole list. |
| `app/lib/ids.ts`                     | `routeId(params.x, "ldg")` turns a malformed id in the URL into a 404 before any request.                                                                                                                                               |
| `app/lib/accounts.ts`                | Looks up accounts by id, cached, so pages show codes and format amounts with the right exponent.                                                                                                                                        |
| `app/lib/forms.ts`, `idempotency.ts` | Reading form fields, turning API rejections into form errors, merge-patch updates, and the idempotency key each create form sends.                                                                                                      |
| `app/lib/transaction-form.ts`        | Turning the entries editor's rows into API entries, with per-currency totals.                                                                                                                                                           |
| `app/lib/`                           | Also session, amounts (`formatAmount`, `parseAmount`), times, metadata, event types and API types.                                                                                                                                      |
| `app/components/ui/`                 | Buttons, links, fields, tables, dialogs and notices. Build pages from these; pass `className` to adjust, and `cn` merges it over the base style.                                                                                        |
| `app/components/`                    | Larger pieces shared by pages: filter bar, confirm dialogs, transaction form, account and ledger pickers, balance and entry tables, details list, amounts, status text and pagination.                                                  |
| `tests/`                             | Every test, laid out like `app/`: `tests/lib` for the library, `tests/routes` for pages.                                                                                                                                                |

Pages load data in `clientLoader` and change it in `clientAction`, never in effects. Lists keep their cursor and filters in the URL. Actions on a detail page submit an `intent` through a fetcher, and `ActionError` shows what the API refused.

Create forms send an idempotency key that stays the same for double submits and changes once an attempt finishes, since the API remembers every outcome under its key. Edit forms send only the fields that changed. Each page re-exports `RouteError` as its `ErrorBoundary` so failures stay inside the shell.
