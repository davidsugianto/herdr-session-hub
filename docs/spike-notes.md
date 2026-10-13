# Milestone 0 spike notes

Findings from building and smoke-testing against herdr 0.9.3 (API protocol 22)
on the author's MacBook. Facts marked **verified** were observed against the
real sockets; the rest come from the schema or herdr's docs.

## Where the plan was wrong or incomplete

1. **There is no session-wide agent status subscription (verified in schema and docs).**
   `pane.agent_status_changed` requires a `pane_id`, and herdr rejects the whole
   `events.subscribe` request if a named pane does not exist. The hub subscribes to
   pane lifecycle events plus one status subscription per agent pane, and treats
   `pane_created`, `pane_moved` and `pane_agent_detected` as a signal to resubscribe
   (`sessions.Resync`). A status event names only a pane and a status, so the
   adapter re-reads the agent (`agent.get`) to build full state.
2. **herdr answers one request per connection and then closes it (verified).**
   A second request on the same connection fails with a broken pipe. Request and
   response callers must dial per call (`herdr.CallOnce`); only `events.subscribe`
   holds a connection open. The plan's "one long-lived client per session socket"
   applies to the subscription only.
3. **`events_lost` is an error response, not an event, and is not in the schema.**
   Per herdr's docs it carries the original subscribe request id and
   `error.code = "events_lost"`, then the server closes the connection. Not yet
   observed live (cannot be forced safely).
4. **Event names come in two spellings.** Subscription types and subscription events
   are dotted (`pane.agent_status_changed`); lifecycle events in the event stream are
   underscored (`pane_closed`). The adapter accepts both.
5. **Pane events carry pane ids, not terminal ids.** The hub keys agents by terminal
   id, so the adapter keeps a pane to terminal map from the last snapshot.
6. **Session names are case-sensitive directory names, and there is no default
   socket here (verified).** This machine has sessions `CEKAT` ("work") and
   `FRIDAY` ("labs") and no `~/.config/herdr/herdr.sock`. The config example in the
   plan must use `name = "CEKAT"` plus a `label = "work"`. The running herdr also
   exports `HERDR_SOCKET_PATH`/`HERDR_SESSION` to its panes.
7. **`pane.read` takes `recent_unwrapped` (underscore) (verified);** the CLI flag
   spelling `recent-unwrapped` is rejected by the socket API with `invalid_request`.

## Verified against the real sessions

- `hub watch` finds both sessions, labels them from config, and prints the full
  agent list (work: 7 agents, labs: 1) within a second of starting.
- `hub capture` resolves an agent by name or pane id and saves `pane.read` text;
  error paths (unknown session, unknown agent) exit 1 with a clear message.
- The three integrations (claude, pi, omp) were already installed and current.
- **Live transitions across both sessions.** `hub watch` was left running while real
  agents changed state and printed, with labels and per-session isolation:
  `[work] pi ... done -> working`, `[labs] claude wA:p1 working -> done`,
  `[labs] ... done -> idle`, `[labs] ... idle -> working`, `[work] pi ... working -> done`.
  This exercised the per-pane status subscriptions and the `agent.get` re-read.
- herdr reports an unknown agent target as `agent_not_found` (not `not_found`).
- **A new pane is picked up live.** A Claude Code agent started in `work` while
  `hub watch` ran: it appeared as `+ claude w1:p2 unknown` (the `pane_created`
  resync), then its status subscription reported `idle -> blocked -> idle` within
  two seconds. That blocked state was too short to capture.
- **A free-text question is not `blocked`.** A Claude Code agent that ends its turn
  by asking the user to choose among options in prose ("Pick one: 1. ... 2. ...")
  was reported by herdr as `idle`, not `blocked`; only tool permission menus count.
  So for such an agent the hub sees an ordinary finished turn. The "needs you"
  state, the approve/trust buttons and their profiles only apply to real permission
  prompts. Milestone 1 should decide how to treat "finished and waiting on a
  question" (for example, show the last lines in the notification).

## Not verified

- **Stopping and restarting a real session** (isolation and reconnect). Not done:
  stopping a session kills its agents, so it needs a throwaway named session.
- **`events_lost` against the real server.**
- **Blocked-screen captures** for Claude Code, omp and pi. Attempted: a watcher
  that captures a pane on `-> blocked` ran for about 30 minutes, but only one
  two-second block occurred, omp was not running, and pi never blocked. The two
  panes captured manually were not blocked prompts and held unrelated work
  content, so they were deleted. `hub capture` works; capture real permission
  menus when building the milestone 1 profiles.
