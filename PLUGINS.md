# HappyHome Quest Plugin Contract

A plugin is an independently-run HTTP service that hhq talks to
server-to-server. It can supply a full-screen kiosk view (e.g. a nav button
that shows a custom widget), synthetic calendar events (e.g. bill due dates),
or both. **hhq and its reference plugin (billtracker-plugin) happen to be
written in Go, but nothing about the contract requires that.** Any language or
framework can implement a plugin as long as it exposes the HTTP endpoints
described below with the documented request/response shapes. A plugin could
be a Python Flask app, a Node service, a shell script behind a tiny HTTP
server - hhq only ever speaks plain HTTP + JSON/HTML to it.

## Trust boundary (read this first)

hhq treats a registered plugin's HTTP responses as **trusted** HTML/content,
not sanitized user input. `GET /view`'s response is inlined directly into the
kiosk page; a plugin's icon markup (from `GET /manifest`) is inlined into the
kiosk nav button as raw SVG. Only register plugins you wrote or trust as much
as hhq itself - there is no sandboxing or output sanitization on hhq's side.

## Registration and networking

- A plugin is added to hhq via `CONFIG_DIR/plugins.json` (or
  `PLUGINS_JSON`/`PLUGINS_JSON_FILE`, same `_FILE`-suffix convention used
  elsewhere in hhq's config), reconciled against the database on every hhq
  startup. There is no dashboard "add plugin" UI - `plugins.json` is the
  source of truth for which plugins exist.
- Each entry is a JSON object:

  ```json
  {
    "id": "billtracker",
    "name": "Bill Tracker",
    "base_url": "http://billtracker-plugin.hhq.svc.cluster.local:8090",
    "enabled": true
  }
  ```

  `id` is a stable slug (also used as a URL path segment for hhq's own
  routes, e.g. `/kiosk/view/plugin/{id}`) and `base_url` must be reachable
  from hhq's pod/process with no path suffix (hhq appends `/register`,
  `/manifest`, `/view`, etc. itself). `name` defaults to `id` if omitted.
- hhq talks to the plugin; the plugin is never called from a parent's or
  child's browser directly. Kiosk view content and the parent settings page
  are both proxied through hhq (see below).
- Plugins are **not** authenticated by hhq's own login/session system -
  they're authenticated with a single shared bearer token, established via
  self-registration (next section).

## Authentication: self-registration

There is nothing to hand-generate or keep in sync across two config files.
On startup (and again immediately whenever a plugin is added/enabled),
hhq checks whether it already has a stored token for that plugin. If not, it
calls the plugin's registration endpoint to get one. Every other endpoint
then requires that token on every request.

### `POST /register`

Called by hhq **unauthenticated** - a plugin has no token yet at this point,
so nothing else could sign the request. Must be implemented so it succeeds
**only once**:

- First call: generate a fresh random secret (32 bytes is what
  billtracker-plugin uses, hex-encoded), persist it (encrypted at rest is
  strongly recommended, not just plaintext config), and return it:

  ```json
  { "token": "a1b2c3...64 hex chars..." }
  ```

  Respond `200 OK` with that JSON body.
- Any subsequent call (a token has already been issued): respond
  `403 Forbidden`. This is what makes `/register` safe to leave
  unauthenticated - it can only ever hand out the token once, to whichever
  caller reaches it first (expected to be hhq, on its first successful
  contact with a freshly-started plugin).
- Make the generate-and-store step atomic (e.g. an `INSERT ... ON CONFLICT
  DO NOTHING`-style check-and-set) so two racing calls can never both "win"
  and a losing caller can never learn the winning token.
- hhq retries `POST /register` every 15 seconds if the plugin isn't reachable
  yet (e.g. plugin and hhq starting around the same time in Kubernetes with
  no init-container ordering between them) - a plugin doesn't need to be up
  before hhq is; it just needs to answer eventually.

### Every other endpoint requires the bearer token

Once a token is established, hhq sends it as:

```
Authorization: Bearer <token>
```

on every request to `/manifest`, `/view`, `/events`, `/settings`, and
`/actions/{id}`. A plugin must reject any request to these routes that's
missing the header or whose token doesn't match, with `401 Unauthorized`.
Compare the token in constant time (e.g. `crypto/subtle.ConstantTimeCompare`
in Go, `hmac.compare_digest` in Python) so response timing can't be used to
guess it a byte at a time. Look the token up fresh per request (don't cache
it only at process start) - this naturally means every route stays
unreachable until registration has actually completed, with no separate
"am I registered yet" flag needed.

### `GET /healthz`

Unauthenticated (Kubernetes-style liveness/readiness probes hit this with no
auth header). Any `2xx` response means healthy. hhq does not currently call
this itself (see caveat below) - it exists for the plugin's own container
orchestration, but implementing it is expected.

