const assert = require('node:assert/strict');
const fs = require('node:fs');
const { execFileSync } = require('node:child_process');
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

async function run() {
  const created = await fetch(`${base}/api/stacks/`, {
    method: 'POST', headers: { ...api, 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, pkl_content: project('pilot-v1') }),
  });
  assert.ok(created.ok, `create fixture workspace: HTTP ${created.status}`);
  const browser = await chromium.launch();
  try {
    const page = await browser.newPage();
    page.setDefaultTimeout(25_000);
    await page.goto(`${base}/remote`);
    await page.locator('#user').fill('admin');
    await page.locator('#password').fill(process.env.BRINE_AUTH_PASSWORD);
    await page.locator('form[action="/login"] button').click();
    await page.getByLabel('Target ID').fill('released-pilot');
    await page.getByLabel('Private IP and port').fill(process.env.PILOT_AGENT_ADDR);
    await page.getByLabel('Pairing bundle').fill(fs.readFileSync(process.env.PILOT_PAIRING, 'utf8'));
    await page.getByRole('button', { name: 'Pair HTTPS target' }).click();
    const deploy = page.getByRole('link', { name: 'Deploy a project' });
    await deploy.waitFor();
    const deployURL = new URL(await deploy.getAttribute('href'), base).href;
    console.log('Authenticated Chromium paired released native Dill agent');

    async function confirm(label) {
      const link = page.getByRole('link', { name: 'Open status link' });
      const statusURL = new URL(await link.getAttribute('href'), base).href;
      await page.locator('form[action="/remote/deployment/create"] input[type=checkbox]').check();
      await page.getByRole('button', { name: label }).click();
      await page.getByText(/State succeeded · Effects complete/).waitFor();
      await page.goto(statusURL);
      await page.getByText(/State succeeded · Effects complete/).waitFor();
      console.log(`${label}: succeeded and exact request status verified by GET`);
    }

    async function review(label) {
      await page.goto(deployURL);
      await page.getByLabel('Brine project name').fill(name);
      await page.getByRole('button', { name: 'Review project files' }).click();
      await page.getByRole('heading', { name: 'Review captured files' }).waitFor();
      await page.getByLabel('Send these exact files and request a preview').check();
      await page.getByRole('button', { name: 'Stage and preview' }).click();
      await page.getByRole('heading', { name: `Deployment preview for ${name}` }).waitFor();
      await confirm(label);
    }

    await review('Create stack');
    const first = docker('inspect', '--format', '{{.Id}}', `${name}_web`);
    const network = docker('network', 'inspect', '--format', '{{.Id}}', name);
    const updated = await fetch(`${base}/api/stacks/${name}/`, {
      method: 'PUT', headers: { ...api, 'Content-Type': 'application/json' },
      body: JSON.stringify({ pkl_content: project('pilot-v2') }),
    });
    assert.ok(updated.ok, `update fixture workspace: HTTP ${updated.status}`);
    await review('Update stack');
    assert.notEqual(docker('inspect', '--format', '{{.Id}}', `${name}_web`), first);
    assert.equal(docker('network', 'inspect', '--format', '{{.Id}}', name), network);
    assert.match(docker('logs', `${name}_web`), /pilot-v2/);

    await page.goto(deployURL);
    await page.getByLabel('Managed stack name').fill(name);
    await page.getByRole('button', { name: 'Preview removal' }).click();
    await confirm('Remove containers; keep data');
    assert.equal(docker('ps', '-aq', '--filter', `label=dill.stack=${name}`), '');
    assert.equal(docker('network', 'inspect', '--format', '{{.Id}}', name), network);
    console.log('Manual lifecycle passed: update replaced the container; removal retained network identity');
  } finally {
    await browser.close();
  }
}

// Avoid Playwright call logs that could contain pairing or login values.
run().catch(error => {
  console.error(String(error.message).split('\n')[0]);
  process.exitCode = 1;
});
