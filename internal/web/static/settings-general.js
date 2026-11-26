(() => {
  const get = (id) => document.getElementById('general-' + id);
  const signin = get('signin');
  const username = get('user');
  const newPassword = get('new-password');
  const confirmPassword = get('confirm-password');
  const update = signin.querySelector('button[type=submit]');
  const dialog = get('confirmation');
  const current = get('current-password');
  const submit = get('submit');
  const cancel = get('cancel');
  const error = get('error');
  const warning = get('warning');
  const token = get('token');
  const show = get('show');
  const regenerate = get('regenerate');
  const copy = get('copy');
  const hide = get('hide');
  const status = get('api-status');
  const login = '/login?next=%2Fsettings%2Fgeneral';
  const masked = token?.textContent;
  let request = null;
  let pending = null;

  const confirm = (action, fields = {}) => {
    const [title, message, label] = {
      update: ['Update sign-in?', 'Confirm your current password to update your sign-in. All browser sessions will be signed out.', 'Update'],
      show: ['Show API token', 'Confirm your current password to show this token.', 'Show'],
      regenerate: ['Regenerate API token?', 'The old token stops working immediately. Update any scripts or integrations that use it.', 'Regenerate'],
    }[action];
    get('confirmation-title').textContent = title;
    get('confirmation-message').textContent = message;
    submit.textContent = label;
    submit.classList.toggle('ui-button-danger', action === 'regenerate');
    submit.classList.toggle('ui-button-primary', action !== 'regenerate');
    request = { action, ...fields };
    error.hidden = true;
    current.value = '';
    dialog.showModal();
  };

  const setToken = (value) => {
    if (!token) return;
    token.textContent = value || masked;
    if (value) token.removeAttribute('aria-label');
    else token.setAttribute('aria-label', 'Token hidden');
    show.hidden = Boolean(value);
    copy.hidden = hide.hidden = !value;
  };

  if (update) {
    const updateAvailability = () => {
      update.disabled = (username.readOnly || username.value === username.defaultValue)
        && !newPassword?.value && !confirmPassword?.value;
    };
    signin.addEventListener('input', updateAvailability);
    updateAvailability();
    signin.addEventListener('submit', (event) => {
      event.preventDefault();
      if (newPassword && newPassword.value !== confirmPassword.value) {
        confirmPassword.setCustomValidity('The passwords don’t match.');
        confirmPassword.reportValidity();
        return;
      }
      const fields = {};
      if (!username.readOnly && username.value !== username.defaultValue) fields.username = username.value;
      if (newPassword?.value) fields.new_password = newPassword.value;
      if (Object.keys(fields).length) confirm('update', fields);
    });
    for (const input of [newPassword, confirmPassword]) {
      input?.addEventListener('input', () => confirmPassword.setCustomValidity(''));
    }
  }

  if (token) {
    show.disabled = regenerate.disabled = false;
    show.addEventListener('click', () => confirm('show'));
    regenerate.addEventListener('click', () => confirm('regenerate'));
    hide.addEventListener('click', () => { setToken(''); show.focus(); });
    copy.addEventListener('click', async () => {
      try {
        await navigator.clipboard.writeText(token.textContent);
        status.textContent = 'Token copied.';
      } catch {
        status.textContent = 'Couldn’t copy automatically. Select the token to copy it.';
      }
    });
  }
  window.addEventListener('pagehide', () => {
    pending?.abort();
    setToken('');
    if (dialog.open) dialog.close();
    request = null;
    current.value = '';
    if (newPassword) newPassword.value = confirmPassword.value = '';
  });
  dialog.addEventListener('cancel', (event) => { if (pending) event.preventDefault(); });
  cancel.addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', () => {
    current.value = '';
    request = null;
  });

  dialog.querySelector('form').addEventListener('submit', async (event) => {
    event.preventDefault();
    if (!request || pending) return;
    const payload = { ...request, current_password: current.value };
    current.value = '';
    const flight = new AbortController();
    pending = flight;
    submit.disabled = cancel.disabled = true;
    error.hidden = true;
    try {
      const response = await fetch('/settings/general', {
        method: 'POST',
        headers: brineHeaders({ 'Content-Type': 'application/json', Accept: 'application/json' }),
        cache: 'no-store',
        signal: flight.signal,
        body: JSON.stringify(payload),
      });
      if (response.status === 401) {
        location.assign(login);
        return;
      }
      const result = await response.json();
      if (flight.signal.aborted) return;
      if (!response.ok) {
        error.textContent = result.error || 'Couldn’t verify the credential change. Check its status before retrying.';
        error.hidden = false;
        current.focus();
        return;
      }
      dialog.close();
      if (result.durability_uncertain) {
        warning.hidden = false;
        warning.textContent = 'The published credentials are active, but Brine couldn’t confirm they will survive a restart. Check the credentials file before restarting.';
      }
      if (result.signed_out) {
        if (newPassword) newPassword.value = confirmPassword.value = '';
        if (!result.durability_uncertain) {
          location.replace(login);
        } else {
          for (const button of [update, show, regenerate]) if (button) button.disabled = true;
          setToken('');
          const link = document.createElement('a');
          link.href = login;
          link.textContent = 'Sign in again';
          warning.append(' ', link);
          link.focus();
        }
      } else if (result.token) {
        setToken(result.token);
        status.textContent = payload.action === 'regenerate' ? 'Token regenerated. The old token no longer works.' : '';
        copy.focus();
      }
    } catch {
      if (flight.signal.aborted) return;
      error.textContent = payload.action === 'show'
        ? 'Couldn’t show the token. Try again.'
        : 'Couldn’t confirm the outcome. Reload and check before retrying.';
      error.hidden = false;
      current.focus();
    } finally {
      pending = null;
      submit.disabled = cancel.disabled = false;
    }
  });
})();
