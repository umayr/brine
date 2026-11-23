const http = require('node:http');
const { test, expect } = require('@playwright/test');
const base = process.env.BRINE_FIXTURE_BASE_URL;

async function startImagePullFixture(handler) {
  const upstream = new URL(base);
  if (!['127.0.0.1', '::1', 'localhost'].includes(upstream.hostname)) throw new Error('image pull fixture requires a loopback upstream');
  const requests = [];
  const sockets = new Set();
  const server = http.createServer(async (request, response) => {
    const target = new URL(request.url, upstream);
    if (target.origin !== upstream.origin) {
      response.writeHead(403).end();
      return;
    }
    if (target.pathname === '/web/images/pull') {
      let body = '';
      for await (const chunk of request) body += chunk;
      requests.push({ method: request.method, body });
      await handler(request, response);
      return;
    }
    if (!['GET', 'HEAD'].includes(request.method)) {
      response.writeHead(405).end();
      return;
    }
    const proxy = http.request(target, {
      method: request.method,
      headers: { ...request.headers, host: upstream.host },
    }, upstreamResponse => {
      response.writeHead(upstreamResponse.statusCode, upstreamResponse.headers);
      upstreamResponse.pipe(response);
    });
    proxy.on('error', error => response.destroy(error));
    request.pipe(proxy);
  });
  server.on('connection', socket => {
    sockets.add(socket);
    socket.on('close', () => sockets.delete(socket));
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  const { port } = server.address();
  return {
    base: `http://127.0.0.1:${port}`,
    requests: () => [...requests],
    close: () => new Promise(resolve => {
      server.close(resolve);
      server.closeAllConnections?.();
      for (const socket of sockets) socket.destroy();
    }),
  };
}

for (const [outcome, message] of [
  ['engine error', 'The engine reported an image pull error.'],
  ['completed progress', 'Engine finished reporting pull progress without an error. Refresh image inventory to verify the image.'],
  ['network failure', 'Image pull could not be confirmed.'],
  ['HTTP failure', 'Image pull could not be confirmed.'],
]) {
  test(`image pull dialog reports ${outcome} inline without a duplicate request`, async ({ page }) => {
    let requests = 0;
    let release;
    const pending = new Promise(resolve => { release = resolve; });
    await page.route('**/web/images/pull', async route => {
      requests++;
      expect(route.request().method()).toBe('POST');
      expect(route.request().postData()).toBe('ref=example%3Alocal');
      await pending;
      if (outcome === 'network failure') return route.abort();
      if (outcome === 'HTTP failure') return route.fulfill({ status: 504, contentType: 'text/html', body: '<strong>Unexpected error response</strong>' });
      return route.fulfill({ status: 200, contentType: 'text/html', body: `<div>${message}</div>` });
    });
    await page.goto(new URL('/images', base).href);
    await page.getByRole('button', { name: 'Pull image', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'Pull image' });
    const ref = dialog.getByRole('textbox', { name: 'Image reference' });
    const submit = dialog.getByRole('button', { name: 'Pull', exact: true });
    await ref.fill('example:local');
    await submit.click();
    const status = dialog.getByRole('status');
    await expect(status).toHaveText('Pulling image…');
    await expect(submit).toBeDisabled();
    await expect(ref).toBeDisabled();
    await dialog.locator('form[hx-post]').evaluate(form => form.requestSubmit());
    release();
    await expect(status).toContainText(message);
    await expect(status).not.toContainText(/event: done|pull complete|Unexpected error response/i);
    await expect(ref).toHaveValue('example:local');
    await expect(submit).toBeEnabled();
    expect(requests).toBe(1);
  });
}

test('a timed-out pull releases the dialog without resubmitting', async ({ page }) => {
  const fixture = await startImagePullFixture(() => {});
  try {
    await page.goto(new URL('/images', fixture.base).href);
    await page.getByRole('button', { name: 'Pull image', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'Pull image' });
    const form = dialog.locator('form[hx-post]');
    await expect(form).toHaveAttribute('hx-request', '{"timeout":60000}');
    await form.evaluate(el => el.setAttribute('hx-request', '{"timeout":500}'));
    const ref = dialog.getByRole('textbox', { name: 'Image reference' });
    const submit = dialog.getByRole('button', { name: 'Pull', exact: true });
    await ref.fill('example:local');
    await submit.click();
    await expect(dialog.getByRole('status')).toContainText('Image pull could not be confirmed');
    await expect(submit).toBeEnabled();
    await expect(ref).toBeEnabled();
    expect(fixture.requests()).toEqual([{ method: 'POST', body: 'ref=example%3Alocal' }]);
  } finally {
    await fixture.close();
  }
});
