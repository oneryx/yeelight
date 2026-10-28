'use strict';

// Color modes reported by the light.
const MODE_RGB = 1;
const MODE_CT = 2;
const MODE_HSV = 3;

// How long the controls keep the user's last edit over the state read from
// the light, which may predate it.
const EDIT_GRACE = 1500;
// The least time between two values of a slider sent while it's dragged.
const SEND_INTERVAL = 100;
// How long a delete button waits for the tap confirming it.
const CONFIRM_TIME = 3000;
// Where the page keeps the server's last state, to show it while loading.
const STATE_KEY = 'state';

const $ = (id) => document.getElementById(id);
const els = {
    name: $('name'),
    statusLine: $('status-line'),
    spinner: $('spinner'),
    refresh: $('refresh'),
    power: $('power'),
    powerStatus: $('power-status'),
    controls: $('controls'),
    modes: document.querySelectorAll('.segmented button'),
    bright: $('bright'),
    ct: $('ct'),
    hue: $('hue'),
    sat: $('sat'),
    schemeList: $('scheme-list'),
    schemeEmpty: $('scheme-empty'),
    schemeSave: $('scheme-save'),
    schemeName: $('scheme-name'),
    schemeSaveButton: $('scheme-save-button'),
    settings: $('settings'),
    address: $('address'),
};

// What the page shows: the server's state as last read, and the controls' values.
const app = {
    server: { loading: true, connected: false, error: '', props: {}, address: '', schemes: [] },
    error: '', // why the last request to the server failed
    power: false,
    tab: 'scheme', // the tab shown: 'scheme', 'white' or 'color'
    mode: 'white', // the mode whose sliders show, 'white' or 'color'
    lightMode: 'white', // the mode the light shines in, as last set or read
    bright: 50,
    ct: 4000,
    hue: 0,
    sat: 100,
    editedAt: 0,
    addressEdited: false, // the address field holds the user's text
    renaming: null, // the name of the scheme being renamed
    renameValue: '', // the new name typed for it
    confirming: null, // the name of the scheme whose delete waits for a confirmation
    confirmTimer: null,
    schemesShown: '', // what the scheme list was last built from
    rebuilding: false, // the scheme list is being rebuilt
};

// Colors

// kelvinColor approximates the color of white light at a temperature, after
// Tanner Helland's fit of blackbody radiation, as [r, g, b].
function kelvinColor(k) {
    const t = k / 100;
    let r, g, b;
    if (t <= 66) {
        r = 255;
        g = 99.4708025861 * Math.log(t) - 161.1195681661;
    } else {
        r = 329.698727446 * Math.pow(t - 60, -0.1332047592);
        g = 288.1221695283 * Math.pow(t - 60, -0.0755148492);
    }
    if (t >= 66) b = 255;
    else if (t <= 19) b = 0;
    else b = 138.5177312231 * Math.log(t - 10) - 305.0447927307;
    return [r, g, b].map(clamp8);
}

// hsvColor returns the color of hue 0-360, saturation and value 0-1, as [r, g, b].
function hsvColor(h, s, v) {
    h = (h % 360) / 60;
    const c = v * s;
    const x = c * (1 - Math.abs((h % 2) - 1));
    const rgb = [[c, x, 0], [x, c, 0], [0, c, x], [0, x, c], [x, 0, c], [c, 0, x]][Math.floor(h)] || [c, 0, x];
    const m = v - c;
    return rgb.map((u) => clamp8((u + m) * 255));
}

// rgbHueSat returns the hue 0-359 and saturation 0-100 of a 0xRRGGBB color.
function rgbHueSat(rgb) {
    const r = ((rgb >> 16) & 0xff) / 255;
    const g = ((rgb >> 8) & 0xff) / 255;
    const b = (rgb & 0xff) / 255;
    const hi = Math.max(r, g, b);
    const d = hi - Math.min(r, g, b);
    if (hi === 0 || d === 0) return [0, 0];
    let h;
    if (hi === r) h = (((g - b) / d) % 6 + 6) % 6;
    else if (hi === g) h = (b - r) / d + 2;
    else h = (r - g) / d + 4;
    return [Math.round(h * 60) % 360, Math.round((d / hi) * 100)];
}

const clamp8 = (v) => Math.max(0, Math.min(255, Math.round(v)));
const css = ([r, g, b]) => `rgb(${r}, ${g}, ${b})`;
const gradient = (stops) => `linear-gradient(to right, ${stops.join(', ')})`;

// lightColor is the color the light shines in, as the controls set it.
function lightColor() {
    return app.mode === 'white' ? kelvinColor(app.ct) : hsvColor(app.hue, app.sat / 100, 1);
}

// Server

