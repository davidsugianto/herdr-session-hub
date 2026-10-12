import { request } from "./api";
import { auth } from "./auth";

/** Reads "#pair=<code>" from location.hash; if present clears the hash (history.replaceState) and returns the code. */
export const consumePairFragment = (): string | null => {
  const hash = location.hash.startsWith("#") ? location.hash.slice(1) : location.hash;
  const part = hash.split("&").find((p) => p.startsWith("pair="));
  if (part === undefined) return null;
  history.replaceState(null, "", location.pathname + location.search);
  const raw = part.slice("pair=".length);
  let code = raw;
  try {
    code = decodeURIComponent(raw);
  } catch {
    /* keep raw */
  }
  return code || null;
};

/** Short human label for this device, e.g. "iPhone Safari". */
export const deviceName = (ua: string = navigator.userAgent): string => {
  const platform = /iPhone/.test(ua) ? "iPhone"
    : /iPad/.test(ua) ? "iPad"
    : /Android/.test(ua) ? "Android"
    : /Macintosh|Mac OS X/.test(ua) ? "Mac"
    : /Windows/.test(ua) ? "Windows"
    : /Linux/.test(ua) ? "Linux"
    : "";
  const browser = /Edg(e|A|iOS)?\//.test(ua) ? "Edge"
    : /Firefox\/|FxiOS\//.test(ua) ? "Firefox"
    : /Chrome\/|CriOS\//.test(ua) ? "Chrome"
    : /Safari\//.test(ua) ? "Safari"
    : "Browser";
  return `${platform} ${browser}`.trim().slice(0, 100);
};

/** POST /api/pair {code, device_name}; on success sets auth to "paired". Throws ApiError (invalid_code on 401). */
export const pairWithCode = async (code: string): Promise<void> => {
  await request<unknown>("/api/pair", {
    method: "POST",
    body: { code, device_name: deviceName() },
    keepAuth: true,
  });
  auth.value = "paired";
};