### Recovery if hhq and the plugin fall out of sync

If hhq's plugin row and the plugin's stored token ever diverge (e.g. hhq
never received the `/register` response even though the plugin committed its
token), there's no authenticated "re-register" flow - recovery is manual:
clear the plugin's stored token (reopens `/register`) and null out hhq's
stored `encrypted_token` for that plugin (makes hhq retry registration on its
next restart or retry tick).

## Endpoints

All bodies are JSON unless noted. All endpoints below (except `/register` and
`/healthz`) require the `Authorization: Bearer <token>` header described
above.

### `GET /manifest`

Describes the plugin: whether it wants a kiosk nav button/view, and whether
it supplies calendar events. Fetched at registration time and periodically
thereafter (hhq allows up to 10s for this call).

Request: no body, no query params.

Response `200 OK`:

```json
{
  "id": "billtracker",
  "name": "Bill Tracker",
  "version": "1.0.0",
  "view": {
    "enabled": true,
    "label": "Bills",
    "icon": "<svg>...</svg>"
  },
  "provides_events": true
}
```

- `id` / `name` / `version`: informational (version isn't currently used by
  hhq for compatibility gating, but include it).
- `view.enabled`: if `true`, hhq shows a nav button on the kiosk labeled
  `view.label` using `view.icon` as trusted inline SVG markup (a blank icon
  falls back to a generic default). If `false`, no nav button is shown and
  `GET /view` is never called.
- `provides_events`: if `true`, hhq provisions a dedicated synthetic calendar
  for this plugin and starts periodically calling `GET /events` to populate
  it (see below). If `false`, `GET /events` is never called.

### `GET /view`

Only called if the manifest's `view.enabled` was `true`, and only when a
user actually taps the plugin's kiosk nav button (not polled/pre-fetched) -
hhq allows 3 seconds for this call, since a person is waiting.

Request: no body, no query params.

Response `200 OK`: a raw HTML fragment (`Content-Type` doesn't need to be set
precisely; hhq treats the body as HTML regardless) - not a full HTML
document. It's inlined directly into the kiosk's full-screen content region.
Style it to fit a kiosk display (hhq does not wrap it in any particular
layout beyond that region).

A non-`200` response, a timeout, or a connection failure results in hhq
showing an empty state on the kiosk rather than an error - a down/slow
plugin should never be able to put an error blob on an always-on wall
display.

### `GET /events`

Only called if the manifest's `provides_events` was `true`. Called
periodically by hhq's background scheduler (never inline in a user-facing
request), so it can afford to be slower - hhq allows 10 seconds.

Request: query params `from` and `to`, both dates formatted `YYYY-MM-DD`
(e.g. `/events?from=2026-07-20&to=2026-07-27`), describing the inclusive-ish
window hhq wants synthetic events for. Return every event whose occurrence
falls in or near that window; hhq does its own window filtering on read, so
it's fine to be generous.

Response `200 OK`:

```json
{
  "events": [
    {
      "uid": "bill-1234-2026-07-20",
      "summary": "Electric Bill Due",
      "location": "",
      "description": "PSE&G - $142.50",
      "starts_at": "2026-07-20T00:00:00-04:00",
      "ends_at": "2026-07-20T23:59:59-04:00",
      "all_day": true,
      "actions": [
        { "id": "mark-paid", "label": "Mark Paid", "requires_parent": true }
      ]
    }
  ]
}
```

- `uid`: stable, unique per plugin - hhq upserts on this key and prunes any
  previously-seen `uid` this call no longer returns (so a paid/deleted bill
  disappears from the kiosk on the next sync). Must stay stable across calls
  for the same logical event/occurrence; changing it will look like a
  delete-and-recreate.
- `starts_at` / `ends_at`: RFC 3339 timestamps, **timezone-aware** - do not
  send bare UTC midnight for what's conceptually a local calendar day (hhq
  has been bitten by exactly this class of bug internally - see this repo's
  `CLAUDE.md` "Round 17" for the postmortem). Send the wall-clock instant in
  whatever offset is correct for that event.
- `all_day`: `true` for a date-only concept (like a bill due date) rather
  than a specific time.
- `location` / `description`: optional, empty string if not applicable.
- `actions`: optional, omit or send `[]` if this event has no tappable
  actions. Each action's `id` must match a route this plugin serves at
  `POST /actions/{id}` (see below). `requires_parent: true` means hhq will
  only show/allow this action to a signed-in parent, never an unauthenticated
  kiosk tap (enforced both in hhq's UI and server-side - a plugin doesn't
  need to re-check this itself, but also shouldn't assume the "requires
  parent" gate is the *only* thing standing in front of the action route,
  since the token auth is what actually secures it).

