const assert = require('node:assert/strict');
const fs = require('node:fs');
const { execFileSync } = require('node:child_process');
const { setTimeout: delay } = require('node:timers/promises');
const { chromium } = require('playwright');

const base = process.env.PILOT_BRINE_URL;
const name = 'released-pair-pilot';
const api = { Authorization: `Bearer ${process.env.BRINE_API_TOKEN}` };
const project = marker => `amends "package://pkg.ushd.ca/dill@0.8.1#/dill.pkl"
config {
  name = "${name}"
  engine = "docker"
  services {
    ["web"] {
      image = "docker.io/library/alpine:3.22"
      command = List("sh", "-c", "echo ${marker}; sleep 600")
    }
  }
}
`;

function docker(...args) {
  return execFileSync('docker', ['--context', 'default', ...args], { encoding: 'utf8' }).trim();
}

const afterRestart = process.argv[2] === '--after-restart';
const checkpoint = process.env.PILOT_CHECKPOINT;

async function run() {
  if (!afterRestart) await createWorkspace();
  const browser = await chromium.launch();
  try {
    await exercise(await browser.newPage());
  } finally {
    await browser.close();
  }
}

async function createWorkspace() {
  const created = await fetch(`${base}/api/stacks/`, {
    method: 'POST', headers: { ...api, 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, pkl_content: project('pilot-v1') }),
  });
  assert.ok(created.ok, `create fixture workspace: HTTP ${created.status}`);
}

async function exercise(page) {
  {
    page.setDefaultTimeout(25_000);
    await page.goto(`${base}/remote`);
    await page.locator('#user').fill('admin');
    await page.locator('#password').fill(process.env.BRINE_AUTH_PASSWORD);
    await page.locator('form[action="/login"] button').click();
    if (!afterRestart) {
      await page.getByLabel('Target ID').fill('released-pilot');
      await page.getByLabel('Private IP and port').fill(process.env.PILOT_AGENT_ADDR);
      await page.getByLabel('Pairing bundle').fill(fs.readFileSync(process.env.PILOT_PAIRING, 'utf8'));
      await page.getByRole('button', { name: 'Pair HTTPS target' }).click();
    }
    const deploy = page.getByRole('link', { name: 'Deploy a project' });
    await deploy.waitFor();
    const deployURL = new URL(await deploy.getAttribute('href'), base).href;
    if (!afterRestart) console.log('Authenticated Chromium paired released native Dill agent');

    async function confirm(label, interrupt = false) {
      const link = page.getByRole('link', { name: 'Open status link' });
      const statusURL = new URL(await link.getAttribute('href'), base).href;
      await page.locator('form[action="/remote/deployment/create"] input[type=checkbox]').check();
      const button = page.getByRole('button', { name: label });
      if (interrupt) {
        const path = '**/remote/deployment/create';
        let submissions = 0;
        await page.route(path, async route => {
          submissions++;
          try {
            // Let the one server request complete; hide its response from Chromium.
            await route.fetch({ maxRetries: 0 });
          } finally {
            await route.abort('connectionclosed');
          }
        });
        await Promise.all([
          page.waitForEvent('requestfailed', request => request.method() === 'POST' && request.url().endsWith('/remote/deployment/create')),
          button.click({ noWaitAfter: true }),
        ]);
        assert.equal(submissions, 1);
        await page.unroute(path);
      } else {
        await button.click();
        await page.getByText(/State succeeded · Effects complete/).waitFor();
      }
      const receipt = await status(statusURL);
      console.log(`${label}: exact request status verified by GET${interrupt ? ' after browser-response loss' : ''}`);
      return { statusURL, receipt };
    }

    async function status(url) {
      const response = await page.goto(url);
      assert.equal(response.request().method(), 'GET');
      const result = page.getByText(/State succeeded · Effects complete/);
      await result.waitFor();
      return result.textContent();
    }

    async function review(label, interrupt) {
      await page.goto(deployURL);
      await page.getByLabel('Brine project name').fill(name);
      await page.getByRole('button', { name: 'Review project files' }).click();
      await page.getByRole('heading', { name: 'Review captured files' }).waitFor();
      await page.getByLabel('Send these exact files and request a preview').check();
      await page.getByRole('button', { name: 'Stage and preview' }).click();
      await page.getByRole('heading', { name: `Deployment preview for ${name}` }).waitFor();
      return confirm(label, interrupt);
    }

    if (!afterRestart) {
      await review('Create stack');
      const first = docker('inspect', '--format', '{{.Id}}', `${name}_web`);
      const network = docker('network', 'inspect', '--format', '{{.Id}}', name);
      const updated = await fetch(`${base}/api/stacks/${name}/`, {
        method: 'PUT', headers: { ...api, 'Content-Type': 'application/json' },
        body: JSON.stringify({ pkl_content: project('pilot-v2') }),
      });
      assert.ok(updated.ok, `update fixture workspace: HTTP ${updated.status}`);
      const outcome = await review('Update stack', true);
      const container = docker('inspect', '--format', '{{.Id}}', `${name}_web`);
      assert.notEqual(container, first);
      assert.equal(docker('network', 'inspect', '--format', '{{.Id}}', name), network);
      assert.match(docker('logs', `${name}_web`), /pilot-v2/);
      fs.writeFileSync(checkpoint, JSON.stringify({ ...outcome, container, network }), { mode: 0o600 });
      return;
    }

    const { statusURL, receipt, container, network } = JSON.parse(fs.readFileSync(checkpoint, 'utf8'));
    assert.equal(await status(statusURL), receipt);
    assert.equal(docker('inspect', '--format', '{{.Id}}', `${name}_web`), container);
    assert.equal(docker('network', 'inspect', '--format', '{{.Id}}', name), network);
    console.log('Same durable outcome and Docker identities after native Dill restart; status lookup used GET');
    await page.goto(deployURL);
    await page.getByLabel('Managed stack name').fill(name);
    await page.getByRole('button', { name: 'Preview removal' }).click();
    await confirm('Remove containers; keep data');
    assert.equal(docker('ps', '-aq', '--filter', `label=dill.stack=${name}`), '');
    assert.equal(docker('network', 'inspect', '--format', '{{.Id}}', name), network);
    console.log('Manual lifecycle passed: update replaced the container; removal retained network identity');
    await verifyGitPause(page, deployURL);
  }
}

