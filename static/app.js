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
