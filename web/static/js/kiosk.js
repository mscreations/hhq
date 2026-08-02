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
// expectations set by a parent, see web/templates/kiosk/_chores.html and
// _week_day_chores.html) don't tap-to-complete directly - they open this
// modal first so the child sees what's expected before confirming.
let pendingChoreID = null;
// pendingChoreDay is set when the confirm was opened from the 5-day view
// (see _week_day_chores.html's openChoreConfirm(..., 'week', dateKey) call),
// so confirmChoreComplete knows to target that day's chore column instead of
// the classic view's single #chores-content.
let pendingChoreDay = null;

function openChoreConfirm(id, name, description, view, day) {
  pendingChoreID = id;
  pendingChoreDay = view === 'week' ? day : null;
  document.getElementById('chore-confirm-name').textContent = name;
  document.getElementById('chore-confirm-description').textContent = description;
  document.getElementById('chore-confirm-modal').classList.add('open');
}

function closeChoreConfirm() {
  pendingChoreID = null;
  pendingChoreDay = null;
  document.getElementById('chore-confirm-modal').classList.remove('open');
}

function confirmChoreComplete() {
  if (pendingChoreID === null) return;
  if (pendingChoreDay) {
    htmx.ajax('POST', `/kiosk/chores/${pendingChoreID}/complete?view=week&day=${pendingChoreDay}`, {
      target: `#week-chores-${pendingChoreDay}`,
      swap: 'outerHTML',
    });
  } else {
    htmx.ajax('POST', `/kiosk/chores/${pendingChoreID}/complete`, {
      target: '#chores-content',
      swap: 'innerHTML',
    });
  }
  closeChoreConfirm();
}

// --- 5-day view: "now" line + full-24h auto-scroll ---
//
// The surrounding fragment already refreshes every 60s via htmx (see
// kiosk/_week.html), which would eventually reposition the now-line, but a
// lighter independent timer (same 10s cadence as updateClock above) gives
// smoother movement between polls without re-fetching anything.

function updateNowLine() {
  const container = document.querySelector('.kiosk-week');
  if (!container) return;
  const mode = container.dataset.gridMode;
  const startHour = mode === 'full24' ? 0 : parseInt(container.dataset.gridStart, 10);
  const endHour = mode === 'full24' ? 24 : parseInt(container.dataset.gridEnd, 10);
  const rangeStart = startHour * 60;
  const rangeEnd = endHour * 60;
  const now = new Date();
  const nowMin = Math.min(Math.max(now.getHours() * 60 + now.getMinutes(), rangeStart), rangeEnd);
  const pct = ((nowMin - rangeStart) / (rangeEnd - rangeStart)) * 100;
  container.querySelectorAll('.now-line').forEach(function (line) {
    line.style.top = pct + '%';
  });
}
updateNowLine();
setInterval(updateNowLine, 10 * 1000);

// The 60s poll (see kiosk/_week.html's hx-trigger) replaces the day-columns
// subtree wholesale, including every #now-line-<date> element, so a fresh
// call is needed right after each swap rather than relying on the interval
// alone to catch a freshly-recreated element at the right moment.
document.body.addEventListener('htmx:afterSwap', function (evt) {
  if (!evt.target.closest || !evt.target.closest('.kiosk-week')) return;
  updateNowLine();
  scrollToNowIfFull24();
});

function scrollToNowIfFull24() {
  const container = document.querySelector('.kiosk-week[data-grid-mode="full24"]');
  if (!container) return;
  const nowLine = container.querySelector('.week-grid-track-today .now-line');
  if (!nowLine) return;
  // 'nearest' only scrolls if not already visible - brings "now" into view
  // without forcing a hard center, so it won't fight a parent who scrolled
  // to look at, say, the evening.
  nowLine.scrollIntoView({ block: 'nearest', behavior: 'auto' });
}

if (document.querySelector('.kiosk-week')) {
  scrollToNowIfFull24();
}