// apply shows the server's state.
function apply(st) {
    const prevMode = app.server.props.color_mode;
    app.server = st;
    app.error = '';
    if (!app.addressEdited && document.activeElement !== els.address) {
        els.address.value = st.address;
    }
    if (st.connected && Date.now() - app.editedAt >= EDIT_GRACE) {
        takeProps(st.props, prevMode);
    }
    try {
        localStorage.setItem(STATE_KEY, JSON.stringify(st));
    } catch {}
    render();
}

// takeProps sets the controls to the light's state.
function takeProps(p, prevMode) {
    app.power = p.on;
    app.bright = p.bright;
    app.ct = p.ct;
    // The hue and saturation the light reports in white mode are stale:
    // keep those the sliders show.
    if (p.color_mode === MODE_HSV) {
        [app.hue, app.sat] = [p.hue, p.sat];
    } else if (p.color_mode === MODE_RGB) {
        [app.hue, app.sat] = rgbHueSat(p.rgb);
    }
    app.lightMode = p.color_mode === MODE_CT ? 'white' : 'color';
    // Follow the light's mode only when it changes, so that a mode chosen
    // here stays while the light keeps shining as before.
    if (p.color_mode !== prevMode) {
        app.mode = app.lightMode;
        if (app.tab !== 'scheme') app.tab = app.mode;
    }
}

// restore shows the state saved by the last visit, with the controls waiting
// for the server, so that the page shows at once.
function restore() {
    let st;
    try {
        st = JSON.parse(localStorage.getItem(STATE_KEY));
    } catch {}
    if (!st || !st.props) return;
    if (st.props.color_mode) takeProps(st.props);
    app.server = { ...st, loading: true, connected: false, error: '' };
    els.address.value = st.address || '';
}

// watch keeps the page showing the server's state: each request waits until
// the state changes from the version the page has.
async function watch() {
    let version = 0;
    for (;;) {
        try {
            const res = await fetch(`api/state?wait=${version}`, { cache: 'no-store' });
            await check(res);
            const st = await res.json();
            version = st.version;
            apply(st);
        } catch (err) {
            version = 0;
            app.server = { ...app.server, loading: false, connected: false };
            app.error = `Can't reach the server: ${err.message}`;
            render();
            await new Promise((resolve) => setTimeout(resolve, 2000));
        }
    }
}

// check throws the server's {"error": "..."} of a failed response.
async function check(res) {
    if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        throw new Error(body.error || `${res.status} ${res.statusText}`);
    }
}

// send sends a request to the server, and returns whether it succeeded.
async function send(path, body, method = 'POST') {
    try {
        const res = await fetch(path, {
            method,
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body ?? {}),
        });
        await check(res);
        return true;
    } catch (err) {
        app.error = err.message;
        render();
        return false;
    }
}

// throttled returns fn that runs at most once per SEND_INTERVAL, with the
// last arguments it was called with.
function throttled(fn) {
    let last = 0, timer = null, args;
    return (...a) => {
        args = a;
        if (timer) return;
        const wait = Math.max(0, last + SEND_INTERVAL - Date.now());
        timer = setTimeout(() => {
            timer = null;
            last = Date.now();
            fn(...args);
        }, wait);
    };
}

const sendBright = throttled(() => send('api/bright', { value: app.bright }));
// sendColor sends the color temperature or the color the controls set.
const sendColor = throttled(() => app.mode === 'white'
    ? send('api/ct', { value: app.ct })
    : send('api/hsv', { hue: app.hue, sat: app.sat }));

// Controls

function edited() {
    app.editedAt = Date.now();
}

// turnOn switches the light on if it's off, before a slider sends its value,
// which the light only takes while on.
function turnOn() {
    if (app.power) return;
    app.power = true;
    send('api/power', { on: true });
}

els.power.addEventListener('click', () => {
    app.power = !app.power;
    edited();
    render();
    send('api/power', { on: app.power });
});

els.refresh.addEventListener('click', () => send('api/refresh'));

// Choosing a mode shows its controls without changing the light: its
// sliders send their values once moved. Color starts from the color of the
// white light. Scheme shows the saved schemes, leaving the mode as it is.
for (const b of els.modes) {
    b.addEventListener('click', () => {
        if (b.dataset.mode === app.tab) return;
        app.tab = b.dataset.mode;
        if (app.tab === 'scheme' || app.tab === app.mode) {
            render();
            return;
        }
        app.mode = app.tab;
        if (app.mode === 'color' && app.server.props.color_mode === MODE_CT) {
            const [r, g, bl] = kelvinColor(app.ct);
            [app.hue, app.sat] = rgbHueSat((r << 16) | (g << 8) | bl);
        }
        render();
    });
}

