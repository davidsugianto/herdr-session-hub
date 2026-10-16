# herdr-session-hub
A Dashboard for herdr agents: live status, instant alerts, and one-tap approve, reply or interrupt.

## Web app

`hub serve` serves a mobile web app (a PWA) at its address, `http://127.0.0.1:7878` by default.

1. Start the hub: `hub serve`
2. Pair a device: `hub pair` prints a one-time code and a QR link (`/#pair=<code>`). Open the link, or paste the code into the app (Settings, "Pair this device").
3. On an iPhone, use Share, then Add to Home Screen. The Home Screen app keeps its own cookies, so pair again from inside it by pasting a code.

The service worker only registers on `https` or `localhost`, so the offline shell and push do not work over plain `http://` on a LAN address. Use the Tailscale tunnel below for the phone.

## Notifications and remote access

Web Push needs HTTPS, so the iPhone reaches the hub through Tailscale.

1. Install Tailscale on the Mac and the iPhone (same tailnet) and enable HTTPS certificates for the tailnet.
2. Start the hub (`hub serve`), then run `hub tunnel`. It runs `tailscale serve` for the hub, saves the tailnet host name and your Tailscale login, and prints a pairing QR for `https://<host>/#pair=<code>`. `hub tunnel off` removes it.
3. Scan the QR, then Share, Add to Home Screen (iOS 16.4 or later).
4. Open the Home Screen app, pair again by pasting a code from `hub pair`, then Settings, Enable notifications.

Requests for the tunnel host (or any `*.ts.net` name once a tunnel is saved) are accepted only when `tailscale serve` forwards them from this Mac with a `Tailscale-User-Login` header that matches the saved login, on top of the device token. Direct loopback use and `-allow-host` LAN names keep working with the device token alone.

A push goes out when an agent becomes blocked or finishes. Tapping it opens the agent. Mute an agent or a session from the agent menu (more options); the app icon badge shows blocked plus finished agents.

Optional `~/.config/herdr-session-hub/config.toml`:

```toml
[[session]]
name = "work"
label = "work"
quiet_hours = "Sat-Sun, 22:00-07:00"   # local time; terms are OR'd: a weekday term silences that whole day, a time window silences those hours every day, so this is all of Sat and Sun plus every night 22:00-07:00

[notify]
blocked_breaks_quiet = true   # blocked still notifies during quiet hours
contact = "mailto:you@example.com"   # VAPID contact sent to the push service

[mac]
keep_awake = "while-working"  # off | while-working | always (holds `caffeinate -i`)
```

### Developing the web app

The built app in `web/dist` is committed and embedded into the binary, so `go build` needs no Node. After changing anything under `web/src`, rebuild and commit `web/dist`:

```sh
cd web
pnpm install --frozen-lockfile
pnpm typecheck
pnpm test
pnpm build        # writes web/dist and fails if JS+CSS exceeds 60 KB gzipped
pnpm dev          # Vite dev server; proxies /api to 127.0.0.1:7878
```
