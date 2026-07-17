# PDF.js runtime assets

This directory contains the runtime subset of `pdfjs-dist` 5.7.284 used by
the `open-server` live PDF viewer. It is sourced from the official npm tarball:

- URL: `https://registry.npmjs.org/pdfjs-dist/-/pdfjs-dist-5.7.284.tgz`
- SHA-1: `01d9ff1d7ccd15245bdec8524a6770feb77fbb23`
- Integrity: `sha512-h4EdYQczmGhbOlqc3PPZwxevn7ApdWPbovAuWXOB/DjIyigSnwfy2oze7c6mRcSr9XgLp3eN3EeL4DyySTPMFw==`

Only the minified core and worker, reusable viewer module and stylesheet, and
their runtime compatibility resources are vendored. Source maps, legacy
builds, type declarations, examples, and Node-specific files are excluded.

PDF.js is licensed under Apache-2.0. The upstream license and the licenses for
the bundled CMaps, fonts, ICC profiles, and WebAssembly decoders are preserved
beside their corresponding assets.