els.bright.addEventListener('input', () => {
    app.bright = +els.bright.value;
    edited();
    turnOn();
    render();
    sendBright();
});

for (const key of ['ct', 'hue', 'sat']) {
    els[key].addEventListener('input', () => {
        app[key] = +els[key].value;
        app.lightMode = app.mode;
        edited();
        turnOn();
        render();
        sendColor();
    });
}

// Schemes

const schemePath = (name) => `api/schemes/${encodeURIComponent(name)}`;
const findScheme = (name) => app.server.schemes.find((sc) => sc.name === name);

// isCurrent returns whether the light shines as the scheme sets it.
function isCurrent(sc) {
    return app.power && sc.mode === app.lightMode && sc.bright === app.bright &&
        (sc.mode === 'white' ? sc.ct === app.ct : sc.hue === app.hue && sc.sat === app.sat);
}

function applyScheme(sc) {
    app.power = true;
    app.mode = app.lightMode = sc.mode;
    app.bright = sc.bright;
    if (sc.mode === 'white') app.ct = sc.ct;
    else [app.hue, app.sat] = [sc.hue, sc.sat];
    edited();
    render();
    send(`${schemePath(sc.name)}/apply`);
}

function startRename(name) {
    app.renaming = name;
    app.renameValue = name;
    render();
}

function finishRename(save) {
    const name = app.renaming;
    if (name === null) return;
    const to = app.renameValue.trim();
    app.renaming = null;
    render();
    if (save && to && to !== name) send(schemePath(name), { name: to }, 'PUT');
}

// deleteScheme deletes the scheme on the second tap of its delete button.
function deleteScheme(name) {
    clearTimeout(app.confirmTimer);
    if (app.confirming !== name) {
        app.confirming = name;
        app.confirmTimer = setTimeout(() => {
            app.confirming = null;
            render();
        }, CONFIRM_TIME);
        render();
        return;
    }
    app.confirming = null;
    render();
    send(schemePath(name), null, 'DELETE');
}

els.schemeName.addEventListener('input', render);

// Saving stores the light's setting as it shines, whichever tab was shown.
els.schemeSave.addEventListener('submit', async (e) => {
    e.preventDefault();
    const name = els.schemeName.value.trim();
    if (!name) return;
    const ok = await send('api/schemes', {
        name, mode: app.lightMode, bright: app.bright, ct: app.ct, hue: app.hue, sat: app.sat,
    });
    if (ok) {
        els.schemeName.value = '';
        els.schemeName.blur();
        render();
    }
});

els.address.addEventListener('input', () => {
    app.addressEdited = true;
});

els.settings.addEventListener('submit', (e) => {
    e.preventDefault();
    const address = els.address.value.trim();
    els.address.value = address;
    els.address.blur();
    app.addressEdited = false;
    send('api/address', { address }, 'PUT');
});

// Rendering

function render() {
    const st = app.server;
    const p = st.props;
    const connected = st.connected;

    // While connecting, the name is the one last read.
    els.name.textContent = ((connected || st.loading) && p.name) || 'Yeelight';
    document.title = els.name.textContent;
    const error = app.error || st.error;
    els.statusLine.classList.toggle('error', !st.loading && !!error);
    els.statusLine.textContent = !st.loading ? error || p.host || ''
        : st.address ? `Connecting to ${st.address}…` : 'Looking for the light…';
    els.spinner.classList.toggle('active', st.loading);

    const on = app.power && connected;
    const glow = lightColor();
    els.power.disabled = !connected;
    els.power.classList.toggle('on', on);
    els.power.setAttribute('aria-pressed', on);
    els.power.style.setProperty('--glow', css(glow));
    els.power.style.setProperty('--level', 0.35 + 0.65 * app.bright / 100);
    els.powerStatus.textContent = connected ? (on ? `On · ${app.bright}%` : 'Off')
        : st.loading ? 'Connecting…' : 'Not connected';

    // The controls work while the light is off: they switch it on. They
    // only wait while the light can't be reached.
    els.controls.inert = !connected;
    els.controls.classList.toggle('disabled', !connected);
    for (const b of els.modes) {
        b.setAttribute('aria-checked', b.dataset.mode === app.tab);
        b.disabled = !connected;
    }
    for (const el of els.controls.querySelectorAll('[data-for]')) {
        el.hidden = !el.dataset.for.split(' ').includes(app.tab);
    }

    slider('bright', `${app.bright}%`, ['rgb(40, 40, 40)', css(glow)]);
    const temps = [];
    for (let k = 1700; k <= 6500; k += 800) temps.push(css(kelvinColor(k)));
    slider('ct', `${app.ct} K`, temps);
    const hues = [];
    for (let h = 0; h <= 360; h += 30) hues.push(css(hsvColor(h, 1, 1)));
    slider('hue', `${app.hue}°`, hues);
    slider('sat', `${app.sat}%`, ['rgb(255, 255, 255)', css(hsvColor(app.hue, 1, 1))]);
    for (const el of [els.bright, els.ct, els.hue, els.sat]) el.disabled = !connected;

    renderSchemes();
}

