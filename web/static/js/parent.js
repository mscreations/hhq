// Applies as early as possible (called from an inline <script> in <head>,
// before the page body renders) to avoid a flash of the wrong theme.
(function () {
  const saved = localStorage.getItem('hhq-theme');
  if (saved) {
    document.documentElement.setAttribute('data-theme', saved);
  }
})();

// Sets the active theme by name (any value from the "themes" template
// func / internal/theme.Available - not just light/dark). Persisted to
// localStorage (the client-side source of truth, read by the inline
// pre-render script above) AND mirrored to a plain, non-HttpOnly cookie
// so server-side code - specifically internal/plugins/proxy.go's
// ProxySettings, which proxies a plugin's settings page as its own
// standalone document that can't inherit this page's CSS - can read the
// current theme choice without a client round trip.
function setTheme(name) {
  document.documentElement.setAttribute('data-theme', name);
  localStorage.setItem('hhq-theme', name);
  document.cookie = 'hhq_theme=' + encodeURIComponent(name) + '; path=/; max-age=31536000; samesite=lax';
  document.querySelectorAll('#theme-select').forEach((sel) => { sel.value = name; });
}

document.addEventListener('DOMContentLoaded', () => {
  const current = document.documentElement.getAttribute('data-theme') ||
    (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
  document.querySelectorAll('#theme-select').forEach((sel) => { sel.value = current; });
  updateChoreKindFields();
  updateChoreSelection();
  updateChoreAssigneeFields();
});

function openSettingsModal() {
  const overlay = document.getElementById('settings-modal-overlay');
  if (overlay) overlay.hidden = false;
}

function closeSettingsModal() {
  const overlay = document.getElementById('settings-modal-overlay');
  if (overlay) overlay.hidden = true;
}

function closePluginErrorModal() {
  const overlay = document.getElementById('plugin-error-modal-overlay');
  if (overlay) overlay.hidden = true;
}

function openAvatarModal(id) {
  const overlay = document.getElementById('avatar-modal-overlay-' + id);
  if (overlay) overlay.hidden = false;
}

function closeAvatarModal(id) {
  const overlay = document.getElementById('avatar-modal-overlay-' + id);
  if (overlay) overlay.hidden = true;
}

// Closes every avatar modal, not just one by id - used by the Escape-key
// handler below, which has no specific id to target, and as a belt-and-
// suspenders cleanup after an htmx swap (see the htmx:afterSwap listener
// below) in case more than one somehow ended up open.
function closeAllAvatarModals() {
  document.querySelectorAll('.avatar-modal-overlay').forEach((overlay) => {
    overlay.hidden = true;
  });
}

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') {
    closeSettingsModal();
    closePluginErrorModal();
    closeAllAvatarModals();
  }
});

// The Children/Parents cards re-render their whole fragment (including every
// per-row avatar modal, freshly closed) after any avatar upload/remove - see
// respondAfterMutation's outerHTML swap in internal/handlers/avatar.go. This
// is also true on a validation error (e.g. a rejected file type), so the
// modal always closes and the error message surfaces at the top of the card
// instead - acceptable, since there's no per-user error state to reopen the
// right modal against.
document.body.addEventListener('htmx:afterSwap', (evt) => {
  const id = evt.detail && evt.detail.target && evt.detail.target.id;
  if (id === 'children-card' || id === 'parents-card') closeAllAvatarModals();
});

// Close the settings modal after a successful save. The server re-renders
// the same "settings-card" fragment on validation failure (e.g. bad
// timezone) with a `class="error"` message rather than returning a non-2xx
// status (so htmx still swaps the error in) - checking the raw response
// text for that marker, rather than the swapped-in DOM node, sidesteps any
// ambiguity about whether the outerHTML swap has replaced the node by the
// time this listener runs.
//
// Listens on "htmx:beforeSwap", not "htmx:afterRequest": hx-target
// ("#settings-card") is an ANCESTOR of hx-post's own element
// ("#settings-form"), so the outerHTML swap replaces the form itself. By
// the time "afterRequest" fires (after the swap), the original form node
// is already detached from the document, and a detached node's dispatched
// event has no ancestors to bubble to - so a document.body listener for
// "afterRequest" never runs. "beforeSwap" fires first, while the form is
// still attached, so bubbling to document.body works.
document.body.addEventListener('htmx:beforeSwap', (evt) => {
  const elt = evt.detail && evt.detail.elt;
  if (!elt || elt.id !== 'settings-card') return;
  const xhr = evt.detail.xhr;
  const hasError = xhr && xhr.responseText && xhr.responseText.indexOf('class="error"') !== -1;
  if (!hasError) closeSettingsModal();
});

function updateChoreKindFields() {
  const kind = document.getElementById('chore-kind');
  const daysPicker = document.getElementById('chore-days-picker');
  const oneOffDate = document.getElementById('chore-one-off-date');
  if (!kind || !daysPicker || !oneOffDate) return;
  const isOneOff = kind.value === 'one_off';
  daysPicker.style.display = isOneOff ? 'none' : '';
  oneOffDate.style.display = isOneOff ? '' : 'none';
}

// Drives the "Add Chore" form's chore-name field: selecting an existing
// catalog chore hides the new-name input and shows its description
// read-only (so assigning it to another child can't accidentally fork the
// description); selecting "+ New chore..." shows an editable name input and
// clears the description for the parent to fill in.
function updateChoreSelection() {
  const select = document.getElementById('chore-select');
  const nameInput = document.getElementById('new-chore-name');
  const description = document.getElementById('chore-description');
  if (!select || !nameInput || !description) return;
  const isNew = select.value === 'new';
  nameInput.style.display = isNew ? '' : 'none';
  nameInput.required = isNew;
  if (isNew) {
    description.value = '';
    description.readOnly = false;
  } else {
    const opt = select.options[select.selectedIndex];
    description.value = (opt && opt.dataset.description) || '';
    description.readOnly = true;
  }
}

// Drives the "Add Chore" form's points field: assigning to a parent (see
// the "data-role" attribute on each <option> in
// web/templates/parent/_chore_defs.html's "_chore_defs_inner") makes the
// chore informational only, so the entire "Points" label+input (see
// "#chore-points-wrap") is hidden rather than just disabled - a disabled-
// but-visible field with a leftover "1" in it still reads as "this chore is
// worth 1 point" at a glance.
function updateChoreAssigneeFields() {
  const select = document.getElementById('chore-assignee-select');
  const wrap = document.getElementById('chore-points-wrap');
  const points = document.getElementById('chore-points');
  if (!select || !wrap || !points) return;
  const opt = select.options[select.selectedIndex];
  const isParent = opt && opt.dataset.role === 'parent';
  wrap.style.display = isParent ? 'none' : '';
  points.disabled = isParent;
  points.required = !isParent;
  if (isParent) points.value = 0;
  else if (points.value === '0') points.value = 1;
}

// Toggles a chore catalog row between its view row and its inline edit form
// (see web/templates/parent/_chore_defs.html's "_chore_catalog_inner").
function toggleChoreEdit(id) {
  const row = document.getElementById('chore-catalog-row-' + id);
  const edit = document.getElementById('chore-catalog-edit-' + id);
  if (!row || !edit) return;
  const showingEdit = edit.style.display !== 'none';
  row.style.display = showingEdit ? '' : 'none';
  edit.style.display = showingEdit ? 'none' : '';
}
