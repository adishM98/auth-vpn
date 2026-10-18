import fs from 'node:fs';
import path from 'node:path';
import { test, showOverlay, withOverlay } from '@argo-video/cli';

// The narrated tour: the terminal recording (../media/terminal.mp4, made by
// VHS) played segment by segment, then the stage server's live dashboard.
// Run ../env.sh up, ../env.sh tape and ../env.sh dash first; see ../README.md.

test.use({ ignoreHTTPSErrors: true }); // the demo server's certificate is self-signed

const env = Object.fromEntries(
  fs.readFileSync(path.join(__dirname, '../../.env'), 'utf8').trim().split('\n').map((l) => l.split(/=(.*)/s).slice(0, 2)),
);
const terminalPage = 'file://' + path.join(__dirname, '../assets/terminal.html');

// Seconds into terminal.mp4 where each part of terminal.tape starts and ends.
const SEG = {
  private: [0, 7],
  connect: [7, 16],
  works: [16, 22.5],
  switch: [22.5, 37],
  prod: [37, 47.5],
};

test('tour', async ({ page, narration }) => {
  test.setTimeout(5 * 60_000); // the tour runs about 90 s; Playwright's default is 30 s
  await page.goto(terminalPage);
  await page.waitForFunction(() => (document.getElementById('t') as HTMLVideoElement).readyState >= 2);
  await narration.startRecording(page);

  narration.mark('intro');
  await showOverlay(page, 'intro', narration.durationFor('intro'));

  for (const scene of ['private', 'connect', 'works', 'switch', 'prod'] as const) {
    narration.mark(scene);
    const [start, end] = SEG[scene];
    await withOverlay(page, scene, async () => {
      await Promise.all([
        page.evaluate(([s, e]) => (window as any).playSeg(s, e), [start, end]),
        page.waitForTimeout(narration.durationFor(scene)),
      ]);
    });
  }

  await page.goto(`/ui?key=${env.STAGE_KEY}`);
  await page.waitForSelector('#tab-overview.active');
  await page.waitForTimeout(1500); // first refresh fills the stat cards

  narration.mark('overview');
  await showOverlay(page, 'overview', narration.durationFor('overview'));

  narration.mark('clients');
  await withOverlay(page, 'clients', async () => {
    await page.click("button.nav-item:has-text('Clients')");
    await page.waitForTimeout(narration.durationFor('clients') * 0.55);
    page.once('dialog', (d) => d.accept());
    await page.click("#tab-clients button:has-text('Kick')");
    await page.waitForTimeout(narration.durationFor('clients') * 0.45);
  });

  narration.mark('tokens');
  await withOverlay(page, 'tokens', async () => {
    await page.click("button.nav-item:has-text('Access tokens')");
    await page.click("#tab-tokens button:has-text('Create token')");
    await page.locator('#token-name').pressSequentially('alice-mac', { delay: 70 });
    await page.click(".modal-footer button:has-text('Create token')");
    await page.waitForTimeout(narration.durationFor('tokens') * 0.6);
    await page.click(".modal-footer button:has-text('Done')");
    await page.waitForTimeout(narration.durationFor('tokens') * 0.2);
  });

  narration.mark('whitelist');
  await withOverlay(page, 'whitelist', async () => {
    await page.click("button.nav-item:has-text('IP whitelist')");
    await page.click("#tab-whitelist button:has-text('Add IP')");
    await page.locator('#wl-name').pressSequentially('ci-runners', { delay: 70 });
    await page.locator('#wl-ip').pressSequentially('192.0.2.0/24', { delay: 70 });
    await page.click(".modal-footer button:has-text('Add IP')");
    await page.waitForTimeout(narration.durationFor('whitelist') * 0.6);
  });

  narration.mark('outro');
  await showOverlay(page, 'outro', narration.durationFor('outro'));
});