async function verifyGitPause(page, deployURL) {
  const stack = 'released-git-pause';
  const source = process.env.PILOT_GIT_SOURCE;
  const git = (...args) => execFileSync('git', ['-C', source, ...args], { encoding: 'utf8' }).trim();
  const invalidCommit = git('rev-parse', 'HEAD');
  async function createGitWorkspace(name, branch) {
    const response = await fetch(`${base}/api/stacks/git`, {
      method: 'POST', headers: { ...api, 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, git: { url: process.env.PILOT_GIT_URL, branch } }),
    });
    assert.ok(response.ok, `create synthetic Git workspace (${branch}): HTTP ${response.status}`);
  }

  async function settings(name = stack) {
    await page.goto(deployURL);
    await page.getByLabel('Git-backed Brine project').fill(name);
    const result = page.waitForResponse(r => r.request().method() === 'POST' && r.url().endsWith('/remote/deployment/git'));
    await page.getByRole('button', { name: 'Remote auto-deploy settings' }).click();
    return (await result).status();
  }

  async function enable(name) {
    assert.equal(await settings(name), 200);
    await page.locator('input[type=checkbox][value=trusted]').check();
    await page.getByRole('button', { name: 'Enable remote auto-deploy' }).click();
    await page.getByText(/State: ready/).waitFor();
  }

  await createGitWorkspace(stack, 'main');
  await enable(stack);
  const deadline = Date.now() + 90_000;
  let blocked = false;
  while (Date.now() < deadline) {
    await delay(3000);
    if (await settings() === 200 && await page.getByText(/State: blocked/).isVisible()) {
      blocked = true;
      break;
    }
  }
  assert.ok(blocked, 'remote Git policy did not enter blocked state during staging or preview');
  await page.getByRole('button', { name: 'Disable remote auto-deploy' }).waitFor();
  await page.getByText('Staging or preview did not complete. Inspect the receiver before taking further action.').waitFor();
  assert.match(await page.getByText(/State: blocked/).textContent(), new RegExp(invalidCommit));

  // A fresh valid commit must not implicitly clear the existing pause.
  fs.writeFileSync(`${source}/compose.pkl`, project('git-next').replace(name, stack));
  git('add', 'compose.pkl');
  git('-c', 'user.name=Pilot', '-c', 'user.email=pilot@example.invalid', 'commit', '-qm', 'Synthetic valid successor');
  const successor = git('rev-parse', 'HEAD');
  const advertised = git('ls-remote', '--exit-code', process.env.PILOT_GIT_URL, 'refs/heads/main');
  assert.equal(advertised.split(/\s+/)[0], successor);

  // Enable a separate valid stack only after advertising the successor. Its
  // durable receipt proves the serial Git worker completed subsequent work.
  const witness = 'released-git-witness';
  git('switch', '-qc', 'witness');
  fs.writeFileSync(`${source}/compose.pkl`, project('git-witness').replace(name, witness));
  git('add', 'compose.pkl');
  git('-c', 'user.name=Pilot', '-c', 'user.email=pilot@example.invalid', 'commit', '-qm', 'Synthetic poll witness');
  await createGitWorkspace(witness, 'witness');
  await enable(witness);
  const stop = Date.now() + 90_000;
  let witnessed = false;
  while (Date.now() < stop) {
    await delay(3000);
    if (await settings(witness) === 200 && await page.getByText(/State: succeeded/).isVisible()) {
      witnessed = true;
      break;
    }
  }
  assert.ok(witnessed, 'no successful Git poll witness after advertising the successor');
  console.log('Witness deployment succeeded; disabling its Git policy before manual removal');
  await page.getByRole('button', { name: 'Disable remote auto-deploy' }).click();
  await page.getByRole('button', { name: 'Enable remote auto-deploy' }).waitFor();
  await page.goto(deployURL);
  await page.getByLabel('Managed stack name').fill(witness);
  await page.getByRole('button', { name: 'Preview removal' }).click();
  await page.locator('form[action="/remote/deployment/create"] input[type=checkbox]').check();
  const removal = page.waitForResponse(r => r.request().method() === 'POST' && r.url().endsWith('/remote/deployment/create'));
  await page.getByRole('button', { name: 'Remove containers; keep data' }).click();
  const removalResponse = await removal;
  assert.ok(removalResponse.ok(), `witness removal: HTTP ${removalResponse.status()}`);
  await page.getByText(/State succeeded · Effects complete/).waitFor();
  assert.equal(await settings(), 200);
  await page.getByText('Staging or preview did not complete. Inspect the receiver before taking further action.').waitFor();
  assert.match(await page.getByText(/State: blocked/).textContent(), new RegExp(invalidCommit));
  assert.equal(await page.getByRole('link', { name: /Look up pending request/ }).count(), 0);
  assert.equal(docker('ps', '-aq', '--filter', `label=dill.stack=${stack}`), '');
  assert.equal(docker('network', 'ls', '--filter', `name=^${stack}$`, '-q'), '');
  await page.getByRole('button', { name: 'Disable remote auto-deploy' }).waitFor();
  const journal = JSON.parse(fs.readFileSync(process.env.PILOT_JOURNAL, 'utf8'));
  assert.ok(journal.operations.some(row => row.stack === witness && row.state === 'succeeded'));
  assert.ok(!journal.operations.some(row => row.stack === stack), 'blocked Git project has an admitted operation');
  console.log('Enabled Git policy remained blocked while a separate Git stack deployed; final journal and Docker checks found no admission or matching resources for the blocked stack');
}

// Avoid Playwright call logs that could contain pairing or login values.
run().catch(error => {
  console.error(String(error.message).split('\n')[0]);
  process.exitCode = 1;
});
