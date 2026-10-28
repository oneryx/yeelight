'use strict';

// The service worker shows the page from its cache, so an installed app opens
// at once instead of waiting on the network, and updates the cache in the
// background: a new version of the page shows from the next launch. The API
// always goes to the server.

const CACHE = 'yeelight';
const SHELL = ['./', 'style.css', 'app.js', 'manifest.json', 'icon.svg', 'icon-512.png', 'apple-touch-icon.png'];

self.addEventListener('install', (e) => {
    e.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener('activate', (e) => {
    e.waitUntil(self.clients.claim());
});

self.addEventListener('fetch', (e) => {
    const url = new URL(e.request.url);
    if (e.request.method !== 'GET' || url.origin !== location.origin || url.pathname.includes('/api/')) return;
    e.respondWith(caches.open(CACHE).then(async (c) => {
        const cached = await c.match(e.request, { ignoreSearch: true });
        const fresh = fetch(e.request, { cache: 'no-cache' }).then((res) => {
            if (res.ok) c.put(e.request, res.clone());
            return res;
        });
        if (!cached) return fresh;
        e.waitUntil(fresh.catch(() => {}));
        return cached;
    }));
});
