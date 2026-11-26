/* Asterism service worker: shows Web Push alert notifications.
 * Served at /sw.js with scope "/" (internal/web sets no-cache and
 * Service-Worker-Allowed). The payload is notify.PushPayload JSON. */
self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (event) => event.waitUntil(self.clients.claim()));

self.addEventListener("push", (event) => {
  let data = {};
  try {
    data = event.data ? event.data.json() : {};
  } catch {
    data = { title: "Asterism", body: event.data ? event.data.text() : "" };
  }
  const title = data.title || "Asterism alert";
  const options = {
    body: data.body || "",
    tag: data.tag || "asterism",
    renotify: true,
    requireInteraction: data.severity === "critical",
    icon: "/icon-192.png",
    badge: "/icon-192.png",
    timestamp: data.ts || Date.now(),
    data: { url: typeof data.url === "string" && data.url.startsWith("/") ? data.url : "/alerts" },
  };
  event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const target = new URL(event.notification.data?.url || "/alerts", self.location.origin).href;
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((windows) => {
      for (const w of windows) {
        if (new URL(w.url).origin === self.location.origin && "focus" in w) {
          w.navigate(target);
          return w.focus();
        }
      }
      return self.clients.openWindow(target);
    }),
  );
});