- **Tailscale `Tailscale-User-Login` header.** Tailscale is not installed on this
  machine, so `tailscale serve` could not be probed.

## Adapter behavior worth remembering for milestone 1

- Subscribing lists the agent panes, subscribes, then lists them again: a pane
  created in between would have no status subscription, so a changed set triggers
  a retry (and a `Resync` if it never settles).
- Captured screens are written with mode 0600 and `testdata/screens/` is
  git-ignored, because terminal text can contain secrets. Review a capture before
  adding it with `git add -f`.

## Loop behavior worth remembering for milestone 1

- Only status changes produce a `Transition`; a changed `PaneID` is absorbed
  silently, but actions address panes, so milestone 1 needs an "agent changed"
  update.
- Repeated `events_lost` with no progress in between backs off; `Resync` never does.
- The Apache-2.0 `LICENSE` file was still missing at the end of milestone 0 (the
  download from apache.org failed); it was added in the milestone 1 cleanup.

# Milestone 1 findings

Findings from running the hub core against herdr 0.9.3 with a throwaway agent.
Facts marked **verified** were observed against a real session.

1. **herdr's `name` is the user-assigned agent name, `agent` is the agent type.**
   The name is free text (for example `sa-d810-3`); the type is `claude`, `pi` or
   `omp`. The hub keys prompt profiles by `Kind` (the type) and falls back to the
   name only when no kind is known. (**verified**: a named Claude agent was
   reported as `claude` in `kind`.)
2. **The trust-folder dialog counts as `blocked` (verified).** herdr reports the
   "Is this a project you created or one you trust?" dialog as `blocked`, although
   it is not a tool permission prompt. It has no numbered options, so the claude
   profile does not parse it, and `agent start` can return `agent_not_ready` while
   the dialog is showing.
3. **A real Claude Code permission prompt parsed correctly (verified for Claude
   only).** The prompt was for a Bash command and its options were: Yes / Yes, and
   always allow access to <dir> from this project / Yes, and switch to auto mode /
   No. Trust must map to the "always allow" option and never to "switch to auto
   mode"; the profile's trust regex does this. Profiles stay `verified = false`
   until pi and omp prompts are captured too.
4. **A subscription whose buffer (256) overflows is closed by the engine, so every
   consumer must resubscribe.** The API ring resubscribes and resets (clients get a
   fresh snapshot). The timeline recorder resubscribes too; events dropped in
   between are not recorded.
5. **Actions verified live through the API against a throwaway agent:** pair,
   reply, approve (wrong hash -> 409), deny with note, trust, send, keys,
   interrupt, seen, reply refused while blocked (409 `agent_blocked`), SSE
   snapshot and changes, and timeline and audit rows.
6. **Go version: the hub now requires Go 1.26.** `modernc.org/sqlite` v1.60 needs
   it; v1.38 would be the last version for Go 1.23. Decision: keep Go 1.26.

## Not verified

- **Stopping and restarting a real session** against the hub.
- **`events_lost` against a real server.**
- **pi and omp blocked screens.**

# Milestone 3 findings

Built without Tailscale or an iPhone available, so the end-to-end path is **not verified**.

## Verified (unit tests, fakes)

- Config quiet-hours grammar, store schema v2 migration from v1, mutes expiry, push subscription cascade on device revoke.
- Tunnel: `tailscale status --json` parsing and `serve` argument construction against fixtures.
- API identity check, dynamic tunnel Host, Secure cookie, push and mute endpoints.
- Notifier rules (blocked episode, finished, mutes, quiet hours, gone pruning, resubscribe) and web push sink status mapping against an `httptest` server.
- Service worker push and click handlers with a stubbed `self`; web bundle size.

## Not verified (needs the Mac with Tailscale and the iPhone)

- `tailscale serve --bg --https=443 <target>` argument syntax on the installed Tailscale version (`tailscale serve --help`).
- `tailscale serve` sets `Tailscale-User-Login` and `X-Forwarded-Proto: https`, and keeps the original Host header.
- iOS Web Push delivery, the lock-screen latency target (under 2 s), tap-to-open deep link `/#/a/<session%2Fterminal>`, and the app icon badge.
- Apple push service accepting the configured VAPID `contact` (`sub` claim).
- Whether iOS tolerates suppressing the system notification when the app is in front (the app always shows it for now).
- Home Screen app pairing on the tunnel origin (separate cookies, paste a code).
- Real `caffeinate -i -w <pid>` keep-awake behavior.
- pi and omp blocked screens, session stop/restart and `events_lost` (still open from earlier milestones).
