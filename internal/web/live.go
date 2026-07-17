package web

import "html/template"

var livePDFPage = template.Must(template.New("live-pdf").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Live PDF — {{.Title}}</title>
<link rel="stylesheet" href="{{.PDFJSStylesURL}}">
<link rel="stylesheet" href="{{.StylesURL}}">
</head>
<body data-title="{{.Title}}" data-preview-url="{{.PreviewURL}}" data-download-url="{{.DownloadURL}}" data-status-url="{{.StatusURL}}" data-initial-version="{{.InitialVersion}}" data-initial-ready="{{.InitialReady}}">
{{if .PlainHTTP}}<div role="alert" class="plain-http-warning"><strong>Security warning:</strong> Token-protected plain HTTP; traffic is not encrypted.</div>{{end}}
<div class="live-toolbar">
  <div id="status" role="status">Live PDF: {{.Title}} — {{if .InitialReady}}loading{{else}}waiting for PDF compilation{{end}}</div>
  <div id="viewerControls" class="viewer-controls" hidden>
    <div class="control-group">
      <button id="previousPage" type="button" aria-label="Previous page" title="Previous page">&#x2191;</button>
      <label for="pageNumber">Page</label>
      <input id="pageNumber" type="number" min="1" step="1" value="1" inputmode="numeric">
      <span id="pageCount">/ 1</span>
      <button id="nextPage" type="button" aria-label="Next page" title="Next page">&#x2193;</button>
    </div>
    <span class="control-divider" aria-hidden="true"></span>
    <div class="control-group">
      <button id="zoomOut" type="button" aria-label="Zoom out" title="Zoom out">&#x2212;</button>
      <select id="zoomPreset" aria-label="Zoom level" title="Zoom level">
        <option id="zoomCustom" value="" disabled>Custom</option>
        <option value="page-width" selected>Fit width</option>
        <option value="page-fit">Fit page</option>
        <option value="page-actual">Actual size</option>
        <option value="0.5">50%</option>
        <option value="0.75">75%</option>
        <option value="1">100%</option>
        <option value="1.25">125%</option>
        <option value="1.5">150%</option>
        <option value="2">200%</option>
        <option value="3">300%</option>
        <option value="4">400%</option>
      </select>
      <button id="zoomIn" type="button" aria-label="Zoom in" title="Zoom in">+</button>
    </div>
    <span class="control-divider" aria-hidden="true"></span>
    <button id="download" type="button" aria-label="Download PDF" title="Download PDF"><svg viewBox="0 0 24 24" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v10m0 0 4-4m-4 4-4-4M5 17v3h14v-3"></path></svg></button>
  </div>
</div>
<div class="viewer-host">
  <div id="viewerContainer" tabindex="0">
    <div id="viewer" class="pdfViewer"></div>
  </div>
</div>
<script type="module" src="{{.ControllerURL}}"></script>
</body>
</html>`))
