# CLAUDE.md

## What this project is

Synctrades Lite is a standalone Go CLI that syncs a user's Schwab trade history into their own Google Sheet. It is a sibling project to the `synctrades` SaaS app (Next.js + ASP.NET Core, private repo), not part of it — no shared code, no shared repo, separate license. It exists because it's a monetizable product that doesn't require the commercial brokerage license the SaaS app is waiting on: users connect their own Schwab and Google accounts, the tool runs entirely on their machine, and we never touch their credentials or data.

Full product rationale: see `PRODUCT_PIVOT_BRIEF.md` at the root of this repo. It is gitignored and deliberately not committed: it contains internal business strategy (pricing, upsell roadmap) that must stay out of a source-available repo. Keep it local.

## Product/architecture decisions already made

These were decided in planning and should not be re-litigated without a reason:

- **No backend server, no multi-tenancy, no database.** Single local user, single local encrypted credential store. This is the core simplification vs. the SaaS app.
- **Schwab OAuth uses a paste-back flow, not a local listener.** Schwab requires an HTTPS callback URL even for `127.0.0.1` (confirmed in the SaaS app's dev setup, which relies on `mkcert` for a browser-trusted local cert). Asking an individual customer to install a root CA to use a CLI tool is not viable. Instead: the CLI opens the system browser to Schwab's authorize URL using the `redirect_uri` registered on the user's own Schwab app; the browser redirect fails after login, but the address bar still shows the full URL with `code` and `state` query params; the user copies that URL and pastes it back into the terminal for the CLI to parse and exchange. Schwab exact-matches `redirect_uri` at authorize and again at code exchange, so it must be sent verbatim in both requests. Nothing needs to listen on the callback host or port for this to work.
- **Schwab app credentials are user-supplied, never shipped.** Each user registers their own Schwab developer app and provides `client_id`, `client_secret`, and `redirect_uri` as local config. None of the three is compiled into the binary: an embedded secret in a distributed Go binary is recoverable with `strings`, it would breach Schwab's developer terms, and it would undermine the "users connect their own accounts, we never touch their credentials" premise that the no-commercial-license argument depends on. Consequence to design around: customer onboarding requires registering a Schwab app and waiting for approval, which is the heaviest and earliest step in setup.
- **Google Sheets uses a service account, not OAuth.** User creates a GCP service account, downloads a JSON key, and shares their target sheet with the service account's email (like sharing with a person). No OAuth consent screen, no browser flow, no token refresh to maintain. Chosen over the "Sign in with Google" OAuth flow specifically to minimize setup friction and support burden — OAuth consent screen configuration is the single biggest confusion point in comparable bring-your-own-credentials CLI tools.
- **Dedup key is Schwab's `activityId`.** It's unique per transaction in the Schwab transactions response and is the source of truth for "already synced to the sheet."
- **Sync is append-only for MVP.** A transaction whose `activityId` is already in the sheet is skipped entirely, never rewritten. Schwab does revise transactions after the fact (corrections, cancellations, status changes) and append-only will keep the stale row; that is an accepted MVP tradeoff, not an oversight. Consequence: the product must not claim it syncs "updated" trades. An update path means tracking row positions and rewriting cells, which is a materially larger `dedup.go`, and it is deferred.
- **One row per transaction. Fee legs fold into a single `Fees` column.** Every `transferItems[]` array mixes fee legs (they carry `feeType` and a `CURRENCY` instrument) with the actual instrument leg (it carries `positionEffect` and `price`). The row is built from the instrument leg; `Fees` is `-(sum of cost across fee legs)`, so a normal charge renders positive and a refund renders negative. Verify against `netAmount`: instrument-leg `cost` plus fee-leg `cost` should equal `netAmount` (in the sample data, `-81 + -0.65 + -0.01 = -81.66`). Open edge case to handle when `dedup.go` is built, not before: transactions carrying more than one non-fee leg (assignments, exercises, multi-leg orders) break the one-row assumption and need an explicit rule.
- **Distribution:** single compiled binary per platform (macOS/Linux/Windows), built with `goreleaser`, shipped via GitHub Releases. Source-available with a commercial license (Prosperity Public License is the current candidate, not finalized) — not MIT. Free for personal use, paid license for commercial use.
- **Step-by-step end-user setup instructions (Schwab app registration, GCP service account creation, sheet sharing) are deliberately not written into this repo yet.** They'll become user-facing onboarding/marketing content later. This file covers implementation only.

## Repo layout (planned — not yet scaffolded)

```
synctrades-lite/
  cmd/synctrades/main.go        — entry point, CLI wiring (cobra root command)
  internal/schwab/
    client.go                   — typed HTTP client: account numbers, account details, transactions
    oauth.go                    — authorize URL builder, paste-back code exchange
    token.go                    — refresh logic, expiry check, local persistence
    errors.go                   — status-code -> typed errors
  internal/sheets/
    client.go                   — Sheets API wrapper (service account auth)
    dedup.go                    — reads existing activityId column, diffs against fetched transactions
  internal/store/
    credentials.go              — local encrypted storage for Schwab tokens + config (service account key path, Sheet ID)
  internal/cli/
    commands.go                 — cobra commands: auth schwab, auth sheets, sync, status
  go.mod
  go.sum
  README.md                     — placeholder; full onboarding docs come later
  LICENSE                       — commercial/source-available license text (TBD)
```

## Language & tooling

- **Go**, latest stable — check `go version`, target that in `go.mod`.
- **Module path:** `github.com/akirpach/synctrades-lite` (matches the GitHub org used for the `synctrades` SaaS repo — confirm before first push if that's wrong).
- **CLI framework:** `cobra`.
- **Sheets:** `google.golang.org/api/sheets/v4` with `golang.org/x/oauth2/google` service-account credentials (`option.WithCredentialsFile`).
- **Schwab:** hand-rolled `net/http` client. No official Go SDK exists. Port the *behavior* of the SaaS app's Schwab integration, not its code — see "Reference material" below.
- **Local encryption:** AES-GCM for the credential store, key sourced from the OS keychain where possible (e.g. `99designs/keyring`) rather than requiring the user to manage a passphrase — this is a single-user local tool, the bar is "not plaintext on disk," not multi-user key management.
- **Release:** `goreleaser` for cross-compilation, GitHub Actions to run it on tag push.

## Reference material

- **Schwab API behavior:** vendored PDFs in the sibling `synctrades` repo at `backend/documentation/` (`SchwabAPI-Docs.pdf`, `SchwabAPI-transactions.pdf`, `SchwabAPI-account.pdf`) and `backend/documentation/transactions-response.json` for the real transaction JSON shape (transactions are nested: `activityId`, `transferItems[]` each with an `instrument` and `feeType`/`cost`). This repo doesn't vendor its own copy — go to the sibling checkout.
- **Existing C# implementation, for logic parity only (not code reuse — separate repos, separate licenses):** `synctrades/backend/Services/Schwab/{SchwabApiClient,TokenService,SchwabResponseHandler}.cs`. Port the behavior — 5-minute expiry buffer before refresh, Basic-auth (`client_id:client_secret` base64) on the token endpoint, delete stored tokens and force re-auth on a 401/400 refresh failure — not the ASP.NET/EF Core scaffolding around it.

### Confirmed Schwab wire contract

Read off the C# implementation, verified against `AuthController.cs`, `SchwabApiClient.cs` and `SchwabOAuthOptions.cs`. Port this directly rather than re-deriving it:

```
authorize   GET  https://api.schwabapi.com/v1/oauth/authorize
                 ?client_id=…&redirect_uri=<urlencoded>&response_type=code&state=…
                 no scope parameter - Schwab does not use one here

token       POST https://api.schwabapi.com/v1/oauth/token
                 Authorization: Basic base64(client_id:client_secret)
                 exchange: grant_type=authorization_code, code, redirect_uri
                 refresh:  grant_type=refresh_token, refresh_token   (no redirect_uri)

api base         https://api.schwabapi.com/trader/v1/
                 accounts/accountNumbers
                 accounts/{accountHash}
                 accounts/{accountHash}/transactions?startDate=&endDate=&types=
```

Accounts are addressed by `accountHash`, not by account number, so any transaction fetch is a two-call sequence: resolve the hash from `accounts/accountNumbers` first.

Dev `redirect_uri` (reusing the SaaS app's existing Schwab registration for local testing only): `https://127.0.0.1:5001/api/schwab/callback`. Note `synctrades/backend/documentation/DEV_SETUP.md` states `/api/auth/callback`, which is stale and does not match the controller route.

## Build order

1. `internal/schwab/oauth.go` + `token.go` — authorize URL, paste-back code exchange, refresh, local encrypted token storage. Build this first: it's the riskiest and most novel piece, since there's no server to receive the redirect.
2. `internal/schwab/client.go` — account numbers, account details, transactions fetch with date range.
3. `internal/store/credentials.go` — encrypted local storage for Schwab tokens, service account key path, target Sheet ID.
4. `internal/sheets/client.go` + `dedup.go` — read existing `activityId`s from the sheet, append only new transactions.
5. `internal/cli/commands.go` + `cmd/synctrades/main.go` — wire it into `synctrades auth schwab`, `synctrades auth sheets`, `synctrades sync`, `synctrades status`.
6. `goreleaser` config, GitHub Actions release workflow, LICENSE file.

## Explicitly out of scope for MVP

Per `PRODUCT_PIVOT_BRIEF.md`:

- Scheduled/automated syncs — manual trigger only.
- Brokers other than Schwab.
- Calculated fields or analysis — raw data sync only.
- Web dashboard or SaaS wrapper.
- Multiple Schwab accounts in a single run (re-running the tool per account is fine).

## Commands (once scaffolded)

- `go build ./...` — compile; run after every change before declaring something done.
- `go test ./...` — unit tests.
- `go vet ./...` — run alongside build.
- `go run ./cmd/synctrades` — run locally during development.
- `goreleaser release --snapshot --clean` — local test of the release build without publishing.

## Verification before declaring work done

- Any change to `internal/schwab/` (OAuth, token refresh, transaction fetch) → walk the full flow end-to-end against a real dev Schwab account before claiming it works. Silent breakage here is the most expensive kind of bug this project can have.
- Any change to `internal/sheets/dedup.go` → verify against a real sheet with pre-existing rows. A dedup bug either creates duplicate rows or silently drops real trades — both are unacceptable in a financial tool.
- Everything else → `go build ./...` and `go vet ./...` at minimum.

## Communication preferences

Be direct. Lead with what's wrong or risky before what's working. Don't pad responses with affirmations. If something here looks like a mistake or a footgun, call it out instead of working around it silently.
