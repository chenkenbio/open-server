/* Directory navigation. Keep this URL versioned: the asset is immutable. */
(function(root) {
  'use strict';

  class DirectoryCache {
    constructor({maxEntries = 10, maxBytes = 8 * 1024 * 1024, maxAge = 60000, now = Date.now} = {}) {
      this.entries = new Map();
      this.bytes = 0;
      Object.assign(this, {maxEntries, maxBytes, maxAge, now});
    }
    delete(key) {
      const entry = this.entries.get(key);
      if (entry) this.bytes -= entry.bytes;
      this.entries.delete(key);
    }
    get(key) {
      const entry = this.entries.get(key);
      if (!entry) return null;
      if (this.now() - entry.time >= this.maxAge) {
        this.delete(key);
        return null;
      }
      this.entries.delete(key);
      this.entries.set(key, entry);
      return entry.html;
    }
    set(key, html) {
      this.delete(key);
      // Count UTF-16 storage, rather than assuming one byte per character.
      const bytes = html.length * 2;
      if (bytes > this.maxBytes) return;
      this.entries.set(key, {html, bytes, time: this.now()});
      this.bytes += bytes;
      while (this.entries.size > this.maxEntries || this.bytes > this.maxBytes) {
        this.delete(this.entries.keys().next().value);
      }
    }
    invalidatePath(path) {
      for (const key of this.entries.keys()) {
        if (new URL(key).searchParams.get('path') === path) this.delete(key);
      }
    }
  }

  function normalizeURL(input, base, directoryRoot) {
    const url = new URL(input, base);
    if (url.origin !== new URL(base).origin || url.pathname !== '/') throw new Error('Invalid folder URL');
    const path = url.searchParams.get('path') || directoryRoot;
    const sort = ['size', 'modified'].includes(url.searchParams.get('sort')) ? url.searchParams.get('sort') : 'name';
    const order = url.searchParams.get('order') === 'desc' ? 'desc' : 'asc';
    url.search = '';
    url.hash = '';
    url.searchParams.set('path', path);
    url.searchParams.set('sort', sort);
    url.searchParams.set('order', order);
    if (new URL(input, base).searchParams.get('hidden') === '1') url.searchParams.set('hidden', '1');
    url.searchParams.sort();
    return url;
  }

  function retryDelay(value, now = Date.now()) {
    if (!value) return 5000;
    const seconds = Number(value);
    const milliseconds = Number.isFinite(seconds) ? seconds * 1000 : Date.parse(value) - now;
    return Number.isFinite(milliseconds) ? Math.max(1000, milliseconds) : 5000;
  }

  function createNavigation(options, win = window) {
    const doc = win.document;
    const ids = ['directory-header', 'regex-filter-controls', 'directory-content'];
    const header = () => doc.getElementById('directory-header');
    const content = () => doc.getElementById('directory-content');
    const directoryRoot = doc.getElementById('path-jump').dataset.root;
    const normalize = input => normalizeURL(input, win.location.href, directoryRoot);
    const cache = new DirectoryCache();
    const feedback = doc.getElementById('navigation-feedback');
    const status = doc.getElementById('navigation-status');
    const retry = doc.getElementById('navigation-retry');
    let currentURL = normalize(header().dataset.url);
    let dispose = options.dispose;
    let pending = null;
    let loadingTimer;
    let retryTimer;
    let scrollFrame;
    let disposed = false;
    const listeners = new AbortController();
    const initialPosition = historyPosition(win.history.state);
    let restoringInitialPosition = !!initialPosition && !header().dataset.loadError && doc.readyState !== 'complete';

    function announce(message, error = false) {
      status.textContent = message;
      feedback.dataset.kind = error ? 'error' : '';
    }
    function snapshot() {
      return ids.map(id => doc.getElementById(id).outerHTML).join('');
    }
    function position() {
      const result = {x: win.scrollX, y: win.scrollY, tableX: content().scrollLeft};
      for (const row of doc.querySelectorAll('#directory-entries tr')) {
        const rect = row.getBoundingClientRect();
        if (rect.bottom > 0 && rect.top < win.innerHeight && !row.hidden) {
          result.anchor = row.querySelector('a').getAttribute('href');
          result.offset = rect.top;
          break;
        }
      }
      const focused = doc.activeElement;
      if (focused && focused.id) {
        result.focus = focused.id;
        if (typeof focused.selectionStart === 'number') {
          result.selection = [focused.selectionStart, focused.selectionEnd];
        }
      }
      return result;
    }
    function restore(saved) {
      if (!saved) return;
      win.scrollTo(saved.x || 0, saved.y || 0);
      content().scrollLeft = saved.tableX || 0;
      if (saved.anchor) {
        for (const row of doc.querySelectorAll('#directory-entries tr')) {
          const link = row.querySelector('a');
          if (!row.hidden && link && link.getAttribute('href') === saved.anchor) {
            win.scrollBy(0, row.getBoundingClientRect().top - saved.offset);
            break;
          }
        }
      }
      const focused = saved.focus && doc.getElementById(saved.focus);
      if (focused) {
        focused.focus({preventScroll: true});
        if (saved.selection && focused.setSelectionRange) focused.setSelectionRange(...saved.selection);
      }
    }
    function historyPosition(state) {
      const saved = state && state.openServer;
      return saved && saved.url === normalize(win.location.href).href ? saved.position : undefined;
    }
    function remember() {
      // During an uncached history load the URL already describes the target,
      // while the old view stays visible. Never attach its scroll to that URL.
      if (doc.hidden || restoringInitialPosition || header().dataset.loadError || normalize(win.location.href).href !== currentURL.href) return;
      win.history.replaceState({...win.history.state, openServer: {url: currentURL.href, position: position()}}, '', currentURL.href);
    }
    function parse(html) {
      const parsed = new win.DOMParser().parseFromString(html, 'text/html');
      if (parsed.querySelector('script') || ids.some(id => !parsed.getElementById(id))) throw new Error('Invalid folder response');
      const canonical = normalize(parsed.getElementById('directory-header').dataset.url);
      return {parsed, canonical};
    }
    function install(parsed, saved, stale) {
      if (dispose) dispose();
      for (const id of ids) doc.getElementById(id).replaceWith(doc.importNode(parsed.getElementById(id), true));
      dispose = options.mount();
      if (stale) {
        // Cached application state is only a hint until the fresh view arrives.
        for (const control of doc.querySelectorAll('[data-app-control], .app-stop-column')) control.inert = true;
      }
      restore(saved);
    }
    function commit(request, url) {
      currentURL = url;
      const state = {...win.history.state, openServer: {url: url.href, position: position()}};
      if (request.committed || request.history || url.href === normalize(win.location.href).href) {
        win.history.replaceState(state, '', url.href);
      } else {
        win.history.pushState(state, '', url.href);
      }
      request.committed = true;
    }
    function scheduleRetry(request) {
      win.clearTimeout(retryTimer);
      if (doc.hidden || pending !== request || !request.retrying) return;
      retryTimer = win.setTimeout(() => load(request), Math.max(0, request.retryAt - Date.now()));
    }
    async function load(request) {
      if (pending !== request || disposed) return;
      win.clearTimeout(retryTimer);
      request.retrying = false;
      retry.hidden = true;
      const controller = new AbortController();
      request.controller = controller;
      content().setAttribute('aria-busy', 'true');
      win.clearTimeout(loadingTimer);
      loadingTimer = win.setTimeout(() => {
        if (pending === request) announce((request.committed || request.preserve ? 'Refreshing ' : 'Loading ') + request.url.searchParams.get('path') + '…');
      }, 150);
      try {
        const endpoint = new URL('/browse', request.url);
        endpoint.search = request.url.search;
        const response = await win.fetch(endpoint.href, {cache: 'no-store', credentials: 'same-origin', redirect: 'error', signal: controller.signal});
        if (pending !== request || controller.signal.aborted) return;
        if (response.status === 409) {
          pending = null;
          if (request.history || request.committed) win.location.replace(request.url.href);
          else win.location.assign(request.url.href);
          return;
        }
        if (!response.ok) {
          const error = new Error((await response.text()).trim() || 'Could not read this folder');
          error.status = response.status;
          error.delay = retryDelay(response.headers.get('Retry-After'));
          throw error;
        }
        const html = await response.text();
        if (pending !== request || controller.signal.aborted) return;
        const {parsed, canonical} = parse(html);
        // Capture the latest scroll, so scrolling during a refresh is respected.
        const saved = (request.committed || request.preserve) && currentURL.href === canonical.href ? position() : request.position;
        install(parsed, saved, false);
        cache.set(canonical.href, html);
        commit(request, canonical);
        pending = null;
        announce('Updated ' + canonical.searchParams.get('path') + '.');
      } catch (error) {
        if (pending !== request || controller.signal.aborted) return;
        cache.delete(request.url.href);
        const transient = !error.status || [502, 503, 504].includes(error.status);
        if ([403, 404].includes(error.status)) cache.invalidatePath(request.url.searchParams.get('path'));
        if ([403, 404].includes(error.status) && currentURL.href === request.url.href) {
          // Do not leave an invalid cached listing actionable after revalidation.
          content().replaceChildren();
          const message = doc.createElement('p');
          message.textContent = error.message;
          content().appendChild(message);
        }
        announce('Could not open ' + request.url.searchParams.get('path') + ': ' + error.message + (transient ? ' Retrying when the connection is available.' : ''), true);
        retry.hidden = false;
        request.retrying = transient;
        request.retryAt = Date.now() + (error.delay || 5000);
        scheduleRetry(request);
      } finally {
        if (pending === request || pending === null) {
          win.clearTimeout(loadingTimer);
          content().removeAttribute('aria-busy');
        }
      }
    }
    function navigate(input, {force = false, preserve = false, history = false, position: saved} = {}) {
      const url = normalize(input);
      if (pending && pending.controller) pending.controller.abort();
      win.clearTimeout(loadingTimer);
      win.clearTimeout(retryTimer);
      options.beforeNavigate();
      if (!history) remember();
      const request = {url, preserve, history, position: saved || (preserve ? position() : {x: 0, y: 0, tableX: 0, focus: 'directory-content'}), committed: false};
      pending = request;
      retry.hidden = true;
      const html = !force && cache.get(url.href);
      if (html) {
        const {parsed, canonical} = parse(html);
        install(parsed, request.position, true);
        commit(request, canonical);
        announce('Refreshing ' + canonical.searchParams.get('path') + '…');
      }
      return load(request);
    }
    function changed(endpoint) {
      const path = new URL(endpoint, win.location.href).searchParams.get('path');
      cache.invalidatePath(path);
      // A read started before a write completed must not replace its result.
      if (pending && pending.url.searchParams.get('path') === path) {
        const request = pending;
        navigate(request.url, {force: true, preserve: true, history: request.history || request.committed, position: request.position});
      } else if (!pending && currentURL.searchParams.get('path') === path) {
        navigate(currentURL, {force: true, preserve: true});
      }
    }
    doc.addEventListener('click', event => {
      const link = event.target.closest && event.target.closest('a');
      if (!link || event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || link.target || link.hasAttribute('download')) return;
      if (!link.hasAttribute('data-directory-link')) {
        // Save before leaving for a file. Updating history during pagehide can
        // evict the page from the browser's Back/Forward cache.
        remember();
        return;
      }
      event.preventDefault();
      navigate(link.href, {preserve: link.hasAttribute('data-preserve-position')});
    }, {signal: listeners.signal});
    retry.addEventListener('click', () => { if (pending) load(pending); }, {signal: listeners.signal});
    win.addEventListener('popstate', event => {
      navigate(win.location.href, {history: true, position: historyPosition(event.state)});
    }, {signal: listeners.signal});
    win.addEventListener('scroll', () => {
      if (!scrollFrame) scrollFrame = win.requestAnimationFrame(() => { scrollFrame = null; remember(); });
    }, {capture: true, passive: true, signal: listeners.signal});
    doc.addEventListener('visibilitychange', () => {
      if (doc.hidden && scrollFrame) {
        win.cancelAnimationFrame(scrollFrame);
        scrollFrame = null;
      }
      if (pending && pending.retrying) scheduleRetry(pending);
    }, {signal: listeners.signal});
    win.addEventListener('pageshow', event => {
      if (event.persisted) {
        const saved = historyPosition(win.history.state);
        const preserve = !header().dataset.loadError && currentURL.href === normalize(win.location.href).href;
        if (preserve) restore(saved);
        navigate(win.location.href, {history: true, force: true, preserve, position: saved});
      } else if (restoringInitialPosition) {
        restoringInitialPosition = false;
        restore(initialPosition);
        remember();
      }
    }, {signal: listeners.signal});
    const previousScrollRestoration = win.history.scrollRestoration;
    win.history.scrollRestoration = 'manual';
    // Full document returns (for example, Back from a figure) retain this
    // history entry even when the browser cannot retain the directory page.
    // Wait for pageshow so layout and the browser's initial scroll are settled,
    // then restore before saving any position.
    if (header().dataset.loadError) {
      announce(header().dataset.loadError, true);
      navigate(currentURL, {force: true, history: true, position: initialPosition});
    } else {
      cache.set(currentURL.href, snapshot());
      if (!restoringInitialPosition) {
        restore(initialPosition);
        remember();
      }
    }
    return {
      navigate, changed,
      get currentURL() { return currentURL.href; },
      dispose() {
        disposed = true;
        listeners.abort();
        if (pending && pending.controller) pending.controller.abort();
        win.clearTimeout(loadingTimer);
        win.clearTimeout(retryTimer);
        if (scrollFrame) win.cancelAnimationFrame(scrollFrame);
        if (dispose) dispose();
        win.history.scrollRestoration = previousScrollRestoration;
      }
    };
  }

  const api = {DirectoryCache, normalizeURL, retryDelay, createNavigation};
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  else root.OpenServerBrowser = api;
})(globalThis);
