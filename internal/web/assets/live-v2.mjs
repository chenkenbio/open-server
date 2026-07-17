import {
  AnnotationMode,
  GlobalWorkerOptions,
  getDocument,
} from './pdfjs-5.7.284/build/pdf.min.mjs';

const assetBase = new URL('./pdfjs-5.7.284/', import.meta.url);
GlobalWorkerOptions.workerSrc = new URL(
  'build/pdf.worker.min.mjs',
  assetBase,
).href;

// pdf_viewer.mjs reads globalThis.pdfjsLib during module evaluation. The core
// import above must finish before this dynamic import starts.
const { EventBus, LinkTarget, PDFLinkService, PDFViewer } = await import(
  './pdfjs-5.7.284/web/pdf_viewer.mjs'
);

const config = document.body.dataset;
const title = config.title;
const previewURL = config.previewUrl;
const downloadURL = config.downloadUrl;
const statusURL = config.statusUrl;
const status = document.getElementById('status');
const controls = document.getElementById('viewerControls');
const previousPage = document.getElementById('previousPage');
const nextPage = document.getElementById('nextPage');
const pageNumber = document.getElementById('pageNumber');
const pageCount = document.getElementById('pageCount');
const zoomOut = document.getElementById('zoomOut');
const zoomIn = document.getElementById('zoomIn');
const zoomPreset = document.getElementById('zoomPreset');
const zoomCustom = document.getElementById('zoomCustom');
const download = document.getElementById('download');
const viewerContainer = document.getElementById('viewerContainer');
const viewerElement = document.getElementById('viewer');

const eventBus = new EventBus();
const linkService = new PDFLinkService({
  eventBus,
  externalLinkTarget: LinkTarget.BLANK,
  externalLinkRel: 'noopener noreferrer',
});
const pdfViewer = new PDFViewer({
  container: viewerContainer,
  viewer: viewerElement,
  eventBus,
  linkService,
  enableScripting: false,
  imageResourcesPath: new URL('web/images/', assetBase).href,
  annotationMode: AnnotationMode.ENABLE,
});
linkService.setViewer(pdfViewer);

let activeDocument = null;
let currentPage = 1;
let currentScaleValue = 'page-width';
let displayedVersion = '';
let loadingVersion = '';
let loadingTask = null;
let loadGeneration = 0;
let installing = false;
let pendingVersion = '';
let stablePolls = 0;

/** Clamp a requested page to the available document range. */
function clampPage(value, totalPages) {
  const parsed = Number.isFinite(value) ? Math.trunc(value) : 1;
  return Math.min(Math.max(parsed, 1), Math.max(totalPages, 1));
}

/** Replace the status suffix while retaining the current PDF name. */
function setStatus(message) {
  status.textContent = `Live PDF: ${title} — ${message}`;
}

/** Update page controls from the committed viewer state. */
function updatePageControls() {
  const totalPages = activeDocument?.numPages ?? 1;
  pageNumber.value = String(currentPage);
  pageNumber.max = String(totalPages);
  pageCount.textContent = `/ ${totalPages}`;
  previousPage.disabled = currentPage <= 1;
  nextPage.disabled = currentPage >= totalPages;
}

/** Return the preset option matching a PDF.js scale value, if any. */
function matchingZoomPreset(scaleValue) {
  for (const option of zoomPreset.options) {
    if (option.value === scaleValue) {
      return option.value;
    }
  }

  const numericScale = Number(scaleValue);
  if (!Number.isFinite(numericScale)) {
    return '';
  }
  for (const option of zoomPreset.options) {
    if (option.value && Number(option.value) === numericScale) {
      return option.value;
    }
  }
  return '';
}

/** Format a PDF.js scale as a compact percentage. */
function formatZoomPercent(scale) {
  const percent = Math.round(scale * 10000) / 100;
  return `${percent}%`;
}

/** Synchronize the single zoom control with the viewer. */
function updateZoomControl() {
  const preset = matchingZoomPreset(currentScaleValue);
  if (preset) {
    zoomPreset.value = preset;
  } else {
    zoomCustom.textContent = formatZoomPercent(pdfViewer.currentScale);
    zoomPreset.value = '';
  }
}

