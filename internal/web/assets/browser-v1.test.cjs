const {test} = require('node:test');
const assert = require('node:assert/strict');
const {DirectoryCache, normalizeURL, retryDelay} = require('./browser-v1.js');

test('directory cache expires without extending freshness on access', () => {
  let now = 0;
  const cache = new DirectoryCache({now: () => now});
  cache.set('a', 'listing');
  now = 59999;
  assert.equal(cache.get('a'), 'listing');
  now = 60000;
  assert.equal(cache.get('a'), null);
  assert.equal(cache.bytes, 0);
});

test('directory cache bounds memory and evicts the least recently used listing', () => {
  const cache = new DirectoryCache({maxEntries: 2, maxBytes: 12});
  cache.set('a', 'ab');
  cache.set('b', 'cd');
  cache.get('a');
  cache.set('c', 'ef');
  assert.equal(cache.get('b'), null);
  cache.set('a', 'abcd');
  assert.equal(cache.bytes, 12);
  cache.set('d', 'ghij');
  assert.equal(cache.get('a'), null);
  assert.equal(cache.get('d'), 'ghij');
  cache.set('d', 'too large');
  assert.equal(cache.get('d'), null);
  assert.equal(cache.bytes, 0);
});

test('write invalidation removes every sort and hidden variant of its destination', () => {
  const cache = new DirectoryCache();
  cache.set('http://localhost/?path=%2Fa&sort=name', 'one');
  cache.set('http://localhost/?path=%2Fa&sort=size&hidden=1', 'two');
  cache.set('http://localhost/?path=%2Fb', 'three');
  cache.invalidatePath('/a');
  assert.equal(cache.entries.size, 1);
  assert.equal(cache.get('http://localhost/?path=%2Fb'), 'three');
});

test('folder URL normalization shares server defaults and excludes credentials', () => {
  const base = 'http://127.0.0.1:43210/';
  assert.equal(normalizeURL('/?token=secret&sort=invalid&order=invalid#file', base, '/root').href,
    base + '?order=asc&path=%2Froot&sort=name');
  assert.equal(normalizeURL('/?sort=size&hidden=1&path=%2Froot%2FA&order=desc', base, '/root').href,
    base + '?hidden=1&order=desc&path=%2Froot%2FA&sort=size');
  assert.throws(() => normalizeURL('https://elsewhere.example/', base, '/root'));
  assert.throws(() => normalizeURL('/preview?path=notes', base, '/root'));
});

test('retry delay honors seconds and HTTP dates without rapid retry loops', () => {
  assert.equal(retryDelay(null), 5000);
  assert.equal(retryDelay('7'), 7000);
  assert.equal(retryDelay('0'), 1000);
  assert.equal(retryDelay('garbage'), 5000);
  assert.equal(retryDelay('Thu, 01 Jan 1970 00:00:10 GMT', 2000), 8000);
});
