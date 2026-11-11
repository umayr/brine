// Apply browser-local preferences before paint. CSS owns the visuals; this file
// chooses named scopes and notifies editors without replacing their content.
(function () {
  const media = window.matchMedia('(prefers-color-scheme: dark)');
  const themes = ['system', 'light', 'dark'];
  const accents = ['sage', 'glacier', 'lilac', 'sand', 'rose', 'graphite'];
  const densities = ['comfortable', 'compact'];
  const keys = ['theme', 'brine-accent', 'brine-density'];
  let storageAvailable = true;

  function stored(key) {
    try { return localStorage.getItem(key); }
    catch (_) { storageAvailable = false; return null; }
  }
  function save(key, value) {
    try {
      if (value === null) localStorage.removeItem(key);
      else localStorage.setItem(key, value);
    } catch (_) { storageAvailable = false; }
  }
  function readPreferences() {
    let theme = stored('theme');
    if (theme === 'night') theme = 'dark'; // migrate existing preferences
    if (theme === 'corporate') theme = 'light';
    const accent = stored('brine-accent');
    const density = stored('brine-density');
    return {
      theme: themes.includes(theme) ? theme : 'system',
      accent: accents.includes(accent) ? accent : 'sage',
      density: densities.includes(density) ? density : 'comfortable',
    };
  }
  // Keep working in this tab even if browser storage is disabled or full.
  let preferences = readPreferences();

  function apply() {
    const dark = preferences.theme === 'dark' || (preferences.theme === 'system' && media.matches);
    const root = document.documentElement;
    root.dataset.theme = dark ? 'dark' : 'light';
    root.dataset.accent = preferences.accent;
    root.dataset.density = preferences.density;
    root.style.colorScheme = dark ? 'dark' : 'light';

    const themeSelect = document.getElementById('theme-select');
    const densitySelect = document.getElementById('density-select');
    if (themeSelect) themeSelect.value = preferences.theme;
    if (densitySelect) densitySelect.value = preferences.density;
    document.querySelectorAll('input[name="brine-accent"]').forEach(input => {
      input.checked = input.value === preferences.accent;
    });
    const warning = document.getElementById('preference-storage-warning');
    if (warning) warning.hidden = storageAvailable;
    window.dispatchEvent(new CustomEvent('brine:theme', {
      detail: { dark, preference: preferences.theme, accent: preferences.accent, density: preferences.density },
    }));
  }
  function feedback(message) {
    const status = document.getElementById('preference-feedback');
    if (status) status.textContent = message + (storageAvailable ? ' Saved for this browser.' : ' Applied in this tab only.');
  }
  function update(property, key, value, choices, message) {
    if (!choices.includes(value)) return;
    preferences[property] = value;
    save(key, value);
    apply();
    feedback(message);
  }

  window.setBrineTheme = choice => update('theme', 'theme', choice, themes, 'Color scheme updated.');
  window.setBrineAccent = choice => update('accent', 'brine-accent', choice, accents, 'Accent color updated.');
  window.setBrineDensity = choice => update('density', 'brine-density', choice, densities, 'Table spacing updated.');
  window.resetBrinePreferences = () => {
    keys.forEach(key => save(key, null));
    preferences = { theme: 'system', accent: 'sage', density: 'comfortable' };
    apply();
    feedback('Appearance reset to System, Sage and Comfortable.');
  };

  document.addEventListener('change', event => {
    const target = event.target;
    if (target.id === 'theme-select') window.setBrineTheme(target.value);
    else if (target.id === 'density-select') window.setBrineDensity(target.value);
    else if (target.matches('input[name="brine-accent"]')) window.setBrineAccent(target.value);
  });
  document.addEventListener('click', event => {
    if (event.target.closest('[data-reset-appearance]')) window.resetBrinePreferences();
  });
  media.addEventListener('change', () => {
    if (preferences.theme === 'system') apply();
  });
  window.addEventListener('storage', event => {
    if (!keys.includes(event.key) && event.key !== null) return;
    // Session-storage events from frames are not appearance preferences.
    if (event.storageArea) {
      try { if (event.storageArea !== localStorage) return; }
      catch (_) { return; }
    }
    preferences = readPreferences();
    apply();
  });
  apply();
  document.addEventListener('DOMContentLoaded', apply);
})();
