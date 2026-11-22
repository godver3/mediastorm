// Retry parental-control mutations only after fresh confirmation by the owner.
(() => {
    'use strict';
    const originalFetch = window.fetch.bind(window);
    const passwordHeader = 'X-Account-Password';
    let confirmation = null;
    // A confirmed password is reused for a short grace period so back-to-back
    // actions (e.g. deleting several profiles) don't re-prompt. It lives only in
    // this closure (never browser storage) and is dropped on any rejection.
    const graceMs = 5 * 60 * 1000;
    let remembered = null;
    function rememberedPassword() {
        if (remembered && Date.now() < remembered.expires) return remembered.password;
        remembered = null;
        return null;
    }

    // Describe the blocked action from the request so the prompt matches what
    // the user just did (e.g. deleting a profile) instead of generic wording.
    function describeAction(request) {
        const path = new URL(request.url).pathname;
        const method = request.method.toUpperCase();
        if (/parental|content-?rating|limits/i.test(path)) return 'change parental controls';
        if (/\/users?\b|\/profiles?\b/i.test(path)) {
            if (method === 'DELETE') return 'delete this profile';
            if (method === 'POST' && !/\/(users|profiles)\/[^/]+/i.test(path)) return 'create a profile';
            return 'change this profile';
        }
        return 'continue';
    }

    function confirmPassword(action) {
        // Concurrent requests share a single prompt.
        if (confirmation) return confirmation;
        const dialog = document.createElement('dialog');
        dialog.setAttribute('aria-label', 'Confirm account password');
        dialog.style.cssText = 'position:fixed;inset:0;margin:auto;height:fit-content;max-width:24rem;width:calc(100% - 2rem);padding:1.5rem;border:1px solid var(--border,#2a2a36);border-radius:var(--radius-lg,12px);background:var(--bg-secondary,#16161e);color:var(--text-primary,#fff);box-shadow:0 20px 50px rgba(0,0,0,0.5);';
        const style = document.createElement('style');
        style.textContent = 'dialog.parent-auth-dialog::backdrop{background:rgba(0,0,0,0.6);backdrop-filter:blur(2px);}';
        dialog.className = 'parent-auth-dialog';
        dialog.appendChild(style);
        const form = document.createElement('form');
        const title = document.createElement('h2');
        title.textContent = 'Confirm password';
        title.style.cssText = 'margin:0 0 0.5rem;font-size:1.15rem;font-weight:600;';
        const explanation = document.createElement('p');
        explanation.textContent = action === 'continue'
            ? 'Enter the account password to continue.'
            : 'Enter the account password to ' + action + '.';
        explanation.style.cssText = 'margin:0 0 1rem;font-size:0.9rem;color:var(--text-secondary,#9ca3af);line-height:1.45;';
        const label = document.createElement('label');
        label.textContent = 'Account password';
        label.style.cssText = 'display:block;font-size:0.8rem;font-weight:500;color:var(--text-secondary,#9ca3af);';
        const input = document.createElement('input');
        input.type = 'password';
        input.autocomplete = 'current-password';
        input.required = true;
        input.className = 'form-input';
        input.style.cssText = 'display:block;box-sizing:border-box;width:100%;margin:0.4rem 0 1.25rem;padding:0.65rem 0.75rem;border-radius:var(--radius,8px);border:1px solid var(--border,#2a2a36);background:var(--bg-tertiary,#1c1c26);color:var(--text-primary,#fff);font-size:1rem;';
        label.appendChild(input);
        const actions = document.createElement('div');
        actions.style.cssText = 'display:flex;justify-content:flex-end;gap:0.75rem;';
        const cancel = document.createElement('button');
        cancel.type = 'button';
        cancel.className = 'btn btn-secondary';
        cancel.textContent = 'Cancel';
        const submit = document.createElement('button');
        submit.type = 'submit';
        submit.className = 'btn ' + (action === 'delete this profile' ? 'btn-danger' : 'btn-primary');
        submit.textContent = action === 'delete this profile' ? 'Delete' : 'Confirm';
        actions.append(cancel, submit);
        form.append(title, explanation, label, actions);
        dialog.appendChild(form);
        document.body.appendChild(dialog);
        confirmation = new Promise(resolve => {
            let completed = false;
            const finish = value => {
                if (completed) return;
                completed = true;
                input.value = '';
                dialog.remove();
                confirmation = null;
                resolve(value);
            };
            form.addEventListener('submit', event => { event.preventDefault(); finish(input.value); });
            cancel.addEventListener('click', () => finish(null));
            dialog.addEventListener('cancel', event => { event.preventDefault(); finish(null); });
        });
        dialog.showModal();
        input.focus();
        return confirmation;
    }

    window.fetch = async (input, options) => {
        const request = new Request(input, options);
        const response = await originalFetch(request.clone());
        if (response.status !== 403 || request.method === 'GET' || request.headers.has(passwordHeader)) return response;
        const url = new URL(request.url);
        if (url.origin !== window.location.origin) return response;
        const body = await response.clone().json().catch(() => null);
        if (body?.code !== 'parent_authorization_required') return response;
        const retry = password => {
            const headers = new Headers(request.headers);
            const encoded = btoa(Array.from(new TextEncoder().encode(password), byte => String.fromCharCode(byte)).join(''));
            headers.set(passwordHeader, 'base64:' + encoded);
            return originalFetch(new Request(request, { headers }));
        };
        const cached = rememberedPassword();
        if (cached !== null) {
            const cachedResponse = await retry(cached);
            if (cachedResponse.status !== 403) return cachedResponse;
            remembered = null;
        }
        const password = await confirmPassword(describeAction(request));
        if (password === null) return response;
        const result = await retry(password);
        if (result.status === 403) remembered = null;
        else if (result.ok) remembered = { password, expires: Date.now() + graceMs };
        return result;
    };
})();
