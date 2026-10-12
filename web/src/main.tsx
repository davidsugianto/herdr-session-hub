import { render } from "preact";
import { App } from "./ui/App";
import "./ui/global.css";

render(<App />, document.getElementById("app")!);

if ("serviceWorker" in navigator && window.isSecureContext && import.meta.env.PROD) {
  navigator.serviceWorker.register("/sw.js").catch(() => {});
}
