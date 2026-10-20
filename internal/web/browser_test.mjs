// Dependency-free Chrome integration driver; run through TestBrowserNavigation.
import {spawn} from 'node:child_process';
import {mkdtemp, rm, writeFile} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {once} from 'node:events';
import assert from 'node:assert/strict';

const [base, directoryRoot] = process.argv.slice(2);
const profile = await mkdtemp(join(tmpdir(), 'open-server-chrome-'));
const chromePath = process.env.CHROME_BIN || (process.platform === 'darwin' ? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' : 'google-chrome');
const chrome = spawn(chromePath, ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--disable-extensions', '--disable-background-networking', '--remote-debugging-port=0', `--user-data-dir=${profile}`, 'about:blank'], {stdio: ['ignore', 'ignore', 'pipe']});
let socket;
let sessionId;
let nextID = 0;
const commands = new Map();
const exceptions = [];
let stderr = '';

async function control(action, extra = {}) {
  return (await fetch(base + '/__test/control', action ? {method: 'POST', body: JSON.stringify({action, ...extra})} : {})).json();
}
function send(method, params = {}, session = sessionId) {
  return new Promise((resolve, reject) => {
    const id = ++nextID;
    const timer = setTimeout(() => { commands.delete(id); reject(new Error('CDP timeout: ' + method)); }, 15000);
    commands.set(id, {resolve: result => {clearTimeout(timer); resolve(result);}, reject});
    socket.send(JSON.stringify({id, method, params, ...(session ? {sessionId: session} : {})}));
  });
}
async function evaluate(expression) {
  const result = await send('Runtime.evaluate', {expression, awaitPromise: true, returnByValue: true});
  if (result.exceptionDetails) throw new Error(result.exceptionDetails.exception?.description || result.exceptionDetails.text);
  return result.result.value;
}
async function run(body) { return evaluate(`(async () => {${body}})()`); }
async function wait(expression) {
  return run(`const deadline = Date.now() + 10000;
    while (!(${expression})) { if (Date.now() > deadline) throw new Error('Wait failed: ' + ${JSON.stringify(expression)}); await new Promise(resolve => setTimeout(resolve, 25)); }
    return true;`);
}
async function updated(path) {
  await wait(`document.getElementById('directory-header')?.dataset.directory === ${JSON.stringify(path)} && !document.getElementById('directory-content').hasAttribute('aria-busy') && document.getElementById('navigation-status').textContent.startsWith('Updated ')`);
}
async function jump(relative) {
  await run(`document.getElementById('path-jump-input').value = ${JSON.stringify(relative)}; document.getElementById('path-jump').dispatchEvent(new Event('submit', {bubbles: true, cancelable: true}));`);
}
async function click(selector) { await run(`document.querySelector(${JSON.stringify(selector)}).click();`); }
async function check(name, fn) { await fn(); process.stdout.write('PASS ' + name + '\n'); }

try {
  const debuggerURL = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('Chrome did not start: ' + stderr)), 15000);
    chrome.on('error', reject);
    chrome.on('exit', code => reject(new Error('Chrome exited ' + code + ': ' + stderr)));
    chrome.stderr.on('data', data => {
      stderr += data.toString();
      const match = stderr.match(/DevTools listening on (ws:\/\/[^\s]+)/);
      if (match) { clearTimeout(timer); resolve(match[1]); }
    });
  });
  socket = new WebSocket(debuggerURL);
  await once(socket, 'open');
  socket.addEventListener('message', event => {
    const message = JSON.parse(event.data);
    if (message.id && commands.has(message.id)) {
      const command = commands.get(message.id);
      commands.delete(message.id);
      if (message.error) command.reject(new Error(JSON.stringify(message.error)));
      else command.resolve(message.result);
    }
    if (message.method === 'Runtime.exceptionThrown') exceptions.push(message.params.exceptionDetails);
  });
  const target = await send('Target.createTarget', {url: 'about:blank'}, null);
  const attached = await send('Target.attachToTarget', {targetId: target.targetId, flatten: true}, null);
  sessionId = attached.sessionId;
  await send('Page.enable');
  await send('Runtime.enable');
  await send('Emulation.setDeviceMetricsOverride', {width: 900, height: 850, deviceScaleFactor: 1, mobile: false});
  await send('Page.navigate', {url: base + '/'});
  await wait(`document.getElementById('btn-refresh') && window.OpenServerBrowser && document.getElementById('upload-buttons').style.display !== 'none'`);
  await run(`window.sessionMarker = 'preserved';`);

  await check('Refresh fetches fresh contents and preserves filter, widths, and scroll', async () => {
    await run(`document.getElementById('regex-filter-input').value = '^entry-'; document.getElementById('regex-filter-controls').requestSubmit();
      window.initialColumnWidth = document.querySelector('col[data-column="name"]').getBoundingClientRect().width;
      document.querySelector('[data-resize-column="name"]').dispatchEvent(new KeyboardEvent('keydown', {key: 'ArrowLeft', bubbles: true}));
      window.scrollTo(0, 1500); document.getElementById('directory-content').scrollLeft = 90; window.initialY = scrollY;`);
    await control('write');
    await click('#btn-refresh');
    await updated(directoryRoot);
    const result = await run(`return {marker: window.sessionMarker, filter: document.getElementById('regex-filter-input').value,
      found: document.getElementById('directory-entries').textContent.includes('entry-0000-new.txt'),
      y: scrollY, initialY: window.initialY, tableX: document.getElementById('directory-content').scrollLeft,
      width: document.getElementById('directory-list').style.getPropertyValue('--column-name-width'), initialWidth: window.initialColumnWidth};`);
    assert.equal(result.marker, 'preserved'); assert.equal(result.filter, '^entry-'); assert.equal(result.found, true);
    assert.ok(Math.abs(result.y - result.initialY) < 35, JSON.stringify(result));
    assert.ok(Math.abs(parseFloat(result.width) - (result.initialWidth - 8)) < 1);
    assert.ok(result.tableX > 0, JSON.stringify(result));
  });

  await check('sorting and hidden toggles keep the page and filter state', async () => {
    await click('th:nth-child(3) a');
    await updated(directoryRoot);
    assert.equal(await evaluate(`new URL(location.href).searchParams.get('sort')`), 'size');
    await click('#btn-toggle-hidden');
    await updated(directoryRoot);
    assert.equal(await evaluate(`new URL(location.href).searchParams.get('hidden')`), '1');
    assert.equal(await evaluate(`document.getElementById('regex-filter-input').value`), '^entry-');
    assert.equal(await evaluate(`window.sessionMarker`), 'preserved');
    await click('#regex-filter-clear');
  });

  await check('Back/Forward restores cached folders immediately, then revalidates', async () => {
    await run(`window.scrollTo(0, 1200); window.backY = scrollY;`);
    await jump('a'); await updated(directoryRoot + '/a');
    await control('delay', {path: directoryRoot, delay: 500});
    await run('history.back();');
    await wait(`document.getElementById('directory-header').dataset.directory === ${JSON.stringify(directoryRoot)}`);
    assert.equal(await evaluate(`document.getElementById('directory-content').getAttribute('aria-busy')`), 'true');
    assert.ok(Math.abs(await evaluate('scrollY - window.backY')) < 35);
    await updated(directoryRoot);
    await run('history.forward();'); await updated(directoryRoot + '/a');
    await control('delay', {path: '', delay: 0});
  });

  await check('rapid navigation cancels obsolete reads and ignores late responses', async () => {
    await jump('b'); await updated(directoryRoot + '/b');
    await control('delay', {path: directoryRoot + '/a', delay: 800});
    await jump('a');
    await wait(`document.getElementById('directory-header').dataset.directory === ${JSON.stringify(directoryRoot + '/a')}`);
    await jump('b'); await updated(directoryRoot + '/b');
    await run(`await new Promise(resolve => setTimeout(resolve, 900));`);
    assert.equal(await evaluate(`document.getElementById('directory-header').dataset.directory`), directoryRoot + '/b');
    assert.ok((await control()).canceled >= 1);
    await control('delay', {path: '', delay: 0});
  });

  await check('disconnect preserves the interface and reconnect retries automatically', async () => {
    await control('disconnect');
    await jump(''); // An empty jump deliberately leaves the current directory alone.
    await click('#btn-refresh');
    await wait(`document.getElementById('navigation-retry').hidden === false`);
    assert.equal(await evaluate(`!!document.getElementById('directory-list')`), true);
    await control('reconnect');
    await updated(directoryRoot + '/b');
    assert.equal(await evaluate('window.sessionMarker'), 'preserved');
  });

  await check('permission-denied revalidation removes cached rows and Refresh recovers', async () => {
    await control('forbid'); await jump('a');
    await wait(`document.getElementById('navigation-feedback').dataset.kind === 'error' && !document.getElementById('directory-list')`);
    await control('reconnect'); await click('#btn-refresh'); await updated(directoryRoot + '/a');
  });

  await check('an upload batch keeps its original destination while navigating', async () => {
    await control('block-upload');
    await run(`const files = new DataTransfer(); files.items.add(new File(['first'], 'upload-one.txt')); files.items.add(new File(['second'], 'upload-two.txt'));
      const input = document.getElementById('file-input'); input.files = files.files; input.dispatchEvent(new Event('change', {bubbles: true}));`);
    let state;
    for (let i = 0; i < 100; i++) { state = await control(); if (state.uploads > 0) break; await new Promise(resolve => setTimeout(resolve, 10)); }
    assert.equal(state.uploads, 1);
    await jump('b'); await updated(directoryRoot + '/b');
    await control('release-upload');
    await wait(`document.getElementById('upload-status').textContent.startsWith('All uploads complete')`);
    for (const name of ['upload-one.txt', 'upload-two.txt']) {
      assert.equal((await fetch(base + '/download?path=' + encodeURIComponent(directoryRoot + '/a/' + name))).status, 200);
      assert.equal((await fetch(base + '/download?path=' + encodeURIComponent(directoryRoot + '/b/' + name))).status, 404);
    }
    assert.equal((await control()).uploads, 2);
    assert.equal(await evaluate(`document.getElementById('directory-header').dataset.directory`), directoryRoot + '/b');
  });

  await check('folder creation, URL import, and application dialogs work after view replacement', async () => {
    await click('#btn-create-folder');
    await run(`document.getElementById('folder-name').value = 'new-dir'; document.getElementById('folder-create').click();`);
    await wait(`document.getElementById('directory-entries').textContent.includes('new-dir/')`);
    await click('#btn-from-url');
    await run(`document.getElementById('url-input').value = ${JSON.stringify(base + '/__test/imported.txt')}; document.getElementById('url-fetch').click();`);
    await wait(`document.getElementById('directory-entries').textContent.includes('imported.txt')`);
    await click('[data-app="jupyter"][data-app-control] .app-start');
    assert.notEqual(await evaluate(`document.getElementById('jupyter-modal').style.display`), 'none');
    await click('#jupyter-cancel');
    assert.equal(await evaluate('window.sessionMarker'), 'preserved');
    assert.equal(await evaluate(`document.querySelectorAll('.file-action-column').length % 3`), 0);
  });

  await check('URL overwrite confirmation retains its destination across navigation', async () => {
    await click('#url-fetch');
    await wait(`document.getElementById('conflict-modal').style.display === 'block'`);
    await run('history.back();'); await updated(directoryRoot + '/a');
    await click('#conflict-overwrite');
    await wait(`document.getElementById('upload-status').textContent.startsWith('Saved ')`);
    assert.equal((await fetch(base + '/download?path=' + encodeURIComponent(directoryRoot + '/b/imported.txt'))).status, 200);
    assert.equal((await fetch(base + '/download?path=' + encodeURIComponent(directoryRoot + '/a/imported.txt'))).status, 404);
    assert.equal(await evaluate(`document.getElementById('directory-header').dataset.directory`), directoryRoot + '/a');
  });

  await check('direct loading during disconnection renders a retrying shell', async () => {
    await control('disconnect');
    await send('Page.navigate', {url: base + '/'});
    await wait(`document.getElementById('btn-refresh') && document.getElementById('navigation-retry')?.hidden === false`);
    assert.ok(await evaluate(`document.getElementById('directory-header').dataset.loadError`));
    await control('reconnect'); await updated(directoryRoot);
  });

  await check('repeated refresh and navigation dispose document listeners', async () => {
    async function listenerCounts() {
      const result = await send('Runtime.evaluate', {
        expression: `({keydown: (getEventListeners(document).keydown || []).length, click: (getEventListeners(document).click || []).length})`,
        includeCommandLineAPI: true, returnByValue: true
      });
      if (result.exceptionDetails) throw new Error(result.exceptionDetails.text);
      return result.result.value;
    }
    const initial = await listenerCounts();
    let current = directoryRoot;
    for (let i = 0; i < 20; i++) {
      if (i % 3 === 0) await click('#btn-refresh');
      else {
        const relative = i % 3 === 1 ? 'a' : 'b';
        current = directoryRoot + '/' + relative;
        await jump(relative);
      }
      await updated(current);
    }
    assert.deepEqual(await listenerCounts(), initial);
    await click('#regex-filter-help-toggle');
    assert.equal(await evaluate(`document.getElementById('regex-filter-help').hidden`), false);
    await run(`document.dispatchEvent(new KeyboardEvent('keydown', {key: 'Escape', bubbles: true}));`);
    assert.equal(await evaluate(`document.getElementById('regex-filter-help').hidden`), true);
    assert.equal(await evaluate(`document.activeElement.id`), 'regex-filter-help-toggle');
  });

  assert.equal(exceptions.length, 0, JSON.stringify(exceptions));
  if (process.env.BROWSER_SCREENSHOT) {
    const screenshot = await send('Page.captureScreenshot', {format: 'png'});
    await writeFile(process.env.BROWSER_SCREENSHOT, Buffer.from(screenshot.data, 'base64'));
  }
} finally {
  if (socket && socket.readyState === 1) socket.close();
  chrome.kill();
  await Promise.race([once(chrome, 'exit'), new Promise(resolve => setTimeout(resolve, 3000))]);
  await rm(profile, {recursive: true, force: true, maxRetries: 3});
}
