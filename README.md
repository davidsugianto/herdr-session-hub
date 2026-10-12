# herdr-session-hub
A Dashboard for herdr agents: live status, instant alerts, and one-tap approve, reply or interrupt.

## Web app

`hub serve` serves a mobile web app (a PWA) at its address, `http://127.0.0.1:7878` by default.

1. Start the hub: `hub serve`
2. Pair a device: `hub pair` prints a one-time code and a QR link (`/#pair=<code>`). Open the link, or paste the code into the app (Settings, "Pair this device").
3. On an iPhone, use Share, then Add to Home Screen. The Home Screen app keeps its own cookies, so pair again from inside it by pasting a code.

The service worker only registers on `https` or `localhost`, so the offline shell does not work over plain `http://` on a LAN address. Push notifications and the Tailscale tunnel come in a later milestone.

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