function renderSchemes() {
    const schemes = app.server.schemes || [];
    const name = els.schemeName.value.trim();
    els.schemeSaveButton.disabled = !name;
    els.schemeSaveButton.textContent = findScheme(name) ? 'Replace' : 'Save';
    els.schemeEmpty.hidden = schemes.length > 0;

    // Rebuild the list only when it changes, which keeps a rename's field.
    const current = schemes.map(isCurrent);
    const shown = JSON.stringify([schemes, current, app.renaming, app.confirming]);
    if (shown === app.schemesShown) return;
    app.schemesShown = shown;

    const items = schemes.map((sc, i) => {
        const swatch = node('span', { className: 'swatch' });
        swatch.style.background = css(sc.mode === 'white' ? kelvinColor(sc.ct) : hsvColor(sc.hue, sc.sat / 100, 1));
        const detail = sc.mode === 'white' ? `${sc.bright}% · ${sc.ct} K` : `${sc.bright}% · ${sc.hue}° · ${sc.sat}%`;

        let main;
        if (sc.name === app.renaming) {
            const input = node('input', {
                className: 'text', type: 'text', value: app.renameValue, maxLength: 40,
                autocomplete: 'off', spellcheck: false, ariaLabel: 'New name',
            });
            input.addEventListener('input', () => { app.renameValue = input.value; });
            input.addEventListener('keydown', (e) => {
                if (e.key === 'Enter') finishRename(true);
                else if (e.key === 'Escape') finishRename(false);
            });
            // Removing the field blurs it: only the user's blur ends the rename.
            input.addEventListener('blur', () => {
                if (!app.rebuilding) finishRename(true);
            });
            main = node('div', { className: 'scheme-main' }, swatch, input);
            requestAnimationFrame(() => {
                if (!input.isConnected) return;
                input.focus();
                input.select();
            });
        } else {
            main = node('button', { className: 'scheme-main', title: `Set the light to ${sc.name}` },
                swatch,
                node('span', { className: 'scheme-text' },
                    node('span', { className: 'scheme-name', textContent: sc.name }),
                    node('span', { className: 'small muted', textContent: detail })),
                current[i] ? icon(['M20 6 9 17l-5-5'], 'check') : '');
            main.addEventListener('click', () => applyScheme(sc));
        }

        const rename = node('button', { className: 'icon-button', ariaLabel: `Rename ${sc.name}`, title: 'Rename' },
            icon(['M17 3a2.85 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z']));
        // Keep the field of a rename focused, so that a tap here ends it.
        rename.addEventListener('mousedown', (e) => e.preventDefault());
        rename.addEventListener('click', () => {
            if (app.renaming === sc.name) finishRename(true);
            else startRename(sc.name);
        });

        const confirming = app.confirming === sc.name;
        const del = node('button', {
            className: confirming ? 'button danger' : 'icon-button',
            ariaLabel: confirming ? `Confirm deleting ${sc.name}` : `Delete ${sc.name}`,
            title: 'Delete',
        }, confirming ? 'Delete' : icon(['M3 6h18', 'M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6', 'M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2']));
        del.addEventListener('click', () => deleteScheme(sc.name));

        return node('li', { className: 'scheme' }, main, rename, del);
    });
    app.rebuilding = true;
    try {
        els.schemeList.replaceChildren(...items);
    } finally {
        app.rebuilding = false;
    }
}

// node returns a new element with the properties and children.
function node(tag, props, ...children) {
    const el = Object.assign(document.createElement(tag), props);
    el.append(...children);
    return el;
}

// icon returns an SVG icon drawing the paths.
function icon(paths, className = '') {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('aria-hidden', 'true');
    if (className) svg.classList.add(className);
    for (const d of paths) {
        const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
        path.setAttribute('d', d);
        svg.append(path);
    }
    return svg;
}

function slider(key, shown, stops) {
    const el = els[key];
    if (+el.value !== app[key]) el.value = app[key];
    el.style.setProperty('--track', gradient(stops));
    $(`${key}-value`).textContent = shown;
}

restore();
render();
watch();

// The service worker needs HTTPS, and is left out while editing on localhost.
if ('serviceWorker' in navigator && location.protocol === 'https:') {
    navigator.serviceWorker.register('sw.js');
}
