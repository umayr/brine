// Theme management — persists dark/light choice in localStorage and cookie.
(function () {
  const DEFAULT = 'night';

  function getTheme() {
    return localStorage.getItem('theme') || getCookie('theme') || DEFAULT;
  }

  function getCookie(name) {
    const match = document.cookie.match(new RegExp('(^|;\\s*)' + name + '=([^;]+)'));
    return match ? match[2] : null;
  }

  function applyTheme(theme) {
    document.documentElement.setAttribute('data-theme', theme);
    const moon  = document.getElementById('icon-moon');
    const sun   = document.getElementById('icon-sun');
    const label = document.getElementById('theme-label');
    if (!moon || !sun || !label) return;
    if (theme === 'night') {
      moon.classList.remove('hidden');
      sun.classList.add('hidden');
      label.textContent = 'Dark mode';
    } else {
      moon.classList.add('hidden');
      sun.classList.remove('hidden');
      label.textContent = 'Light mode';
    }
  }

  // Apply immediately (before first paint) to avoid flash.
  applyTheme(getTheme());

  window.toggleTheme = function () {
    const next = getTheme() === 'night' ? 'corporate' : 'night';
    localStorage.setItem('theme', next);
    document.cookie = `theme=${next};path=/;max-age=${60 * 60 * 24 * 365};samesite=lax`;
    applyTheme(next);
  };
})();
