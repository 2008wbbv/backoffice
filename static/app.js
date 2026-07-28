// Progressive enhancement only: every control here already works as a plain
// form post if this file never loads.

// Quantity steppers update in place instead of reloading the grid.
document.addEventListener('submit', async (e) => {
  const form = e.target;
  if (!form.classList.contains('step')) return;

  const box = form.closest('.qty, .qtybox');
  const out = box && box.querySelector('.n, .big-qty');
  if (!out) return;

  e.preventDefault();
  try {
    const res = await fetch(form.action, {
      method: 'POST',
      headers: { Accept: 'application/json' },
      body: new FormData(form),
    });
    if (!res.ok) throw new Error(res.statusText);
    const { quantity } = await res.json();
    out.textContent = quantity;
    out.classList.toggle('zero', quantity === 0);
  } catch {
    form.submit(); // fall back to the full page post
  }
});

// One confirm helper for every destructive form.
document.addEventListener('submit', (e) => {
  const msg = e.target.dataset.confirm;
  if (msg && !confirm(msg)) e.preventDefault();
}, true);

// Detail gallery: click a thumbnail to swap the hero, click the hero to zoom.
const hero = document.getElementById('hero');
if (hero) {
  const shots = [...document.querySelectorAll('.shot')];
  const mark = () => shots.forEach((s) =>
    s.setAttribute('aria-current', String(s.dataset.full === hero.getAttribute('src'))));
  shots.forEach((s) => s.addEventListener('click', () => {
    hero.src = s.dataset.full;
    mark();
  }));
  mark();

  hero.addEventListener('click', () => {
    const dlg = document.createElement('dialog');
    dlg.className = 'light';
    const img = document.createElement('img');
    img.src = hero.src;
    dlg.append(img);
    dlg.addEventListener('click', () => dlg.close());
    dlg.addEventListener('close', () => dlg.remove());
    document.body.append(dlg);
    dlg.showModal();
  });
}

// Drag photos onto the detail page to attach them.
const zone = document.querySelector('.dropzone');
if (zone) {
  const input = zone.querySelector('input[type=file]');
  const stop = (e) => { e.preventDefault(); e.stopPropagation(); };

  ['dragenter', 'dragover'].forEach((t) =>
    zone.addEventListener(t, (e) => { stop(e); zone.classList.add('over'); }));
  ['dragleave', 'drop'].forEach((t) =>
    zone.addEventListener(t, (e) => { stop(e); zone.classList.remove('over'); }));

  zone.addEventListener('drop', (e) => {
    if (!e.dataTransfer.files.length) return;
    input.files = e.dataTransfer.files;
    zone.submit();
  });

  // Picking files is the whole intent; no reason to make them press Upload.
  input.addEventListener('change', () => { if (input.files.length) zone.submit(); });
}

// Keyboard: "/" focuses search, "n" opens the add form.
document.addEventListener('keydown', (e) => {
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  const tag = document.activeElement && document.activeElement.tagName;
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;

  if (e.key === '/') {
    const s = document.querySelector('input[type=search]');
    if (s) { e.preventDefault(); s.focus(); s.select(); }
  } else if (e.key === 'n') {
    e.preventDefault();
    location.href = '/items/new';
  }
});

// --- import from a link -----------------------------------------------------
// The server does the fetching and parsing; this only fills the form in and
// leaves everything editable, so nothing is saved until Add/Save is pressed.
const importGo = document.getElementById('import-go');
if (importGo) {
  const urlInput = document.getElementById('import-url');
  const status = document.getElementById('import-status');
  const preview = document.getElementById('import-preview');

  const say = (msg, cls) => {
    status.textContent = msg;
    status.className = 'import-status' + (cls ? ' ' + cls : '');
    status.hidden = !msg;
  };

  // Only fill a field the person hasn't already written in, so re-fetching
  // never clobbers a correction.
  const fill = (id, value, { force = false } = {}) => {
    const el = document.getElementById(id);
    if (!el || !value) return;
    if (force || !el.value.trim()) el.value = value;
  };

  const run = async () => {
    const url = urlInput.value.trim();
    if (!url) { say('Paste a link first.', 'bad'); return; }

    importGo.disabled = true;
    say('Fetching…', 'busy');
    preview.hidden = true;

    try {
      const res = await fetch('/import/preview', {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({ url }),
      });
      const data = await res.json();
      if (!res.ok) throw new Error(data.error || 'could not read that page');

      fill('f-name', data.name);
      fill('f-notes', data.notes);
      fill('f-value', data.value);
      fill('f-part_number', data.part_number);
      fill('f-link', data.link, { force: true });
      fill('f-image_url', data.image_url, { force: true });

      if (data.image_url) {
        document.getElementById('import-thumb').src = data.image_url;
      }
      document.getElementById('import-title').textContent = data.name || '(no title found)';
      document.getElementById('import-site').textContent = data.site || new URL(data.link || url).hostname;
      preview.hidden = false;
      say('Filled in what the page provided — edit anything before saving.', '');
    } catch (err) {
      say(err.message, 'bad');
    } finally {
      importGo.disabled = false;
    }
  };

  importGo.addEventListener('click', run);
  urlInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); run(); }
  });
}

// --- tag picker -------------------------------------------------------------
// Clicking an existing tag toggles it in the comma-separated field, which stays
// the source of truth so typing a brand new tag still works.
const tagSuggest = document.getElementById('tag-suggest');
if (tagSuggest) {
  const field = document.getElementById('f-tags');
  const read = () => field.value.split(',').map((t) => t.trim()).filter(Boolean);
  const sync = () => {
    const active = read().map((t) => t.toLowerCase());
    tagSuggest.querySelectorAll('[data-tag]').forEach((b) =>
      b.classList.toggle('on', active.includes(b.dataset.tag.toLowerCase())));
  };

  tagSuggest.addEventListener('click', (e) => {
    const btn = e.target.closest('[data-tag]');
    if (!btn) return;
    const tag = btn.dataset.tag;
    const tags = read();
    const at = tags.findIndex((t) => t.toLowerCase() === tag.toLowerCase());
    if (at >= 0) tags.splice(at, 1); else tags.push(tag);
    field.value = tags.join(', ');
    sync();
  });

  field.addEventListener('input', sync);
  sync();
}
