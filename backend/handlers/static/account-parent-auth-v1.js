// Retry parental-control mutations only after fresh confirmation by the owner.
(() => {
    'use strict';
    const originalFetch = window.fetch.bind(window);
    const passwordHeader = 'X-Account-Password';
    let confirmation = null;

    function confirmPassword() {
        // Concurrent requests share a single prompt, but credentials are never
        // cached for a later action or written to browser storage.
        if (confirmation) return confirmation;
        const dialog = document.createElement('dialog');
        dialog.setAttribute('aria-label', 'Confirm account password');
        dialog.style.cssText = 'max-width:26rem;width:calc(100% - 3rem);padding:1.5rem;border:1px solid #444;border-radius:12px;background:#18181b;color:#fafafa;';
        const form = document.createElement('form');
        const title = document.createElement('h2');
        title.textContent = 'Parent confirmation';
        const explanation = document.createElement('p');
        explanation.textContent = 'Enter the account password to change parental controls or manage profiles.';
        const label = document.createElement('label');
        label.textContent = 'Account password';
        const input = document.createElement('input');
        input.type = 'password';
        input.autocomplete = 'current-password';
        input.required = true;
        input.style.cssText = 'display:block;box-sizing:border-box;width:100%;margin:0.6rem 0 1rem;padding:0.65rem;border-radius:6px;';
        label.appendChild(input);
        const cancel = document.createElement('button');
        cancel.type = 'button';
        cancel.textContent = 'Cancel';
        const submit = document.createElement('button');
        submit.type = 'submit';
        submit.textContent = 'Confirm';
        submit.style.marginLeft = '0.75rem';
        form.append(title, explanation, label, cancel, submit);
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
        const password = await confirmPassword();
        if (password === null) return response;
        const headers = new Headers(request.headers);
        const encoded = btoa(Array.from(new TextEncoder().encode(password), byte => String.fromCharCode(byte)).join(''));
        headers.set(passwordHeader, 'base64:' + encoded);
        return originalFetch(new Request(request, { headers }));
    };
})();
