// Every behaviour on these pages that used to live in an inline <script> or an
// on*= attribute.
//
// It moved here because securityHeaders in cmd/pocketcfo/main.go sends a
// Content-Security-Policy with no script-src, so scripts fall back to
// default-src 'self'. Under that policy the browser refuses inline scripts and
// inline event handlers outright — silently, as a console message rather than
// an error the page can see. That took the accordion and the period pickers
// with it. Served from /static this file is 'self', so it runs; keeping the
// behaviour here rather than relaxing the policy is the point.
//
// Listeners are delegated from document so markup rendered later still works
// and nothing depends on this file running before the HTML it acts on.

// The category accordion on the spending and month pages.
document.addEventListener('click', function (e) {
  var header = e.target.closest('.group-header');
  if (!header) return;
  var group = header.closest('.group');
  if (group) group.classList.toggle('open');
});

// The period pickers. Which URL a change navigates to depends on the page, so
// the select carries it in data-nav rather than each page defining its own
// global function: "year" has no month to read, and spending hangs one path
// segment further down than the month view.
document.addEventListener('change', function (e) {
  var sel = e.target.closest('select[data-nav]');
  if (!sel) return;
  var year = document.getElementById('ysel');
  var month = document.getElementById('msel');
  if (!year) return;
  switch (sel.dataset.nav) {
    case 'year':
      location.href = '/' + year.value;
      break;
    case 'month':
      if (month) location.href = '/' + year.value + '/' + month.value;
      break;
    case 'spending':
      if (month) location.href = '/' + year.value + '/' + month.value + '/spending';
      break;
  }
});

// Copy a transaction line to the clipboard, from the spending detail page.
document.addEventListener('click', function (e) {
  var a = e.target.closest('.copy-tx');
  if (!a) return;
  e.preventDefault();

  function flash(state) {
    a.classList.add(state);
    setTimeout(function () { a.classList.remove(state); }, 1500);
  }

  function fallback() {
    var box = document.createElement('textarea');
    box.value = a.dataset.copy;
    box.setAttribute('readonly', '');
    box.style.position = 'fixed';
    box.style.opacity = '0';
    document.body.appendChild(box);
    box.select();
    var ok = false;
    try { ok = document.execCommand('copy'); } catch (err) { ok = false; }
    document.body.removeChild(box);
    flash(ok ? 'copied' : 'failed');
  }

  if (navigator.clipboard) {
    navigator.clipboard.writeText(a.dataset.copy).then(function () { flash('copied'); }, fallback);
  } else {
    fallback();
  }
});

// A missing avatar leaves the initials underneath rather than a broken image.
// error does not bubble, so this listens in the capture phase; and because the
// script is deferred the image may already have failed by the time it runs, so
// anything already broken is swept once at startup.
document.addEventListener('error', function (e) {
  var img = e.target;
  if (img.tagName === 'IMG' && img.closest('.avatar')) img.remove();
}, true);

document.addEventListener('DOMContentLoaded', function () {
  document.querySelectorAll('.avatar img').forEach(function (img) {
    if (img.complete && img.naturalWidth === 0) img.remove();
  });
});
