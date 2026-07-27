// Admin-only behaviour, in a file rather than inline <script> blocks.
//
// This exists because of the Content-Security-Policy in main.go. A CSP that
// allows 'unsafe-inline' for scripts provides almost no XSS protection, and the
// alternative — a per-request nonce threaded through the renderer — is a lot of
// machinery for what turned out to be two duplicated listeners. Moving them
// here lets the policy stay a plain `script-src 'self'`.
//
// Loaded by admin/list.html and admin/edit.html (the two htmx-driven pages).
// admin/new.html and admin/submissions.html don't need it: new.html is a plain
// multipart POST carrying its token in a form field, and submissions.html is
// read-only.

// htmx sends the CSRF token as a header. The plain form POSTs send it as a
// `csrf` field instead; main.go's TokenLookup accepts either.
document.addEventListener('htmx:configRequest', function (e) {
	const meta = document.querySelector('meta[name="csrf-token"]');
	if (meta) e.detail.headers['X-CSRF-Token'] = meta.content;
});

// Status dropdowns submit on change. Delegated from `document` rather than
// bound to each select, because the handler for a status change swaps a
// freshly-rendered <tr> into the table — a listener attached at page load
// would be discarded with the row it was bound to, and the second change on
// any given row would do nothing.
document.addEventListener('change', function (e) {
	const el = e.target;
	if (el.matches('select[data-autosubmit]') && el.form) el.form.requestSubmit();
});
