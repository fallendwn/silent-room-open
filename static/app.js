const state = { category: 'все', sort: 'new', observed: new Set() };
const grid = document.querySelector('#confession-grid');
const empty = document.querySelector('#empty-state');
const template = document.querySelector('#confession-template');
const dialog = document.querySelector('#compose-dialog');
const form = document.querySelector('#compose-form');
const textarea = document.querySelector('#confession-text');
const formMessage = document.querySelector('#form-message');
const toast = document.querySelector('#toast');

const categoryNames = {
  'поступок': 'Поступок',
  'отношения': 'Отношения',
  'тайна': 'Тайна',
  'мысли': 'Мысли',
  'другое': 'Другое'
};

async function loadConfessions() {
  grid.setAttribute('aria-busy', 'true');
  try {
    const query = new URLSearchParams({ sort: state.sort, category: state.category });
    const response = await fetch(`/api/confessions?${query}`);
    if (!response.ok) throw new Error('Не удалось загрузить признания');
    renderConfessions(await response.json());
  } catch (error) {
    grid.innerHTML = `<p class="empty-state">${error.message}. Попробуйте обновить страницу.</p>`;
  } finally {
    grid.removeAttribute('aria-busy');
  }
}

function renderConfessions(items) {
  grid.replaceChildren();
  empty.hidden = items.length !== 0;
  items.forEach((item, index) => {
    const fragment = template.content.cloneNode(true);
    const card = fragment.querySelector('.confession-card');
    card.dataset.id = item.id;
    card.style.animationDelay = `${Math.min(index * 55, 350)}ms`;
    fragment.querySelector('.category-badge').textContent = categoryNames[item.category] || item.category;
    fragment.querySelector('.card-number').textContent = `№ ${String(index + 1).padStart(2, '0')}`;
    fragment.querySelector('.confession-text').textContent = item.text;
    const time = fragment.querySelector('time');
    time.dateTime = item.createdAt;
    time.textContent = formatDate(item.createdAt);
    fragment.querySelector('.view-count b').textContent = formatNumber(item.views);
    grid.append(fragment);
  });
  observeCards();
}

function formatDate(value) {
  const date = new Date(value);
  const diff = Date.now() - date.getTime();
  const minutes = Math.floor(diff / 60000);
  if (minutes < 1) return 'только что';
  if (minutes < 60) return `${minutes} мин. назад`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} ч. назад`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days} дн. назад`;
  return new Intl.DateTimeFormat('ru', { day: 'numeric', month: 'long', year: 'numeric' }).format(date);
}

function formatNumber(value) {
  return new Intl.NumberFormat('ru').format(value);
}

function observeCards() {
  const observer = new IntersectionObserver((entries) => {
    entries.forEach(entry => {
      if (!entry.isIntersecting || entry.intersectionRatio < .55) return;
      const card = entry.target;
      observer.unobserve(card);
      if (state.observed.has(card.dataset.id)) return;
      state.observed.add(card.dataset.id);
      recordView(card);
    });
  }, { threshold: .55 });
  document.querySelectorAll('.confession-card').forEach(card => observer.observe(card));
}

async function recordView(card) {
  try {
    const response = await fetch(`/api/confessions/${encodeURIComponent(card.dataset.id)}/view`, { method: 'POST' });
    if (!response.ok) return;
    const data = await response.json();
    card.querySelector('.view-count b').textContent = formatNumber(data.views);
  } catch (_) {
    // View counting is intentionally non-blocking for readers.
  }
}

async function updateStats() {
  try {
    const response = await fetch('/api/stats');
    if (!response.ok) return;
    const data = await response.json();
    document.querySelector('#total-count').textContent = formatNumber(data.confessions);
  } catch (_) {}
}

document.querySelectorAll('.filter').forEach(button => {
  button.addEventListener('click', () => {
    document.querySelector('.filter.active')?.classList.remove('active');
    button.classList.add('active');
    state.category = button.dataset.category;
    loadConfessions();
  });
});

document.querySelector('#sort-select').addEventListener('change', event => {
  state.sort = event.target.value;
  loadConfessions();
});

document.querySelectorAll('[data-open-compose]').forEach(button => button.addEventListener('click', () => {
  formMessage.textContent = '';
  dialog.showModal();
  setTimeout(() => textarea.focus(), 50);
}));
document.querySelector('[data-close-compose]').addEventListener('click', () => dialog.close());
dialog.addEventListener('click', event => {
  if (event.target === dialog) dialog.close();
});

textarea.addEventListener('input', () => {
  document.querySelector('#char-count').textContent = `${[...textarea.value].length} / 2000`;
});

form.addEventListener('submit', async event => {
  event.preventDefault();
  const submit = form.querySelector('[type="submit"]');
  submit.disabled = true;
  submit.firstChild.textContent = 'Публикуем… ';
  formMessage.textContent = '';
  const data = Object.fromEntries(new FormData(form));
  try {
    const response = await fetch('/api/confessions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(data)
    });
    const result = await response.json();
    if (!response.ok) throw new Error(result.error || 'Не удалось отправить признание');
    form.reset();
    document.querySelector('#char-count').textContent = '0 / 2000';
    dialog.close();
    state.category = 'все';
    document.querySelector('.filter.active')?.classList.remove('active');
    document.querySelector('[data-category="все"]').classList.add('active');
    await Promise.all([loadConfessions(), updateStats()]);
    showToast('Ваши слова остались здесь анонимно');
    document.querySelector('#confessions').scrollIntoView({ behavior: 'smooth' });
  } catch (error) {
    formMessage.textContent = error.message;
  } finally {
    submit.disabled = false;
    submit.firstChild.textContent = 'Опубликовать анонимно ';
  }
});

function showToast(message) {
  toast.textContent = message;
  toast.classList.add('show');
  setTimeout(() => toast.classList.remove('show'), 3200);
}

loadConfessions();
updateStats();
