# Access model / auth for single-user financial data

Life Ledger holds the user's **personal financial ledger on the public internet** and is effectively **single-user**. This ADR fixes how the app is protected. The stack is settled ([ADR: stack & architecture](https://github.com/emepetres/life-ledger/issues/2)): a **Go app on Azure Container Apps**, server-rendered `html/template` + htmx, SQLite — one deployable unit that must also **run locally for QA**. That context makes the auth options concrete.

## Decision

Require **real authentication** (a secret the user supplies, not just a secret in the URL), enforced **in-app** via a **single shared password**. Everything below follows from those three choices.

| Aspect | Decision |
| --- | --- |
| Posture | Real authentication required. Unguessable-URL-only (splittypie's stance) **rejected** — too weak for financial data; URLs leak via history, referrers, and logs and can't be rotated without breaking the bookmark. |
| Enforcement point | **In-app** middleware — identical behavior locally and in prod, self-contained single deployable, security lives in the repo. Platform "Easy Auth" rejected for *this* map (only exists in Azure → local QA would run no auth path). |
| Credential | **Single shared password** — one user, so OAuth/IdP ceremony is out of proportion to the threat. |
| Login mechanism | **Login form + signed session cookie** (not HTTP Basic) — real logout, fits htmx UX, credential sent once. |
| Session representation | **Signed stateless cookie** (HMAC via a vetted Go lib; no session table). "Log out everywhere" = rotate the signing key — per-session revocation is a non-feature for one user. |
| Session lifetime | **30-day sliding** expiry, **persistent** cookie. Active use never expires; caps a stolen-cookie window at 30 days without constant re-login. |
| Password at rest | **bcrypt hash** (`golang.org/x/crypto/bcrypt`) in config (env / Azure secret) — never plaintext. Constant-time compare + slow hashing come for free. |
| Brute force | **bcrypt + light per-IP in-memory rate limit** (~5 attempts/min then cooldown) on the login POST. **No hard lockout** (self-lockout risk, over-engineered here). Single-instance → in-memory is fine. |
| Cookie flags | `HttpOnly` + `SameSite=Lax` **always**; **`Secure` env-conditional** (on in prod, off for local `http://localhost`). |
| HTTPS | Handled by the **Container Apps platform** (TLS termination + HTTP→HTTPS redirect on the default domain) — no app redirect code. |
| CSRF | **`SameSite=Lax` only**; explicit tokens **consciously deferred**. Lax closes the practical CSRF vector for a single-user, no-cross-origin app; add tokens if it ever goes multi-user/cross-origin. |
| Protected surface | All routes require the session cookie **except** the login page, static assets, and an **unauthenticated health-check endpoint** (Container Apps probes need it). |
| Supporting bits | A **logout route** clearing the cookie; a **small helper** (make target / CLI) to generate the bcrypt hash pasted into config. |

## Rationale / deviations worth recording

- **In-app over platform Easy Auth** — the deciding factor is that in-app auth exercises the *same* login path in local QA and production. Easy Auth would leave the shipped auth path untested locally and smuggle a deployment decision into a map that deliberately defers deployment. Easy Auth remains available as a *future* hardening layer on top.
- **Shared password over OAuth** — for a single known user, a strong bcrypt-hashed password + signed session gives the "real auth" property with minimal ceremony; an OAuth redirect dance protects data only one person accesses.
- **Stateless cookie over server-side sessions** — avoids a sessions table and cleanup job; the only thing lost (per-session revocation) is irrelevant for one user.
- **`Secure` conditional, not always-on** — an always-`Secure` cookie silently breaks login over `http://localhost`, the exact local QA path protected throughout these decisions.
- **CSRF tokens deferred, not ignored** — recorded as a conscious, revisitable call so a future multi-user pivot has a clear place to add them.

## Later upgrades left open (not this map)

- Container Apps built-in "Easy Auth" as a platform layer **on top of** the in-app gate.
- OAuth / identity provider (Google, Microsoft Entra) if the app ever becomes multi-user.

Decided in [issue #7](https://github.com/emepetres/life-ledger/issues/7); stack context in [issue #2](https://github.com/emepetres/life-ledger/issues/2).