A non-`200` response or malformed JSON causes that sync pass to be skipped
(logged, marked unhealthy) - previously-cached events are left alone rather
than pruned, so a transient plugin outage doesn't blank out the kiosk.

### `POST /actions/{id}`

Called when a user taps one of the action buttons this plugin listed on an
event (`{id}` is the action's `id` from `GET /events`, e.g.
`/actions/mark-paid`). Inline, user-facing - hhq allows 10 seconds.

Request body:

```json
{ "uid": "bill-1234-2026-07-20" }
```

`uid` identifies which event (by the same `uid` this plugin returned from
`GET /events`) the action applies to.

Response: any `2xx` status code means success - body is ignored. Perform
the side effect synchronously (e.g. mark a bill paid) before responding;
hhq re-fetches `GET /events` immediately after a successful action so a
now-stale event (e.g. a paid bill that should disappear) is pruned from the
kiosk without waiting for the next scheduled sync.

A non-2xx response is surfaced to the user as a failed action.

### `GET /settings` and `POST /settings`

The plugin's own configuration UI, reverse-proxied through hhq so a parent
never talks to the plugin process directly. Reachable only from hhq's parent
dashboard, behind hhq's own login + CSRF protection - the plugin itself does
not need to know anything about hhq's session/auth scheme. hhq allows 15
seconds for this call (a deliberate, parent-initiated page load or form
submit, not a background poll).

- **Response must be a full, standalone HTML document** (with `<html>`,
  `<body>`, etc.) - not a fragment like `/view`. hhq post-processes the HTML
  before showing it to the parent's browser:
  - Injects a hidden `csrf_token` field into every `<form method="POST">` it
    finds, so the form can be submitted back through hhq's own CSRF
    middleware. Your `POST /settings` handler should ignore any form field
    it doesn't recognize (including `csrf_token`) rather than rejecting the
    request because of it.
  - Injects a "← Back to Dashboard" link right after the opening `<body>`
    tag (or prepends it if there's no `<body>` tag at all), since the
    plugin's page otherwise has no way back to hhq's own UI.
- `GET /settings`: render the current settings as an HTML form (or forms).
- `POST /settings`: hhq forwards the submitted form body (`application/
  x-www-form-urlencoded`) as-is (minus hhq's own `csrf_token` field, which
  your handler should simply ignore if present). Handle the submitted
  action, then respond with the same kind of full HTML document as the
  `GET` case (typically the same settings page, re-rendered) - hhq
  re-applies the same CSRF-token and back-link injection to whatever you
  return, since a `POST` handler commonly re-renders the same page's forms.
- Any `Content-Type` you set is passed through to the parent's browser
  unchanged for non-HTML responses (e.g. if you serve a JSON API endpoint
  under the same path for some other purpose) - only `text/html` responses
  get the injection treatment described above.

## Minimal implementation checklist

To stand up a new plugin from scratch:

1. `POST /register` - unauthenticated, issues a token exactly once,
   `403 Forbidden` on every call after the first.
2. Bearer-token check in front of every other route below, `401
   Unauthorized` on missing/mismatched token, looked up fresh per request
   (not just at startup).
3. `GET /manifest` - at minimum `{"id", "name", "version", "view": {
   "enabled": false }, "provides_events": false}` is a valid (if inert)
   plugin; flip on `view`/`provides_events` as you implement them.
4. If `view.enabled: true` -> `GET /view` returning an HTML fragment.
5. If `provides_events: true` -> `GET /events` returning the JSON shape
   above, with stable `uid`s and timezone-aware timestamps.
6. If any event has `actions` -> a matching `POST /actions/{id}` per action
   `id`.
7. `GET /settings` + `POST /settings` if the plugin has anything a parent
   needs to configure - otherwise both can be omitted (hhq only ever links
   to `/parent/plugins/{id}/settings` from its own dashboard, which a parent
   simply won't click if there's nothing there... though note hhq itself
   doesn't currently check `/settings` exists before offering the link, so
   omitting it entirely means that link 404s/errors if clicked).
8. `GET /healthz` - simple `2xx` liveness response, for your own container
   orchestration's probes.

## Known caveats (current state, not contractual)

- hhq does not currently call `GET /healthz` itself for anything (no
  liveness-driven behavior on hhq's side) - it's there for your own
  deployment's probes.
- There's no version-compatibility negotiation - `manifest.version` is
  carried through but not currently checked against anything.
- The reference implementation, `billtracker-plugin`, has automated test
  coverage only for its vendor connectors, not for its own `/register` or
  bearer-token middleware - don't treat its code as a fully-verified
  reference for edge cases, just as a working example of the wire shapes
  above.
