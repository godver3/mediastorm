const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../static/account-parent-auth-v1.js'), 'utf8');

function app(expectedPassword = 'fixture password') {
    const dialogs = [], calls = [];
    class Element {
        constructor(tag) { this.tagName = tag; this.children = []; this.events = {}; this.style = {}; this.value = ''; }
        append(...children) { this.children.push(...children); }
        appendChild(child) { this.append(child); }
        setAttribute(name, value) { this[name] = value; }
        addEventListener(name, fn) { this.events[name] = fn; }
        showModal() { dialogs.push(this); }
        focus() {}
        remove() { this.removed = true; }
    }
    const window = { location: { origin: 'https://server.test' }, fetch: async request => {
        calls.push({ method: request.method, url: request.url, body: await request.clone().text(), password: request.headers.get('X-Account-Password') });
        if (request.url.endsWith('/unrelated')) return new Response('plain forbidden', { status: 403 });
        const header = request.headers.get('X-Account-Password') || '';
        const password = header.startsWith('base64:') ? Buffer.from(header.slice(7), 'base64').toString('utf8') : header;
        const accepted = password === expectedPassword;
        return new Response(JSON.stringify(accepted ? { updated: true } : { code: 'parent_authorization_required' }), { status: accepted ? 200 : 403, headers: { 'Content-Type': 'application/json' } });
    } };
    const context = vm.createContext({ window, document: { body: new Element('body'), createElement: tag => new Element(tag) }, Request, Response, Headers, URL, Promise, TextEncoder, btoa });
    vm.runInContext(source, context);
    const fetch = (url = 'https://server.test/account/api/profiles/kids', options = {}) => window.fetch(url, { method: 'PUT', body: '{"isKidsProfile":false}', ...options });
    async function dialog() {
        for (let i = 0; i < 1000; i++) {
            const active = dialogs.find(d => !d.removed);
            if (active) return active;
            await new Promise(resolve => setTimeout(resolve, 1));
        }
        throw new Error('parent confirmation was not opened');
    }
    async function respond(password) {
        const d = await dialog(), form = d.children[0];
        const input = form.children.find(e => e.tagName === 'label').children[0];
        assert.equal(input.type, 'password');
        if (password === null) form.children.find(e => e.textContent === 'Cancel').events.click();
        else { input.value = password; form.events.submit({ preventDefault() {} }); }
        assert.equal(input.value, '', 'password input must be cleared');
        assert.equal(d.removed, true, 'prompt must be removed');
    }
    return { fetch, dialog, respond, dialogs, calls };
}

test('confirmed retry retains the original method and body', async () => {
    const p = app(), pending = p.fetch();
    await p.respond('fixture password');
    assert.equal((await pending).status, 200);
    assert.equal(p.calls.length, 2);
    assert.equal(p.calls[0].password, null);
    assert.equal(p.calls[1].method, 'PUT');
    assert.equal(p.calls[1].body, '{"isKidsProfile":false}');
    const again = p.fetch();
    await p.respond(null);
    assert.equal((await again).status, 403, 'prior confirmation must not authorize a later action');
    assert.equal(p.dialogs.length, 2);
});

test('cancel does not send a password or retry the mutation', async () => {
    const p = app(), pending = p.fetch();
    await p.respond(null);
    assert.equal((await pending).status, 403);
    assert.equal(p.calls.length, 1);
});

test('wrong confirmation is reported without an automatic prompt loop', async () => {
    const p = app(), pending = p.fetch();
    await p.respond('incorrect');
    assert.equal((await pending).status, 403);
    assert.equal(p.dialogs.length, 1);
    assert.equal(p.calls.length, 2);
});

test('concurrent mutations share one active prompt without retaining credentials', async () => {
    const p = app(), one = p.fetch(), two = p.fetch();
    await p.dialog();
    await new Promise(resolve => setTimeout(resolve, 20));
    await p.respond('fixture password');
    assert.equal((await one).status, 200);
    assert.equal((await two).status, 200);
    assert.equal(p.dialogs.length, 1);
    assert.equal(p.calls.filter(c => c.password !== null).length, 2);
});

test('external challenges and unrelated forbidden responses never ask for a password', async () => {
    const p = app();
    assert.equal((await p.fetch('https://other.test/mutation')).status, 403);
    assert.equal(await (await p.fetch('https://server.test/unrelated')).text(), 'plain forbidden');
    assert.equal(p.dialogs.length, 0);
    assert.ok(p.calls.every(c => c.password === null));
});

test('an explicitly supplied invalid password is not silently retried', async () => {
    const p = app();
    assert.equal((await p.fetch(undefined, { headers: { 'X-Account-Password': 'incorrect' } })).status, 403);
    assert.equal(p.dialogs.length, 0);
    assert.equal(p.calls.length, 1);
});

test('Unicode passwords and surrounding whitespace survive header transport', async () => {
    const password = '  fixture パスワード  ';
    const p = app(password), pending = p.fetch();
    await p.respond(password);
    assert.equal((await pending).status, 200);
    assert.match(p.calls[1].password, /^base64:[A-Za-z0-9+/]+=*$/);
});
