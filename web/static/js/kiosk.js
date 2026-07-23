// Panel refresh and chore-tap-to-complete are handled declaratively by htmx
// (see hx-get/hx-trigger on the panel divs and hx-post on chore <li>s in
// web/templates/kiosk/index.html and _chores.html). What's left here: the
// clock, the chore confirmation popup, and marking which kiosk-navbar button
// is the active view.

// Flips the active .nav-btn class before an hx-get swaps #kiosk-view, so the
// bar reflects the view being requested without waiting for the response.
function setActiveNav(name) {
  document.querySelectorAll('.nav-btn[data-nav]').forEach(function(btn) {
    btn.classList.toggle('active', btn.dataset.nav === name);
  });
}
function updateClock() {
  const el = document.getElementById('clock');
  if (!el) return;
  const now = new Date();
  const weekday = now.toLocaleDateString([], { weekday: 'long' });
  const month = now.toLocaleDateString([], { month: 'long' });
  const time = now.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
  el.textContent = `${weekday} ${month} ${now.getDate()}, ${now.getFullYear()} ${time}`;
}
updateClock();
setInterval(updateClock, 10 * 1000);

// Chore completion confirmation popup. Chores with a description (job
// expectations set by a parent, see web/templates/kiosk/_chores.html) don't
// tap-to-complete directly - they open this modal first so the child sees
// what's expected before confirming.
let pendingChoreID = null;

function openChoreConfirm(id, name, description) {
  pendingChoreID = id;
  document.getElementById('chore-confirm-name').textContent = name;
  document.getElementById('chore-confirm-description').textContent = description;
  document.getElementById('chore-confirm-modal').classList.add('open');
}

function closeChoreConfirm() {
  pendingChoreID = null;
  document.getElementById('chore-confirm-modal').classList.remove('open');
}

function confirmChoreComplete() {
  if (pendingChoreID === null) return;
  htmx.ajax('POST', `/kiosk/chores/${pendingChoreID}/complete`, {
    target: '#chores-content',
    swap: 'innerHTML',
  });
  closeChoreConfirm();
}
