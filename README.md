<p align="center"><img src="docs/brand/logo.svg" width="96" alt="NetShare Hub logo"></p>

<h1 align="center">NetShare Hub</h1>

Simple network file sharing for Proxmox LXC and any Linux. See [docs/PLAN.md](docs/PLAN.md) for the full design and [docs/implementation](docs/implementation/README.md) for the build order.

This repo currently holds the **Phase 0 framework**: the API and UI foundations that every feature is built on, plus two reference features (auth and users) that show how to use them.

## Run it

Requirements: Go 1.25+, Node 22+, pnpm.

```bash
pnpm --dir web install
```

Development (two terminals; the UI at http://localhost:5173 proxies API calls to the Go server):

```bash
pnpm dev:api
```

```bash
pnpm dev:web
```

Single binary with the UI embedded (output in `bin/`):

```bash
pnpm build
```

Other tasks:

| Command | What it does |
|---|---|
| `pnpm test` | Go tests, TypeScript type check, lint (including the architecture rules) |
| `pnpm gen:api` | Regenerates the OpenAPI spec and the TypeScript API types from the Go code |

On first start the app asks you to create the admin account.

## Linux / Samba integration test environment

Integration tests (`-tags integration`) require Linux user management tools (`useradd`, `smbpasswd`) and a running Samba (`smbd`) daemon. You can run them inside the Debian + Samba test container defined in [deploy/test/Dockerfile](deploy/test/Dockerfile) and [compose.test.yml](compose.test.yml):

```bash
# Run integration tests against real Samba
docker compose -f compose.test.yml run --rm test

# Or open an interactive shell with smbd running (exposes SMB on 445 and API on 8080)
docker compose -f compose.test.yml run --rm --service-ports test bash
```

## How the backend is organized

```
cmd/netshare-hub/        CLI entry point (serve, openapi, version)
internal/
  app/                   wiring: the only place that knows every module
  platform/              shared machinery, no business rules
    errs/                the one error type services return
    httpx/               routes, access control, CSRF, problem+json, middleware
    validate/            business-rule validation (same output shape as tag validation)
    page/                the list envelope and pagination
    rbac/                permissions and roles (one table)
    config/ db/ logger/ password/ ratelimit/
  modules/<name>/        one folder per feature
    model.go             data types
    errors.go            this module's error codes
    repo.go              SQL only
    service.go           business rules only
    handler.go           HTTP only: request/response types + route table
  db/migrations/         numbered .sql files, applied at startup
web/                     the React app (embedded into the binary)
```

### Add an endpoint

1. Declare the request/response types in the module's `handler.go`. Validation rules are struct tags (`minLength:"2"`).
2. Add one `httpx.Register` entry. `Access` is required, so a route can't be left open by accident:
   `httpx.Public()`, `httpx.Authenticated()` or `httpx.Require(rbac.SomePermission)`.
3. Write the handler: call the service, return the result or the error. Errors become problem+json automatically.
4. Run `pnpm gen:api`. The frontend now has the types.

### Common changes

| I want to… | Edit |
|---|---|
| Add a permission, or change what a role can do | `internal/platform/rbac/rbac.go` |
| Reword a validation message | `internal/platform/httpx/messages.go` |
| Add a business rule ("username is taken") | the module's `service.go`, using `validate.New()` |
| Add an error code | the module's `errors.go` |
| Make a list paginated | embed `page.Query` in the input, return `page.Paged(...)` |
| Make a list unpaginated | return `page.All(items)` (same envelope, no `page` block) |
| Change page size limits or sortable fields for one list | that handler's `page.Options` |
| Change them for the whole API | `page.Defaults` in `internal/platform/page/page.go` |
| Change the schema | add a new file in `internal/db/migrations/` |

## How the frontend is organized

```
web/src/
  app/         composition: router, query client, shell.ts (layout choice + navigation)
  routes/      thin route files: pick a layout, guard access, render a feature
  layouts/     page frames (AppLayout, AuthLayout); know nothing about features
    shell/     one CSS grid; a layout variant is one block in shell.css
  features/    one folder per feature; only index.ts is public
  shared/
    ui/          design primitives (Button, Input, Card, Badge, Table, Dialog…)
    components/  building blocks (DataTable, FormField, PageHeader, states…)
    api/         typed client, error model, list types (schema.d.ts is generated)
    styles/      tokens.css is the only place colors and sizes are defined
    session/ theme/ lib/
```

The import rules are enforced by ESLint (`web/eslint.config.js`): shared code and layouts can't import a feature, and a feature is only reachable through its `index.ts`.

### Common changes

| I want to… | Edit |
|---|---|
| Change a color, radius or the sidebar width | `web/src/shared/styles/tokens.css` |
| Change how every button/input/badge looks | the component in `web/src/shared/ui/` |
| Add a page | a file in `web/src/routes/_app/`, then one entry in `web/src/app/shell.ts` |
| Move the navigation (left / right / top) | `layoutVariant` in `web/src/app/shell.ts` |
| Add a new layout arrangement | one name in `layouts/shell/Shell.tsx` + one block in `layouts/shell/shell.css` |
| Show a notification | `notify.success(...)` / `notify.error(error)` from `@/features/notifications` |
| Hide UI by permission | `<PermissionGate permission="users:write">` or `useSession().can(...)` |
| Reword an error by code | `web/src/shared/api/problem.ts` |
