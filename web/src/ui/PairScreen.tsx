import { PairForm } from "./pair-form";
import "./pair.css";

/** Shown when the device is not paired. */
export function PairScreen() {
  return (
    <main class="pair-screen">
      <div class="pair-card">
        <h1>Pair this device</h1>
        <ol class="pair-steps">
          <li>
            On your Mac, run <code>hub pair</code>.
          </li>
          <li>Scan the QR code or open the link it prints, or paste the code below.</li>
        </ol>
        <hr class="pair-divider" />
        <PairForm />
        <p>Codes work once and expire after a few minutes.</p>
      </div>
    </main>
  );
}