/** Apply a PDF.js scale value and make it authoritative for reloads. */
function setZoom(scaleValue) {
  if (!activeDocument || installing) {
    return false;
  }

  // PDF.js does not emit scalechanging when a numeric value resolves to the
  // current scale, so commit our state before calling its setter.
  currentScaleValue = scaleValue;
  pdfViewer.currentScaleValue = scaleValue;
  updateZoomControl();
  return true;
}

/** Move to a user-requested page and make the clamped page authoritative. */
function showPage(value) {
  if (!activeDocument || installing) {
    return;
  }
  currentPage = clampPage(Number(value), activeDocument.numPages);
  pdfViewer.currentPageNumber = currentPage;
  updatePageControls();
}

/** Create a cache-distinct URL for one completed PDF version. */
function versionedPreviewURL(version) {
  const url = new URL(previewURL, window.location.href);
  url.searchParams.set('live', version);
  return url.href;
}

/** Wait for PDF.js to initialize the exact document being installed. */
function waitForPagesInit(documentProxy, generation) {
  let listener;
  const promise = new Promise(resolve => {
    listener = () => {
      eventBus.off('pagesinit', listener);
      resolve(
        generation === loadGeneration &&
          pdfViewer.pdfDocument === documentProxy,
      );
    };
    eventBus.on('pagesinit', listener);
  });
  return {
    promise,
    cancel: () => eventBus.off('pagesinit', listener),
  };
}

/** Install a document and fail if PDF.js cannot initialize its first page. */
async function installViewerDocument(documentProxy, generation) {
  const pagesInit = waitForPagesInit(documentProxy, generation);
  pdfViewer.setDocument(documentProxy);
  linkService.setDocument(documentProxy);
  const pagesPromise = pdfViewer.pagesPromise;

  try {
    return await Promise.race([
      pagesInit.promise,
      pagesPromise.then(
        () =>
          generation === loadGeneration &&
          pdfViewer.pdfDocument === documentProxy,
        error => {
          throw error;
        },
      ),
    ]);
  } finally {
    pagesInit.cancel();
  }
}

/** Load and install a completed PDF while preserving its visible page. */
async function loadVersion(version) {
  if (!version || loadingVersion) {
    return;
  }

  const generation = ++loadGeneration;
  loadingVersion = version;
  setStatus(activeDocument ? 'loading update' : 'loading');

  const task = getDocument({
    url: versionedPreviewURL(version),
    cMapUrl: new URL('cmaps/', assetBase).href,
    cMapPacked: true,
    standardFontDataUrl: new URL('standard_fonts/', assetBase).href,
    wasmUrl: new URL('wasm/', assetBase).href,
    iccUrl: new URL('iccs/', assetBase).href,
    enableXfa: false,
    isEvalSupported: false,
  });
  loadingTask = task;

  let documentProxy = null;
  let candidateInstalled = false;
  let previousDocument = activeDocument;
  let pageToRestore = currentPage;
  let scaleToRestore = currentScaleValue;
  try {
    documentProxy = await task.promise;
    await documentProxy.getPage(1);
    if (generation !== loadGeneration) {
      await documentProxy.destroy();
      return;
    }

    // Capture the latest page only after the replacement has loaded, so page
    // changes made while compiling are retained.
    pageToRestore = currentPage;
    scaleToRestore = currentScaleValue;
    previousDocument = activeDocument;
    const pageForCandidate = clampPage(
      pageToRestore,
      documentProxy.numPages,
    );

    installing = true;
    candidateInstalled = true;

    if (!(await installViewerDocument(documentProxy, generation))) {
      installing = false;
      return;
    }

    pdfViewer.currentScaleValue = scaleToRestore;
    currentPage = pageForCandidate;
    pdfViewer.currentPageNumber = currentPage;
    activeDocument = documentProxy;
    displayedVersion = version;
    controls.hidden = false;
    updatePageControls();
    updateZoomControl();
    installing = false;
    setStatus(`page ${currentPage} of ${documentProxy.numPages}; refreshed ${new Date().toLocaleTimeString()}`);

    if (previousDocument && previousDocument !== documentProxy) {
      void previousDocument.destroy().catch(() => {});
    }
  } catch (error) {
    if (generation === loadGeneration) {
      let showingPrevious =
        !candidateInstalled &&
        activeDocument &&
        pdfViewer.pdfDocument === activeDocument;

      if (
        candidateInstalled &&
        documentProxy &&
        pdfViewer.pdfDocument === documentProxy
      ) {
        if (previousDocument) {
          try {
            const restored = await installViewerDocument(
              previousDocument,
              generation,
            );
            if (restored) {
              currentPage = clampPage(
                pageToRestore,
                previousDocument.numPages,
              );
              pdfViewer.currentScaleValue = scaleToRestore;
              pdfViewer.currentPageNumber = currentPage;
              activeDocument = previousDocument;
              controls.hidden = false;
              updatePageControls();
              updateZoomControl();
              showingPrevious = true;
            }
          } catch (rollbackError) {
            console.error(rollbackError);
          }
        } else {
          pdfViewer.setDocument(null);
          linkService.setDocument(null);
          activeDocument = null;
          controls.hidden = true;
        }
      }

      installing = false;
      setStatus(
        showingPrevious
          ? 'update failed; showing previous PDF'
          : 'could not load PDF',
      );
      console.error(error);
    }
    if (documentProxy && documentProxy !== activeDocument) {
      void documentProxy.destroy().catch(() => {});
    }
  } finally {
    if (generation === loadGeneration) {
      loadingTask = null;
      loadingVersion = '';
      pendingVersion = '';
      stablePolls = 0;
    }
  }
}

