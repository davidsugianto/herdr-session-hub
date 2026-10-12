import { useEffect, useState } from "preact/hooks";
import { pairWithCode, consumePairFragment } from "../lib/pair";
import { agents, auth, connection, startSync } from "../lib/store";
import { AgentDetail } from "./AgentDetail";
import { Board } from "./Board";
import { Keys } from "./Keys";
import { PairScreen } from "./PairScreen";
import { Settings } from "./Settings";
import { Timeline } from "./Timeline";
import { ToastHost, errMsg, showToast } from "./Toast";
import "./app.css";

type Route = { name: "board" } | { name: "detail"; id: string } | { name: "timeline" } | { name: "keys" } | { name: "settings" };

export function parseRoute(hash: string): Route {
  const h = hash.replace(/^#\/?/, "");
  if (h.startsWith("a/")) {
    const raw = h.slice(2);
    try { return { name: "detail", id: decodeURIComponent(raw) }; } catch { return { name: "detail", id: raw }; }
  }
  if (h === "timeline") return { name: "timeline" };
  if (h === "keys") return { name: "keys" };
  if (h === "settings") return { name: "settings" };
  return { name: "board" };
}

const TABS = [
  { name: "board", href: "#/", label: "Board", icon: "▦" },
  { name: "timeline", href: "#/timeline", label: "Timeline", icon: "☰" },
  { name: "keys", href: "#/keys", label: "Keys", icon: "⌨" },
  { name: "settings", href: "#/settings", label: "Settings", icon: "⚙" },
] as const;

export function App() {
  const [route, setRoute] = useState<Route>(() => parseRoute(location.hash));

  useEffect(() => {
    const onHash = () => setRoute(parseRoute(location.hash));
    window.addEventListener("hashchange", onHash);
    (async () => {
      const code = consumePairFragment();
      // consumePairFragment clears the hash with replaceState (no hashchange event), so re-read it.
      setRoute(parseRoute(location.hash));
      if (code) {
        try {
          await pairWithCode(code);
        } catch (e) {
          auth.value = "unpaired";
          showToast(`Pairing failed: ${errMsg(e)}`);
        }
      }
      try {
        startSync();
      } catch (e) {
        showToast(`Could not start sync: ${errMsg(e)}`);
      }
    })();
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  if (auth.value === "unpaired") {
    return <><PairScreen /><ToastHost /></>;
  }

  const attention = Object.values(agents.value).filter((a) => a.state === "blocked" || a.state === "finished").length;
  const offline = connection.value === "offline";

  let screen;
  switch (route.name) {
    case "detail": screen = <AgentDetail key={route.id} id={route.id} />; break;
    case "timeline": screen = <Timeline />; break;
    case "keys": screen = <Keys />; break;
    case "settings": screen = <Settings />; break;
    default: screen = <Board />;
  }

  return (
    <div class={`shell${route.name === "detail" ? " shell-detail" : ""}`}>
      {offline && <div class="offline-banner" role="alert">Offline, reconnecting...</div>}
      <div class="shell-screen">{screen}</div>
      {route.name !== "detail" && (
        <nav class="tabbar" aria-label="Main">
          {TABS.map((t) => (
            <a key={t.name} href={t.href} class={`tab${route.name === t.name ? " active" : ""}`} aria-current={route.name === t.name ? "page" : undefined}>
              <span class="tab-icon" aria-hidden="true">{t.icon}</span>
              <span class="tab-label">{t.label}</span>
              {t.name === "board" && attention > 0 && <span class="tab-badge">{attention}</span>}
            </a>
          ))}
        </nav>
      )}
      <ToastHost />
    </div>
  );
}
