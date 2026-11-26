import { deviceLabel, urlBase64ToUint8Array } from "./model";

/** Why Web Push cannot work in this browser, or undefined when it can. */
export function webPushUnsupportedReason(): string | undefined {
  if (typeof window === "undefined") return "Not a browser";
  if (!window.isSecureContext) return "Browser notifications need HTTPS (or http://localhost while developing).";
  if (!("serviceWorker" in navigator)) return "This browser has no service worker support.";
  if (!("PushManager" in window)) return "This browser does not support Web Push. On iOS, add Asterism to the Home Screen first.";
  if (!("Notification" in window)) return "This browser does not support notifications.";
  return undefined;
}

async function registration(): Promise<ServiceWorkerRegistration> {
  const existing = await navigator.serviceWorker.getRegistration("/");
  if (existing) return existing;
  return navigator.serviceWorker.register("/sw.js", { scope: "/" });
}

/** The push subscription of this browser, if any. */
export async function currentSubscription(): Promise<PushSubscription | null> {
  if (webPushUnsupportedReason()) return null;
  const reg = await navigator.serviceWorker.getRegistration("/");
  return reg ? reg.pushManager.getSubscription() : null;
}

export interface BrowserSubscription {
  endpoint: string;
  p256dh: string;
  auth: string;
  label: string;
}

/**
 * Asks for notification permission and subscribes with the hub's VAPID
 * key. A subscription made with another key is replaced.
 */
export async function subscribeBrowser(publicKey: string): Promise<BrowserSubscription> {
  const permission = await Notification.requestPermission();
  if (permission !== "granted") {
    throw new Error(permission === "denied" ? "Notifications are blocked for this site; allow them in the browser's site settings." : "Notification permission was not granted.");
  }
  const reg = await registration();
  await navigator.serviceWorker.ready;
  const key = urlBase64ToUint8Array(publicKey);
  let sub = await reg.pushManager.getSubscription();
  if (sub) {
    const current = sub.options.applicationServerKey ? new Uint8Array(sub.options.applicationServerKey) : null;
    const same = current !== null && current.length === key.length && current.every((b, i) => b === key[i]);
    if (!same) {
      await sub.unsubscribe();
      sub = null;
    }
  }
  sub ??= await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
  const json = sub.toJSON();
  if (!json.endpoint || !json.keys?.p256dh || !json.keys.auth) throw new Error("The browser returned an incomplete subscription.");
  return { endpoint: json.endpoint, p256dh: json.keys.p256dh, auth: json.keys.auth, label: deviceLabel(navigator.userAgent) };
}

/** Unsubscribes this browser and returns the endpoint it had. */
export async function unsubscribeBrowser(): Promise<string | undefined> {
  const sub = await currentSubscription();
  if (!sub) return undefined;
  const endpoint = sub.endpoint;
  await sub.unsubscribe();
  return endpoint;
}
