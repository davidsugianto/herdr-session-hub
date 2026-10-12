import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { ApiError, getDevices, getMe, revokeDevice } from "../lib/api";
import { relTime, sessionColor } from "../lib/format";
import { auth, connection, sessions } from "../lib/store";
import type { Device } from "../lib/types";
import { PairForm } from "./pair-form";
import "./settings.css";

const CONFIRM_MS = 4000;

const errMessage = (e: unknown): string =>
  e instanceof ApiError ? e.message || e.code : e instanceof Error ? e.message : "Request failed";

const CONNECTION_LABEL = { connecting: "Connecting…", online: "Connected", offline: "Offline" } as const;

export function Settings() {
  const [devices, setDevices] = useState<Device[] | null>(null);
  const [meId, setMeId] = useState("");
  const [error, setError] = useState("");
  const [confirmId, setConfirmId] = useState("");
  const [busyId, setBusyId] = useState("");
  const [notice, setNotice] = useState("");
  const confirmTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const alive = useRef(true);

  const conn = connection.value;
  const list = sessions.value;
  const onlineCount = list.filter((s) => s.online).length;

  const loadDevices = useCallback(async () => {
    try {
      const d = await getDevices();
      if (!alive.current) return;
      setDevices(d);
      setError("");
    } catch (e) {
      if (alive.current) setError(errMessage(e));
    }
  }, []);

  useEffect(() => {
    alive.current = true;
    void loadDevices();
    // Marks "this device"; failure only drops the marker.
    void (async () => {
      try {
        const me = await getMe();
        if (alive.current) setMeId(me.id);
      } catch {
        /* no marker */
      }
    })();
    return () => {
      alive.current = false;
      clearTimeout(confirmTimer.current);
    };
  }, [loadDevices]);

  const onRevoke = async (d: Device): Promise<void> => {
    if (confirmId !== d.id) {
      setConfirmId(d.id);
      clearTimeout(confirmTimer.current);
      confirmTimer.current = setTimeout(() => setConfirmId(""), CONFIRM_MS);
      return;
    }
    clearTimeout(confirmTimer.current);
    setConfirmId("");
    setBusyId(d.id);
    setNotice("");
    try {
      const res = await revokeDevice(d.id);
      if (res.warning) {
        // The current device revoked itself; the cookie is gone.
        auth.value = "unpaired";
        return;
      }
      await loadDevices();
    } catch (e) {
      if (alive.current) setError(errMessage(e));
    } finally {
      if (alive.current) setBusyId("");
    }
  };

  return (
    <main class="se">
      <h1>Settings</h1>

      <section class="se-card" aria-labelledby="se-conn">
        <h2 id="se-conn">Connection</h2>
        <p class="se-row">
          <span class={`se-pill se-conn-${conn}`}>{CONNECTION_LABEL[conn]}</span>
          <span class="se-muted">
            {onlineCount} of {list.length} session{list.length === 1 ? "" : "s"} online
          </span>
        </p>
        {list.length === 0 ? (
          <p class="se-muted">No sessions yet.</p>
        ) : (
          <ul class="se-list">
            {list.map((s) => (
              <li key={s.name} class="se-item">
                <span class={`se-bar se-bg-${sessionColor(s.name)}`} aria-hidden="true" />
                <span class="se-grow">
                  <span class="se-name">{s.label || s.name}</span>
                  <span class="se-muted"> · {s.agents} agent{s.agents === 1 ? "" : "s"}</span>
                </span>
                <span class={`badge ${s.online ? "finished" : ""}`}>{s.online ? "online" : "offline"}</span>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section class="se-card" aria-labelledby="se-dev">
        <h2 id="se-dev">Devices</h2>
        {error && (
          <p class="se-error" role="alert">
            {error}
          </p>
        )}
        {notice && <p class="se-muted">{notice}</p>}
        {devices === null && !error && <p class="se-muted">Loading…</p>}
        {devices !== null && devices.length === 0 && <p class="se-muted">No paired devices.</p>}
        {devices !== null && devices.length > 0 && (
          <ul class="se-list">
            {devices.map((d) => {
              const confirming = confirmId === d.id;
              return (
                <li key={d.id} class="se-item">
                  <span class="se-grow">
                    <span class="se-name">{d.name || d.id}</span>
                    {d.id === meId && <span class="badge se-me">this device</span>}
                    <span class="se-muted se-sub">last seen {relTime(d.last_seen)}</span>
                  </span>
                  <button
                    type="button"
                    class={confirming ? "danger se-confirm" : "danger"}
                    disabled={busyId === d.id}
                    onClick={() => void onRevoke(d)}
                  >
                    {busyId === d.id ? "Revoking…" : confirming ? "Tap to confirm" : "Revoke"}
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </section>

      <section class="se-card" aria-labelledby="se-pair">
        <h2 id="se-pair">Pair this device</h2>
        <p class="se-muted">
          The Home Screen app keeps its own cookies and cannot use the QR link from Safari. Run <code>hub pair</code> on
          your Mac and paste the code here.
        </p>
        <PairForm onPaired={() => { setNotice("Paired."); void loadDevices(); }} />
      </section>

      <p class="se-about">Herdr Session Hub · {location.host}</p>
    </main>
  );
}