/** Poll for completed PDF versions and wait for two stable observations. */
async function poll() {
  try {
    const response = await fetch(statusURL, { cache: 'no-store' });
    if (!response.ok) {
      throw new Error(`status ${response.status}`);
    }
    const data = await response.json();
    const version = data.version || '';

    if (!data.ready) {
      pendingVersion = '';
      stablePolls = 0;
      setStatus(activeDocument ? 'waiting for PDF compilation; showing previous PDF' : 'waiting for PDF compilation');
      return;
    }
    if (!version || version === displayedVersion || version === loadingVersion) {
      pendingVersion = '';
      stablePolls = 0;
      return;
    }
    if (version === pendingVersion) {
      stablePolls += 1;
    } else {
      pendingVersion = version;
      stablePolls = 1;
    }
    if (stablePolls >= 2) {
      await loadVersion(version);
    }
  } catch (error) {
    setStatus(activeDocument ? 'status unavailable; showing previous PDF' : 'waiting for the file');
  }
}

eventBus.on('pagechanging', event => {
  if (!installing) {
    currentPage = clampPage(event.pageNumber, activeDocument?.numPages ?? 1);
    updatePageControls();
  }
});

eventBus.on('scalechanging', event => {
  if (!installing) {
    currentScaleValue = event.presetValue || String(event.scale);
    updateZoomControl();
  }
});

previousPage.addEventListener('click', () => showPage(currentPage - 1));
nextPage.addEventListener('click', () => showPage(currentPage + 1));
pageNumber.addEventListener('change', () => showPage(Number(pageNumber.value)));
pageNumber.addEventListener('keydown', event => {
  if (event.key === 'Enter') {
    event.preventDefault();
    showPage(Number(pageNumber.value));
    pageNumber.select();
  }
});
pageNumber.addEventListener('blur', () => updatePageControls());

zoomOut.addEventListener('click', () => {
  if (activeDocument && !installing) {
    setZoom(String(Math.max(pdfViewer.currentScale / 1.25, 0.1)));
  }
});
zoomIn.addEventListener('click', () => {
  if (activeDocument && !installing) {
    setZoom(String(Math.min(pdfViewer.currentScale * 1.25, 10)));
  }
});

zoomPreset.addEventListener('change', () => setZoom(zoomPreset.value));

download.addEventListener('click', () => {
  window.location.href = downloadURL;
});

window.addEventListener('beforeunload', () => {
  loadGeneration += 1;
  void loadingTask?.destroy();
  void activeDocument?.destroy();
});

if (config.initialReady === 'true' && config.initialVersion) {
  void loadVersion(config.initialVersion);
}
window.setInterval(() => void poll(), 2000);
