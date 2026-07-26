package web

const directoryTemplate = `{{define "app-start-icon"}}<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9.25"></circle><path d="m9.75 8.25 6.5 3.75-6.5 3.75z" fill="currentColor" stroke="none"></path></svg>{{end}}{{define "app-stop-icon"}}<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9.25"></circle><rect x="8.5" y="8.5" width="7" height="7" rx="1" fill="currentColor" stroke="none"></rect></svg>{{end}}{{define "app-loading-icon"}}<svg class="app-loading-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="9" opacity="0.25"></circle><path d="M21 12a9 9 0 0 0-9-9"></path></svg>{{end}}{{define "copy-path-icon"}}<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="8" y="8" width="12" height="12" rx="2"></rect><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2"></path></svg>{{end}}{{define "download-icon"}}<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v10m0 0 4-4m-4 4-4-4M5 17v3h14v-3"></path></svg>{{end}}{{define "app-controls"}}{{if .Endpoint}}<td class="action-column app-primary-column" data-app-control data-app="{{.App}}" data-label="{{.Label}}" data-endpoint="{{.Endpoint}}" data-state="{{.State}}">
<button type="button" class="app-start icon-button" aria-label="Start {{.Label}}" title="Start {{.Label}}" {{if ne .State "stopped"}}hidden{{end}}>{{template "app-start-icon"}}</button>
<button type="button" class="app-starting icon-button" aria-label="{{.Label}} is starting" title="{{.Label}} is starting" disabled aria-busy="true" {{if ne .State "starting"}}hidden{{end}}>{{if eq .App "jupyter"}}{{template "app-loading-icon"}}{{else}}{{template "app-start-icon"}}{{end}}</button>
<a class="app-open icon-button" href="{{if .OpenURL}}{{.OpenURL}}{{else}}#{{end}}" target="_blank" rel="noopener" aria-label="Open {{.Label}} in a new tab" title="Open {{.Label}} in a new tab" {{if not (and (eq .State "running") .Available)}}hidden{{end}}><svg viewBox="0 0 24 24" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 5h5v5"></path><path d="m10 14 9-9"></path><path d="M19 13v6H5V5h6"></path></svg></a>
<button type="button" class="app-unavailable icon-button" aria-label="{{.Label}} is reconnecting or unavailable" title="{{.Label}} is reconnecting or unavailable" disabled {{if or (ne .State "running") .Available}}hidden{{end}}><svg viewBox="0 0 24 24" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 5h5v5"></path><path d="m10 14 9-9"></path><path d="M19 13v6H5V5h6"></path></svg></button>
<button type="button" class="app-stopping-primary icon-button" aria-label="{{.Label}} is stopping" title="{{.Label}} is stopping" disabled aria-busy="true" {{if ne .State "stopping"}}hidden{{end}}>{{template "app-stop-icon"}}</button>
</td><td class="action-column app-stop-column">
<button type="button" class="app-stop icon-button" data-app="{{.App}}" data-label="{{.Label}}" data-endpoint="{{.Endpoint}}" aria-label="Stop {{.Label}}" title="Stop {{.Label}}" {{if ne .State "running"}}hidden{{end}}>{{template "app-stop-icon"}}</button>
<button type="button" class="app-stopping icon-button" aria-label="{{.Label}} is stopping" title="{{.Label}} is stopping" disabled aria-busy="true" {{if ne .State "stopping"}}hidden{{end}}>{{template "app-stop-icon"}}</button>
</td>{{else}}<td class="action-column app-primary-column">&nbsp;</td><td class="action-column app-stop-column">&nbsp;</td>{{end}}{{end}}<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
*, *::before, *::after { box-sizing: border-box; }
body { margin: 0; padding: 0.8em 1.2em 2em; color: #111; font-family: system-ui, -apple-system, sans-serif; font-size: {{.FontSize}}px; }
button, input, select, textarea { font-size: inherit; }
.page-header { padding: 0.4em 0 0.6em; border-bottom: 1px solid #e0e0e0; margin-bottom: 0.7em; }
.header-path { display: flex; flex-wrap: wrap; align-items: baseline; gap: 0.2em; margin-bottom: 0.35em; color: #555; font-size: 0.92em; }
.site-name { color: #111; font-size: 1.05em; font-weight: 700; letter-spacing: -0.01em; }
.header-separator { padding: 0 0.1em; color: #bbb; font-size: 0.85em; }
.host-name { color: #444; font-family: ui-monospace, monospace; font-size: 0.88em; }
.root-path { min-width: 0; color: #444; font-family: ui-monospace, monospace; font-size: 0.88em; overflow-wrap: anywhere; }
.breadcrumb { min-width: 0; overflow-wrap: anywhere; }
.breadcrumb a { color: #1a5cba; text-decoration: none; }
.breadcrumb a:hover { text-decoration: underline; }
.header-actions, #upload-buttons { display: flex; flex-wrap: wrap; gap: 0.4em; }
.header-actions form { margin: 0; }
.header-actions button, #drop-zone button { padding: 0.2em 0.65em; border: 1px solid #ccc; border-radius: 4px; background: #f7f7f7; color: #333; font: inherit; font-size: 0.85em; cursor: pointer; }
.header-actions button:hover, #drop-zone button:hover { border-color: #aaa; background: #ececec; }
#drop-zone { display: flex; flex-wrap: wrap; align-items: center; gap: 0.4em; margin: 0 0 0.7em; }
#drop-zone.dragging { outline: 2px dashed #4a8fe8; outline-offset: 4px; border-radius: 4px; background: #f0f4ff; }
.upload-hint { color: #666; font-size: 0.85em; }
#upload-status { min-height: 1.2em; margin-left: auto; color: #666; font-family: ui-monospace, monospace; font-size: 0.82em; }
#url-row { display: flex; gap: 0.35em; }
#url-input { padding: 0.2em 0.5em; border: 1px solid #ccc; border-radius: 4px; font: inherit; }
.regex-filter-controls { position: relative; display: flex; flex-wrap: wrap; align-items: center; gap: 0.4em; margin: 0 0 0.7em; }
.regex-filter-controls > label:first-child { font-size: 0.88em; font-weight: 600; }
#regex-filter-input { width: 24em; max-width: 60vw; padding: 0.2em 0.5em; border: 1px solid #ccc; border-radius: 4px; font-family: ui-monospace, monospace; }
#regex-filter-input[aria-invalid="true"] { border-color: #a33; outline-color: #a33; }
.regex-filter-controls button { padding: 0.2em 0.65em; border: 1px solid #ccc; border-radius: 4px; background: #f7f7f7; color: #333; cursor: pointer; }
.regex-filter-controls button:hover { border-color: #aaa; background: #ececec; }
#regex-filter-help-toggle { display: inline-grid; place-items: center; width: 1.05rem; height: 1.05rem; padding: 0; border: 1px solid #bbb; border-radius: 50%; background: transparent; color: #666; font-size: 0.68rem; font-weight: 600; line-height: 1; }
#regex-filter-help-toggle:hover, #regex-filter-help-toggle:focus-visible { border-color: #777; background: #fff; color: #222; }
#regex-filter-help-toggle:focus-visible { outline: 2px solid #1a5cba; outline-offset: 2px; }
.regex-ignore-case { display: inline-flex; align-items: center; gap: 0.25em; font-size: 0.88em; }
#regex-filter-error { flex-basis: 100%; min-height: 1.2em; color: #a33; font-size: 0.82em; }
#regex-filter-status { margin-left: auto; color: #666; font-family: ui-monospace, monospace; font-size: 0.82em; }
.regex-filter-controls.filter-active #regex-filter-input:not([aria-invalid="true"]) { border-color: #1a5cba; background: #f0f4ff; }
.regex-filter-controls.filter-active #regex-filter-status { color: #1a5cba; font-weight: 600; }
#regex-filter-badge { display: none; padding: 0.05em 0.5em; border-radius: 999px; background: #1a5cba; color: #fff; font-size: 0.72em; font-weight: 700; letter-spacing: 0.04em; text-transform: uppercase; }
.regex-filter-controls.filter-active #regex-filter-badge { display: inline-block; }
#regex-filter-help { position: absolute; top: calc(100% + 0.25em); left: 0; z-index: 20; width: min(34em, calc(100vw - 2.4em)); padding: 0.85em 1em; border: 1px solid #bbb; border-radius: 6px; background: #fff; box-shadow: 0 4px 16px rgba(0, 0, 0, 0.16); color: #222; font-size: 0.9em; }
#regex-filter-help[hidden] { display: none; }
#regex-filter-help h2 { margin: 0 2em 0.45em 0; font-size: 1em; }
#regex-filter-help p { margin: 0.35em 0; }
#regex-filter-help ul { margin: 0.45em 0; padding-left: 1.5em; }
#regex-filter-help code { font-family: ui-monospace, monospace; }
#regex-filter-help-close { position: absolute; top: 0.45em; right: 0.45em; }
.table-scroll { width: 100%; max-width: 100%; overflow-x: auto; }
table {
  --column-name-width: 28em;
  --column-modified-width: 12.5em;
  --column-size-width: 6.5em;
  --jupyter-columns-width: 0px;
  --tensorboard-columns-width: 0px;
  --latex-columns-width: 0px;
  width: calc(var(--column-name-width) + var(--column-modified-width) + var(--column-size-width) + 4.7em + var(--jupyter-columns-width) + var(--tensorboard-columns-width) + var(--latex-columns-width));
  max-width: none;
  table-layout: fixed;
  border-collapse: collapse;
  font-size: 0.95em;
}
table.has-jupyter { --jupyter-columns-width: 6em; }
table.has-tensorboard { --tensorboard-columns-width: 6em; }
table.has-latex { --latex-columns-width: 6.75em; }
col.column-name { width: var(--column-name-width); }
col.column-modified { width: var(--column-modified-width); }
col.column-size { width: var(--column-size-width); }
col.column-file-action { width: 2.35em; }
col.column-app-action { width: 3em; }
col.column-latex-action { width: 2.25em; }
th { position: relative; padding: 0.23em 0.5em; border-bottom: 2px solid #e0e0e0; color: #666; font-size: 0.82em; font-weight: 600; letter-spacing: 0.03em; text-transform: uppercase; white-space: nowrap; }
th a { color: inherit; text-decoration: none; }
th a:hover { color: #333; text-decoration: underline; }
td { padding: 0.21em 0.5em; border-bottom: 1px solid #f0f0f0; vertical-align: middle; white-space: nowrap; }
tbody tr:hover td, tbody tr:focus-within td { background: #f0f4ff; }
.name-cell-content, .modified-cell-content, .size-cell-content { display: block; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.column-resizer { position: absolute; top: 0; right: -0.3em; z-index: 3; width: 0.6em; height: 100%; cursor: col-resize; touch-action: none; }
.column-resizer::after { position: absolute; top: 8%; bottom: 8%; left: calc(50% - 0.5px); width: 1px; background: #c8ccd2; content: ""; }
.column-resizer:hover::after, .column-resizer:focus-visible::after, .column-resizer.resizing::after { left: calc(50% - 1px); width: 2px; background: #4a8fe8; }
.column-resizer:focus-visible { outline: 2px solid #1a5cba; outline-offset: -2px; }
.app-group-heading { border-bottom: 0; }
.app-group-heading img { display: block; width: 1.3em; height: 1.3em; margin: 0 auto; object-fit: contain; }
.app-subheading { padding-right: 0.4em; padding-left: 0.4em; font-size: 0.78em; font-weight: 500; letter-spacing: 0; line-height: 1; text-align: center; text-transform: none; }
#app-action-status { min-height: 1.2em; margin: -0.25em 0 0.35em; color: #555; font-size: 0.82em; }
#app-action-status:empty { display: none; }
#app-action-status[data-kind="error"] { color: #a33; }
.latex-logo { font-family: "Times New Roman", Times, serif; font-weight: normal; white-space: nowrap; }
.latex-logo-a { font-size: 0.72em; vertical-align: 0.32em; margin-left: -0.28em; margin-right: -0.12em; }
.latex-logo-e { display: inline-block; font-size: 0.85em; vertical-align: -0.18em; margin-left: -0.08em; margin-right: -0.05em; }
.latex-tools-heading { display: flex; flex-direction: column; align-items: center; gap: 0.25em; }
.latex-env-mode { display: flex; align-items: center; gap: 0.3em; color: #666; font-size: 0.78em; font-weight: 500; letter-spacing: 0; line-height: 1; text-transform: none; cursor: pointer; }
.latex-env-switch { position: relative; width: 2em; height: 1.1em; margin: 0; border: 1px solid #aaa; border-radius: 999px; appearance: none; -webkit-appearance: none; background: #ddd; cursor: pointer; }
.latex-env-switch::after { position: absolute; top: 0.13em; left: 0.14em; width: 0.72em; height: 0.72em; border-radius: 50%; background: #fff; box-shadow: 0 1px 2px rgba(0, 0, 0, 0.3); content: ""; transition: transform 120ms ease; }
.latex-env-switch:checked { border-color: #356fba; background: #4a8fe8; }
.latex-env-switch:checked::after { transform: translateX(0.86em); }
.latex-env-switch:focus-visible { outline: 2px solid #1a5cba; outline-offset: 2px; }
.icon-button { padding: 0.15em; border: 1px solid transparent; border-radius: 3px; background: transparent; color: #555; cursor: pointer; }
.icon-button:hover, .icon-button:focus { border-color: #aaa; background: #e8eaf0; color: #111; }
.icon-button svg, .icon-button img { display: block; width: 1.3em; height: 1.3em; object-fit: contain; }
.app-loading-icon { transform-origin: center; animation: app-icon-spin 0.8s linear infinite; }
@keyframes app-icon-spin { to { transform: rotate(360deg); } }
.copy-result { position: relative; color: transparent !important; }
.copy-result svg, .copy-result img { visibility: hidden; }
.copy-result::after { position: absolute; inset: 0; display: grid; place-items: center; font-weight: 700; }
.copy-success { border-color: #a8d5b5 !important; background: #edf8f0 !important; }
.copy-success::after { color: #555; content: "\2713"; }
.copy-failure { border-color: #e1aaaa !important; background: #fff0f0 !important; }
.copy-failure::after { color: #a33; content: "\00d7"; }
.visually-hidden { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }
.file-action-column { padding-right: 0.2em; padding-left: 0.2em; text-align: left; }
.action-column { box-sizing: border-box; width: 2.25em; min-width: 2.25em; max-width: 2.25em; padding-right: 0.2em; padding-left: 0.2em; text-align: center; }
.action-column.app-primary-column, .action-column.app-stop-column { width: 2.5em; min-width: 2.5em; max-width: 2.5em; }
.action-column form { margin: 0; }
.action-column .icon-button { margin: 0 auto; }
.file-action-column .icon-button { margin: 0; }
.icon-button[hidden] { display: none; }
a.icon-button { display: inline-block; }
@media (max-width: 50rem) {
  body { padding-right: 0.7em; padding-left: 0.7em; }
  #upload-status { flex-basis: 100%; margin-left: 0; }
  #regex-filter-input { flex: 1 1 14em; max-width: 100%; }
  #regex-filter-status { flex-basis: 100%; margin-left: 0; }
}
@media (prefers-reduced-motion: reduce) {
  .app-loading-icon { animation: none; }
}
</style>
</head>
<body>
<span id="copy-status" class="visually-hidden" role="status" aria-live="polite"></span>
{{if .PlainHTTPWarning}}<div role="alert" style="margin: 0 0 0.8em 0; padding: 0.7em; border: 1px solid #a66; background: #fff3cd; color: #4d3800; font-family: sans-serif;"><strong>Security warning:</strong> This server uses token-protected plain HTTP. The token controls access, but traffic is not encrypted. Use only on a trusted network.</div>{{end}}
<div class="page-header">
<div class="header-path">
<span class="site-name">open-server</span>
<span class="header-separator">›</span>
<span class="host-name">{{.SSHHost}}</span>
<span class="header-separator">›</span>
<span class="root-path">{{.RootPath}}</span>
<span class="header-separator">›</span>
<span class="breadcrumb">{{with index .Breadcrumbs 0}}<a href="{{.URL}}" title="Session root">.</a>{{end}}{{range $i, $crumb := .Breadcrumbs}}{{if $i}} / <a href="{{$crumb.URL}}">{{$crumb.Name}}</a>{{end}}{{end}}</span>
</div>
<div class="header-actions">
<button id="btn-create-folder" type="button">+ New folder</button>
<button id="btn-toggle-hidden" type="button" data-href="{{.HiddenToggleURL}}">{{.HiddenToggleLabel}}</button>
<button type="button" class="copy-path" data-path="{{.CurrentPath}}">&#x2398; Copy path</button>
{{if .Closeable}}<form action="/close" method="post" onsubmit="return window.confirm('Close open-server and all sessions?');"><input type="hidden" name="csrf" value="{{.CSRFToken}}"><button id="btn-close-server" type="submit">Close open-server</button></form>{{end}}
</div>
</div>
<div id="drop-zone">
<form id="upload-form" action="{{.UploadURL}}" method="POST" enctype="multipart/form-data" style="margin: 0;">
<input id="file-input" type="file" name="file" multiple>
<button type="submit">Upload</button>
</form>
<div id="upload-buttons" style="display: none;">
<button id="btn-upload-files" type="button">&#x2191; Upload files</button>
<button id="btn-paste-file" type="button">Paste file</button>
<button id="btn-from-url" type="button">From URL</button>
</div>
<span class="upload-hint">or drag files here</span>
<div id="url-row" style="display: none;">
<input id="url-input" type="url" placeholder="https://example.com/file.png" style="width: 24em; max-width: 70%;">
<button id="url-fetch" type="button">Fetch</button>
</div>
<span id="upload-status" role="status" aria-live="polite"></span>
</div>
<form id="regex-filter-controls" class="regex-filter-controls" data-directory="{{.CurrentPath}}" data-entry-count="{{len .Entries}}">
<label for="regex-filter-input">Regex filter</label>
<span id="regex-filter-badge" aria-hidden="true">Active</span>
<button id="regex-filter-help-toggle" type="button" aria-label="Show regex syntax help" aria-controls="regex-filter-help" aria-expanded="false" title="Regex syntax help">?</button>
<input id="regex-filter-input" type="text" spellcheck="false" autocomplete="off" aria-describedby="regex-filter-error regex-filter-status" placeholder="For example: \.pdf$">
<label class="regex-ignore-case" for="regex-ignore-case"><input id="regex-ignore-case" type="checkbox"> Ignore case</label>
<button id="regex-filter-apply" type="submit">Apply</button>
<button id="regex-filter-clear" type="button">Clear</button>
<span id="regex-filter-status" role="status" aria-live="polite">{{len .Entries}} of {{len .Entries}} entries</span>
<span id="regex-filter-error" role="alert"></span>
<div id="regex-filter-help" role="dialog" aria-modal="false" aria-labelledby="regex-filter-help-title" hidden>
<button id="regex-filter-help-close" type="button" aria-label="Close regex syntax help">Close</button>
<h2 id="regex-filter-help-title">Regex syntax</h2>
<p>Patterns use JavaScript regular-expression syntax and match entry names. A partial match is enough unless you add anchors.</p>
<ul>
<li><code>\.pdf$</code> matches names ending in <code>.pdf</code>.</li>
<li><code>\.(csv|tsv)$</code> matches CSV or TSV names.</li>
<li><code>^sample_[0-9]+</code> matches a prefix followed by digits.</li>
<li><code>^README$</code> matches that complete name.</li>
</ul>
<p>Use <code>^</code> and <code>$</code> for the start and end, parentheses for groups, <code>|</code> for alternation, square brackets for character classes, and <code>\.</code> for a literal dot. “Ignore case” adds case-insensitive matching. The <code>.</code> and <code>..</code> navigation rows are never filtered.</p>
</div>
</form>
<div id="conflict-modal" role="dialog" aria-modal="true" aria-labelledby="conflict-message" style="display: none; position: fixed; inset: 0; background: rgba(0, 0, 0, 0.25); z-index: 1000; font-family: sans-serif;">
<div style="background: #fff; color: #000; border: 1px solid #777; border-radius: 6px; max-width: 28em; margin: 15vh auto 0 auto; padding: 1em;">
<p id="conflict-message" style="margin: 0 0 0.8em 0;"></p>
<label style="display: block; margin: 0 0 1em 0;"><input id="conflict-apply-all" type="checkbox"> Apply this choice to all remaining conflicts</label>
<div style="text-align: right;">
<button id="conflict-skip" type="button">Skip</button>
<button id="conflict-overwrite" type="button">Overwrite</button>
</div>
</div>
</div>
<div id="paste-modal" role="dialog" aria-modal="true" aria-labelledby="paste-prompt" style="display: none; position: fixed; inset: 0; background: rgba(0, 0, 0, 0.25); z-index: 1000; font-family: sans-serif;">
<div style="background: #fff; color: #000; border: 1px solid #777; border-radius: 6px; max-width: 28em; margin: 15vh auto 0 auto; padding: 1em;">
<p id="paste-prompt" style="margin: 0 0 0.8em 0;">Save pasted file as:</p>
<input id="paste-name" type="text" style="width: 100%; box-sizing: border-box; margin: 0 0 1em 0;">
<div style="text-align: right;">
<button id="paste-cancel" type="button">Cancel</button>
<button id="paste-ok" type="button">Upload</button>
</div>
</div>
</div>
<div id="folder-modal" role="dialog" aria-modal="true" aria-labelledby="folder-prompt" style="display: none; position: fixed; inset: 0; background: rgba(0, 0, 0, 0.25); z-index: 1000; font-family: sans-serif;">
<div style="background: #fff; color: #000; border: 1px solid #777; border-radius: 6px; max-width: 28em; margin: 15vh auto 0 auto; padding: 1em;">
<p id="folder-prompt" style="margin: 0 0 0.8em 0;">Create a folder in the current path:</p>
<input id="folder-name" type="text" placeholder="Folder name" style="width: 100%; box-sizing: border-box; margin: 0 0 1em 0;">
<div style="text-align: right;">
<button id="folder-cancel" type="button">Cancel</button>
<button id="folder-create" type="button">Create folder</button>
</div>
</div>
</div>
{{if .JupyterEnabled}}<div id="jupyter-modal" role="dialog" aria-modal="true" aria-labelledby="jupyter-prompt" style="display: none; position: fixed; inset: 0; background: rgba(0, 0, 0, 0.25); z-index: 1000; font-family: sans-serif;">
<form id="jupyter-form" method="post" style="background: #fff; color: #000; border: 1px solid #777; border-radius: 6px; max-width: 30em; margin: 15vh auto 0 auto; padding: 1em;">
<p id="jupyter-prompt" style="margin: 0 0 0.8em 0;">Select a Python kernel</p>
<label for="jupyter-python" style="display: block; margin: 0 0 0.35em 0;">Python executable</label>
<input id="jupyter-python" name="python" type="text" value="{{.DefaultPython}}" placeholder="/opt/conda/envs/myenv/bin/python" style="width: 100%; box-sizing: border-box; margin: 0 0 0.45em 0;">
<p style="margin: 0 0 1em 0; font-size: 0.9em; color: #555;">Leave blank to use the configured or environment default.</p>
<input type="hidden" name="csrf" value="{{.CSRFToken}}">
<div style="text-align: right;"><button id="jupyter-cancel" type="button">Cancel</button> <button type="submit">Start</button></div>
</form>
</div>{{end}}
<div id="app-action-status" role="status" aria-live="polite"></div>
<div class="table-scroll" tabindex="0" aria-label="Directory listing">
<table id="directory-list" class="{{if .JupyterEnabled}}has-jupyter {{end}}{{if .TensorBoardEnabled}}has-tensorboard {{end}}{{if .LaTeXEnabled}}has-latex{{end}}">
<colgroup>
<col class="column-name" data-column="name">
<col class="column-modified" data-column="modified">
<col class="column-size" data-column="size">
</colgroup>
<colgroup><col class="column-file-action"><col class="column-file-action"></colgroup>
{{if .JupyterEnabled}}<colgroup><col class="column-app-action"><col class="column-app-action"></colgroup>{{end}}
{{if .TensorBoardEnabled}}<colgroup><col class="column-app-action"><col class="column-app-action"></colgroup>{{end}}
{{if .LaTeXEnabled}}<colgroup><col class="column-latex-action"><col class="column-latex-action"><col class="column-latex-action"></colgroup>{{end}}
<thead>
{{$view := .}}<tr>
<th scope="col" align="left"{{if or .JupyterEnabled .TensorBoardEnabled}} rowspan="2"{{end}}><a href="{{.NameSortURL}}">Name{{.NameSortMarker}}</a><span class="column-resizer" role="separator" aria-orientation="vertical" aria-label="Resize Name column" tabindex="0" data-resize-column="name"></span></th>
<th scope="col" align="left"{{if or .JupyterEnabled .TensorBoardEnabled}} rowspan="2"{{end}}><a href="{{.ModifiedSortURL}}">Last modified{{.ModifiedSortMarker}}</a><span class="column-resizer" role="separator" aria-orientation="vertical" aria-label="Resize Last modified column" tabindex="0" data-resize-column="modified"></span></th>
<th scope="col" align="right"{{if or .JupyterEnabled .TensorBoardEnabled}} rowspan="2"{{end}}><a href="{{.SizeSortURL}}">Size{{.SizeSortMarker}}</a><span class="column-resizer" role="separator" aria-orientation="vertical" aria-label="Resize Size column" tabindex="0" data-resize-column="size"></span></th>
<th scope="colgroup" align="left" colspan="2"{{if or .JupyterEnabled .TensorBoardEnabled}} rowspan="2"{{end}}>Actions</th>
{{if .JupyterEnabled}}<th scope="colgroup" align="center" colspan="2" class="app-group-heading" aria-label="JupyterLab" title="JupyterLab"><img src="/assets/apps/jupyter.svg" alt=""></th>{{end}}
{{if .TensorBoardEnabled}}<th scope="colgroup" align="center" colspan="2" class="app-group-heading" aria-label="TensorBoard" title="TensorBoard"><img src="/assets/apps/tensorboard.png" alt=""></th>{{end}}
{{if .LaTeXEnabled}}<th align="center" colspan="3"{{if or .JupyterEnabled .TensorBoardEnabled}} rowspan="2"{{end}} aria-label="LaTeX tools"><span class="latex-tools-heading"><span class="latex-logo" aria-hidden="true">L<span class="latex-logo-a">A</span>T<span class="latex-logo-e">E</span>X</span><label class="latex-env-mode" title="Choose complete environments or inner commands"><span>Short</span><input id="latex-full-environment" class="latex-env-switch" type="checkbox" role="switch" aria-label="Use full LaTeX figure and table environments" checked><span>Full env</span></label></span></th>{{end}}
</tr>
{{if or .JupyterEnabled .TensorBoardEnabled}}<tr>
{{if .JupyterEnabled}}<th scope="col" class="app-subheading">Start</th><th scope="col" class="app-subheading">Stop</th>{{end}}
{{if .TensorBoardEnabled}}<th scope="col" class="app-subheading">Start</th><th scope="col" class="app-subheading">Stop</th>{{end}}
</tr>{{end}}
</thead>
<tbody>
{{if .HasParent}}<tr><td><div class="name-cell-content" title=".."><a href="{{.ParentURL}}">..</a></div></td><td>&nbsp;</td><td align="right"> - </td><td class="file-action-column"><button type="button" class="copy-path icon-button" data-path="{{.ParentPath}}" aria-label="Copy path" title="Copy path">{{template "copy-path-icon"}}</button></td><td class="file-action-column">&nbsp;</td>{{if .JupyterEnabled}}{{template "app-controls" .ParentJupyter}}{{end}}{{if .TensorBoardEnabled}}{{template "app-controls" .ParentTensorBoard}}{{end}}{{if .LaTeXEnabled}}<td class="action-column">&nbsp;</td><td class="action-column">&nbsp;</td><td class="action-column">&nbsp;</td>{{end}}</tr>{{end}}
<tr><td><div class="name-cell-content" title="."><a href="{{.CurrentURL}}">.</a></div></td><td>&nbsp;</td><td align="right"> - </td><td class="file-action-column"><button type="button" class="copy-path icon-button" data-path="{{.CurrentPath}}" aria-label="Copy path" title="Copy path">{{template "copy-path-icon"}}</button></td><td class="file-action-column">&nbsp;</td>{{if .JupyterEnabled}}{{template "app-controls" .CurrentJupyter}}{{end}}{{if .TensorBoardEnabled}}{{template "app-controls" .CurrentTensorBoard}}{{end}}{{if .LaTeXEnabled}}<td class="action-column">&nbsp;</td><td class="action-column">&nbsp;</td><td class="action-column">&nbsp;</td>{{end}}</tr>
</tbody>
{{if .Entries}}<tbody id="directory-entries">
{{range .Entries}}<tr><td><div class="name-cell-content" title="{{.Name}}{{if .IsDir}}/{{end}}{{if .IsLink}} → {{.LinkTarget}}{{if .Broken}} (broken){{end}}{{end}}"><a href="{{.URL}}">{{.Name}}{{if .IsDir}}/{{end}}</a>{{if .IsLink}}&nbsp;→ {{.LinkTarget}}{{if .Broken}} (broken){{end}}{{end}}</div></td><td><div class="modified-cell-content" title="{{time .ModTime}}">{{time .ModTime}}</div></td><td align="right"><div class="size-cell-content" title="{{if .IsDir}}-{{else}}{{size .Size}}{{end}}">{{if .IsDir}} - {{else}}{{size .Size}}{{end}}</div></td><td class="file-action-column"><button type="button" class="copy-path icon-button" data-path="{{.FullPath}}" aria-label="Copy path" title="Copy path">{{template "copy-path-icon"}}</button></td><td class="file-action-column">{{if not .IsDir}}<button type="button" class="download-file icon-button" data-href="{{.Download}}" aria-label="Download {{.Name}}" title="Download {{.Name}}">{{template "download-icon"}}</button>{{else}}&nbsp;{{end}}</td>{{if $view.JupyterEnabled}}{{template "app-controls" .Jupyter}}{{end}}{{if $view.TensorBoardEnabled}}{{template "app-controls" .TensorBoard}}{{end}}{{if $view.LaTeXEnabled}}<td class="action-column">{{if .LiveURL}}<button type="button" class="open-live icon-button" aria-label="Open live PDF preview in a new tab" title="Open live PDF preview in a new tab" data-href="{{.LiveURL}}"><svg viewBox="0 0 16 16" aria-hidden="true" focusable="false"><path d="M1.5 3C1.5 2.72421 1.72421 2.5 2 2.5H14C14.2758 2.5 14.5 2.72421 14.5 3V11C14.5 11.2758 14.2758 11.5 14 11.5H2C1.72421 11.5 1.5 11.2758 1.5 11V3ZM2 1C0.895786 1 0 1.89579 0 3V11C0 12.1042 0.895786 13 2 13H2.64979L1.35052 15.2499L2.64949 16L4.38194 13H11.6391L13.3715 16L14.6705 15.2499L13.3712 13H14C15.1042 13 16 12.1042 16 11V3C16 1.89579 15.1042 1 14 1H2ZM5.79501 4.64401V9.35601C5.79501 9.85001 6.32901 10.159 6.75701 9.91401L10.88 7.55801C11.312 7.31201 11.312 6.68901 10.88 6.44201L6.75701 4.08601C6.32801 3.84101 5.79501 4.15001 5.79501 4.64401Z" fill="currentColor"></path></svg></button>{{end}}</td><td class="action-column">{{if .TableSnippet.Full}}<button type="button" class="copy-snippet icon-button" aria-label="Copy LaTeX table snippet" title="Copy LaTeX table snippet" data-snippet-full="{{.TableSnippet.Full}}" data-snippet-short="{{.TableSnippet.Short}}"><svg viewBox="0 0 24 24" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="16" rx="1"></rect><path d="M3 9h18M3 14h18M9 4v16M15 4v16"></path></svg></button>{{end}}</td><td class="action-column">{{if .FigureSnippet.Full}}<button type="button" class="copy-snippet icon-button" aria-label="Copy LaTeX figure snippet" title="Copy LaTeX figure snippet" data-snippet-full="{{.FigureSnippet.Full}}" data-snippet-short="{{.FigureSnippet.Short}}"><svg viewBox="0 0 24 24" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="16" rx="1"></rect><circle cx="8" cy="9" r="1.5"></circle><path d="m5 17 4-4 3 3 2-2 5 3"></path></svg></button>{{end}}</td>{{end}}</tr>
{{end}}
</tbody>{{else}}<tbody id="directory-empty"><tr><td colspan="{{.ColumnCount}}">This directory is empty.</td></tr></tbody>{{end}}
</table>
</div>
<script>
(function() {
  var dz = document.getElementById('drop-zone');
  var status = document.getElementById('upload-status');
  var copyStatus = document.getElementById('copy-status');
  var appActionStatus = document.getElementById('app-action-status');
  var csrfToken = {{.CSRFToken}};
  var uploadURL = {{.UploadURL}};
  var importURL = {{.ImportURL}};
  var mkdirURL = {{.MkdirURL}};
  var conflictModal = document.getElementById('conflict-modal');
  var conflictMessage = document.getElementById('conflict-message');
  var conflictApplyAll = document.getElementById('conflict-apply-all');
  var conflictSkip = document.getElementById('conflict-skip');
  var conflictOverwrite = document.getElementById('conflict-overwrite');

  var filterControls = document.getElementById('regex-filter-controls');
  var filterInput = document.getElementById('regex-filter-input');
  var filterIgnoreCase = document.getElementById('regex-ignore-case');
  var filterClear = document.getElementById('regex-filter-clear');
  var filterError = document.getElementById('regex-filter-error');
  var filterStatus = document.getElementById('regex-filter-status');
  var filterHelpToggle = document.getElementById('regex-filter-help-toggle');
  var filterHelp = document.getElementById('regex-filter-help');
  var filterHelpClose = document.getElementById('regex-filter-help-close');
  var filterTotal = parseInt(filterControls.getAttribute('data-entry-count'), 10) || 0;
  var filterStorageKey = 'open-server-regex-filter:' + filterControls.getAttribute('data-directory');
  var filterEntries = null;

  function getFilterEntries() {
    if (filterEntries !== null) return filterEntries;
    filterEntries = [];
    var body = document.getElementById('directory-entries');
    var rows = body ? body.rows : [];
    for (var index = 0; index < rows.length; index++) {
      var link = rows[index].cells[0] && rows[index].cells[0].querySelector('a');
      var name = link ? link.textContent : '';
      if (name.slice(-1) === '/') name = name.slice(0, -1);
      filterEntries.push({row: rows[index], name: name});
    }
    return filterEntries;
  }
  function setFilterError(message) {
    filterError.textContent = message;
    filterInput.setAttribute('aria-invalid', message ? 'true' : 'false');
  }
  function setFilterActive(active) {
    if (active) filterControls.classList.add('filter-active');
    else filterControls.classList.remove('filter-active');
  }
  function saveFilter(pattern, ignoreCase) {
    try {
      window.sessionStorage.setItem(filterStorageKey, JSON.stringify({pattern: pattern, ignoreCase: ignoreCase}));
    } catch (e) {}
  }
  function removeSavedFilter() {
    try { window.sessionStorage.removeItem(filterStorageKey); } catch (e) {}
  }
  function showAllFilterEntries() {
    if (filterEntries !== null) {
      filterEntries.forEach(function(entry) {
        if (entry.row.hidden) entry.row.hidden = false;
      });
    }
    filterStatus.textContent = filterTotal + ' of ' + filterTotal + ' entries';
    setFilterActive(false);
  }
  function clearRegexFilter() {
    filterInput.value = '';
    filterIgnoreCase.checked = false;
    setFilterError('');
    showAllFilterEntries();
    removeSavedFilter();
  }
  function applyRegexFilter(pattern, ignoreCase, persist) {
    if (!pattern) {
      clearRegexFilter();
      return true;
    }
    var expression;
    try {
      expression = new RegExp(pattern, ignoreCase ? 'i' : '');
    } catch (e) {
      setFilterError(e.message || 'Invalid regular expression');
      return false;
    }
    var matched = 0;
    getFilterEntries().forEach(function(entry) {
      var matches = expression.test(entry.name);
      if (entry.row.hidden === matches) entry.row.hidden = !matches;
      if (matches) matched++;
    });
    setFilterError('');
    filterStatus.textContent = matched + ' of ' + filterTotal + ' entries';
    setFilterActive(true);
    if (persist) saveFilter(pattern, ignoreCase);
    return true;
  }
  filterControls.addEventListener('submit', function(e) {
    e.preventDefault();
    applyRegexFilter(filterInput.value, filterIgnoreCase.checked, true);
  });
  filterClear.addEventListener('click', clearRegexFilter);

  function openRegexHelp() {
    filterHelp.hidden = false;
    filterHelpToggle.setAttribute('aria-expanded', 'true');
    filterHelpClose.focus();
  }
  function closeRegexHelp(restoreFocus) {
    var helpHadFocus = filterHelp.contains(document.activeElement);
    filterHelp.hidden = true;
    filterHelpToggle.setAttribute('aria-expanded', 'false');
    if (restoreFocus || helpHadFocus) filterHelpToggle.focus();
  }
  filterHelpToggle.addEventListener('click', function() {
    if (filterHelp.hidden) {
      openRegexHelp();
    } else {
      closeRegexHelp(true);
    }
  });
  filterHelpClose.addEventListener('click', function() { closeRegexHelp(true); });
  document.addEventListener('click', function(e) {
    if (!filterHelp.hidden && e.target !== filterHelpToggle && !filterHelp.contains(e.target)) {
      closeRegexHelp(false);
    }
  });
  document.addEventListener('keydown', function(e) {
    if (e.key === 'Escape' && !filterHelp.hidden) {
      e.preventDefault();
      closeRegexHelp(true);
    }
  });

  try {
    var savedFilterJSON = window.sessionStorage.getItem(filterStorageKey);
    if (savedFilterJSON) {
      var savedFilter = JSON.parse(savedFilterJSON);
      if (typeof savedFilter.pattern === 'string' && typeof savedFilter.ignoreCase === 'boolean') {
        filterInput.value = savedFilter.pattern;
        filterIgnoreCase.checked = savedFilter.ignoreCase;
        if (!applyRegexFilter(savedFilter.pattern, savedFilter.ignoreCase, false)) removeSavedFilter();
      } else {
        removeSavedFilter();
      }
    }
  } catch (e) {
    removeSavedFilter();
  }

  var directoryTable = document.getElementById('directory-list');
  var columnWidthStorageKey = 'open-server-directory-column-widths:v1';
  var columnWidthDefaults = {name: 28, modified: 12.5, size: 6.5};
  var columnWidthLimits = {
    name: {min: 8, max: 60},
    modified: {min: 8, max: 30},
    size: {min: 4.5, max: 20}
  };
  var tableFontSize = parseFloat(window.getComputedStyle(directoryTable).fontSize) || 14;
  var savedColumnWidths = {};
  try {
    var savedColumnWidthsJSON = window.localStorage.getItem(columnWidthStorageKey);
    if (savedColumnWidthsJSON) savedColumnWidths = JSON.parse(savedColumnWidthsJSON) || {};
  } catch (e) {
    savedColumnWidths = {};
  }
  function columnElement(name) {
    return directoryTable.querySelector('col[data-column="' + name + '"]');
  }
  function defaultColumnWidth(name) {
    return columnWidthDefaults[name] * tableFontSize;
  }
  function clampColumnWidth(name, width) {
    var limits = columnWidthLimits[name];
    return Math.max(limits.min * tableFontSize, Math.min(limits.max * tableFontSize, width));
  }
  function updateResizeHandle(name, width) {
    var handle = directoryTable.querySelector('[data-resize-column="' + name + '"]');
    if (!handle) return;
    handle.setAttribute('aria-valuemin', Math.round(columnWidthLimits[name].min * tableFontSize));
    handle.setAttribute('aria-valuemax', Math.round(columnWidthLimits[name].max * tableFontSize));
    handle.setAttribute('aria-valuenow', Math.round(width));
    handle.setAttribute('aria-valuetext', Math.round(width) + ' pixels');
  }
  function setColumnWidth(name, width, persist) {
    var column = columnElement(name);
    if (!column) return;
    width = clampColumnWidth(name, width);
    directoryTable.style.setProperty('--column-' + name + '-width', width + 'px');
    updateResizeHandle(name, width);
    if (persist) {
      savedColumnWidths[name] = Math.round(width);
      try { window.localStorage.setItem(columnWidthStorageKey, JSON.stringify(savedColumnWidths)); } catch (e) {}
    }
  }
  Object.keys(columnWidthDefaults).forEach(function(name) {
    var savedWidth = Number(savedColumnWidths[name]);
    setColumnWidth(name, Number.isFinite(savedWidth) && savedWidth > 0 ? savedWidth : defaultColumnWidth(name), false);
  });
  Array.prototype.forEach.call(document.querySelectorAll('.column-resizer'), function(handle) {
    var name = handle.getAttribute('data-resize-column');
    handle.addEventListener('pointerdown', function(e) {
      if (e.button !== 0) return;
      e.preventDefault();
      e.stopPropagation();
      var startX = e.clientX;
      var startWidth = columnElement(name).getBoundingClientRect().width;
      handle.classList.add('resizing');
      handle.setPointerCapture(e.pointerId);
      function move(event) {
        setColumnWidth(name, startWidth + event.clientX - startX, false);
      }
      function finish(event) {
        setColumnWidth(name, columnElement(name).getBoundingClientRect().width, true);
        handle.classList.remove('resizing');
        handle.removeEventListener('pointermove', move);
        handle.removeEventListener('pointerup', finish);
        handle.removeEventListener('pointercancel', finish);
        if (handle.hasPointerCapture(event.pointerId)) handle.releasePointerCapture(event.pointerId);
      }
      handle.addEventListener('pointermove', move);
      handle.addEventListener('pointerup', finish);
      handle.addEventListener('pointercancel', finish);
    });
    handle.addEventListener('keydown', function(e) {
      if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight' && e.key !== 'Home') return;
      e.preventDefault();
      var width = columnElement(name).getBoundingClientRect().width;
      if (e.key === 'Home') width = defaultColumnWidth(name);
      else width += (e.key === 'ArrowRight' ? 1 : -1) * (e.shiftKey ? 24 : 8);
      setColumnWidth(name, width, true);
    });
    handle.addEventListener('dblclick', function(e) {
      e.preventDefault();
      e.stopPropagation();
      delete savedColumnWidths[name];
      setColumnWidth(name, defaultColumnWidth(name), false);
      try { window.localStorage.setItem(columnWidthStorageKey, JSON.stringify(savedColumnWidths)); } catch (error) {}
    });
  });

  function fallbackCopy(text) {
    var ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.left = '-9999px';
    document.body.appendChild(ta);
    ta.focus();
    ta.select();
    var ok = document.execCommand('copy');
    document.body.removeChild(ta);
    return ok;
  }
  function showCopyResult(button, succeeded) {
    clearTimeout(button.copyResultTimer);
    button.classList.remove('copy-success', 'copy-failure');
    button.classList.add('copy-result', succeeded ? 'copy-success' : 'copy-failure');
    copyStatus.textContent = succeeded ? 'Copied to clipboard.' : 'Copy failed.';
    button.copyResultTimer = setTimeout(function() {
      button.classList.remove('copy-result', 'copy-success', 'copy-failure');
    }, 100);
  }
  function copyText(button, attribute) {
    var text = button.getAttribute(attribute);
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(function() {
        showCopyResult(button, true);
      }).catch(function() {
        showCopyResult(button, fallbackCopy(text));
      });
      return;
    }
    showCopyResult(button, fallbackCopy(text));
  }
  Array.prototype.forEach.call(document.querySelectorAll('.copy-path'), function(button) {
    button.addEventListener('click', function() { copyText(button, 'data-path'); });
  });
  var latexFullEnvironment = document.getElementById('latex-full-environment');
  if (latexFullEnvironment) {
    try {
      var savedLaTeXMode = window.localStorage.getItem('open-server-latex-snippet-mode');
      if (savedLaTeXMode === 'short') latexFullEnvironment.checked = false;
      if (savedLaTeXMode === 'full') latexFullEnvironment.checked = true;
    } catch (e) {}
    latexFullEnvironment.addEventListener('change', function() {
      try {
        window.localStorage.setItem('open-server-latex-snippet-mode', latexFullEnvironment.checked ? 'full' : 'short');
      } catch (e) {}
    });
  }
  Array.prototype.forEach.call(document.querySelectorAll('.copy-snippet'), function(button) {
    button.addEventListener('click', function() {
      copyText(button, latexFullEnvironment && !latexFullEnvironment.checked ? 'data-snippet-short' : 'data-snippet-full');
    });
  });
  Array.prototype.forEach.call(document.querySelectorAll('.download-file'), function(button) {
    button.addEventListener('click', function() {
      window.location.href = button.getAttribute('data-href');
    });
  });
  Array.prototype.forEach.call(document.querySelectorAll('.open-live'), function(button) {
    button.addEventListener('click', function() {
      window.open(button.getAttribute('data-href'), '_blank', 'noopener');
    });
  });
  function setAppElementVisible(element, visible) {
    if (element) element.hidden = !visible;
  }
  function updateAppControl(control, appStatus) {
    if (!control || !appStatus) return;
    var state = appStatus.state || 'stopped';
    var available = !!appStatus.available;
    control.setAttribute('data-state', state);
    setAppElementVisible(control.querySelector('.app-start'), state === 'stopped');
    setAppElementVisible(control.querySelector('.app-starting'), state === 'starting');
    var open = control.querySelector('.app-open');
    if (open && appStatus.open_url) open.setAttribute('href', appStatus.open_url);
    setAppElementVisible(open, state === 'running' && available);
    setAppElementVisible(control.querySelector('.app-unavailable'), state === 'running' && !available);
    setAppElementVisible(control.querySelector('.app-stopping-primary'), state === 'stopping');
    var stopCell = control.nextElementSibling;
    if (stopCell) {
      setAppElementVisible(stopCell.querySelector('.app-stop'), state === 'running');
      setAppElementVisible(stopCell.querySelector('.app-stopping'), state === 'stopping');
    }
  }
  function actionResponse(response) {
    return response.text().then(function(text) {
      var body = {};
      try { body = text ? JSON.parse(text) : {}; } catch (e) {}
      if (!response.ok) {
        var requestError = new Error(body.error || text || response.statusText || 'request failed');
        requestError.code = body.code || '';
        throw requestError;
      }
      return body;
    });
  }
  function setAppActionStatus(message, kind) {
    appActionStatus.textContent = message;
    if (kind) {
      appActionStatus.setAttribute('data-kind', kind);
    } else {
      appActionStatus.removeAttribute('data-kind');
    }
  }
  function refreshAppControl(control) {
    return fetch(control.getAttribute('data-endpoint'), {
      method: 'GET',
      headers: {'Accept': 'application/json'},
      credentials: 'same-origin'
    }).then(actionResponse).then(function(appStatus) {
      updateAppControl(control, appStatus);
      return appStatus;
    });
  }
  function blankApplicationTab() {
    var opened = null;
    try {
      opened = window.open('about:blank', '_blank');
      if (opened) opened.opener = null;
    } catch (e) {}
    return opened;
  }
  function startApplication(control, python) {
    var label = control.getAttribute('data-label');
    var opened = blankApplicationTab();
    updateAppControl(control, {state: 'starting', available: false});
    setAppActionStatus('Starting ' + label + '...');
    var body = 'csrf=' + encodeURIComponent(csrfToken);
    if (python !== null) body += '&python=' + encodeURIComponent(python);
    fetch(control.getAttribute('data-endpoint'), {
      method: 'POST',
      headers: {'Accept': 'application/json', 'Content-Type': 'application/x-www-form-urlencoded'},
      credentials: 'same-origin',
      body: body
    }).then(actionResponse).then(function(appStatus) {
      updateAppControl(control, appStatus);
      setAppActionStatus(label + ' is running.');
      if (opened && appStatus.open_url) {
        opened.location.replace(appStatus.open_url);
      } else if (!opened) {
        setAppActionStatus(label + ' is running. Select Open to view it.');
      }
    }).catch(function(error) {
      if (opened) opened.close();
      refreshAppControl(control).catch(function() {
        updateAppControl(control, {state: 'stopped', available: false});
      });
      if (error.code === 'not_tensorboard_folder') {
        setAppActionStatus('');
        window.alert(error.message);
      } else {
        setAppActionStatus('Could not start ' + label + ': ' + error.message, 'error');
      }
    });
  }
  function stopApplication(control) {
    var label = control.getAttribute('data-label');
    if (control.getAttribute('data-app') === 'jupyter' &&
        !window.confirm('Stop JupyterLab and terminate all of its kernels?')) return;
    updateAppControl(control, {state: 'stopping', available: false});
    setAppActionStatus('Stopping ' + label + '...');
    fetch(control.getAttribute('data-endpoint'), {
      method: 'DELETE',
      headers: {'Accept': 'application/json'},
      credentials: 'same-origin'
    }).then(actionResponse).then(function(appStatus) {
      updateAppControl(control, appStatus);
      setAppActionStatus(label + ' stopped.');
    }).catch(function(error) {
      refreshAppControl(control).catch(function() {});
      setAppActionStatus('Could not stop ' + label + ': ' + error.message, 'error');
    });
  }

  var openJupyterModal = null;
  {{if .JupyterEnabled}}var pendingJupyterControl = null;
  var jupyterModal = document.getElementById('jupyter-modal');
  var jupyterForm = document.getElementById('jupyter-form');
  var jupyterPython = document.getElementById('jupyter-python');
  var jupyterCancel = document.getElementById('jupyter-cancel');
  function closeJupyterModal() {
    jupyterModal.style.display = 'none';
  }
  openJupyterModal = function(control) {
    pendingJupyterControl = control;
    jupyterModal.style.display = 'block';
    jupyterPython.focus();
    jupyterPython.select();
  };
  jupyterCancel.addEventListener('click', function() {
    pendingJupyterControl = null;
    closeJupyterModal();
  });
  jupyterForm.addEventListener('submit', function(e) {
    e.preventDefault();
    var control = pendingJupyterControl;
    pendingJupyterControl = null;
    closeJupyterModal();
    if (control) startApplication(control, jupyterPython.value.trim());
  });
  jupyterModal.addEventListener('click', function(e) {
    if (e.target === jupyterModal) {
      pendingJupyterControl = null;
      closeJupyterModal();
    }
  });
  document.addEventListener('keydown', function(e) {
    if (e.key === 'Escape' && jupyterModal.style.display !== 'none') {
      pendingJupyterControl = null;
      closeJupyterModal();
    }
  });{{end}}
  Array.prototype.forEach.call(document.querySelectorAll('[data-app-control] .app-start'), function(button) {
    button.addEventListener('click', function() {
      var control = button.closest('[data-app-control]');
      if (control.getAttribute('data-app') === 'jupyter' && openJupyterModal) {
        openJupyterModal(control);
      } else {
        startApplication(control, null);
      }
    });
  });
  Array.prototype.forEach.call(document.querySelectorAll('.app-stop'), function(button) {
    button.addEventListener('click', function() {
      var control = button.parentElement.previousElementSibling;
      if (control) stopApplication(control);
    });
  });
  function refreshActiveApplications() {
    if (document.hidden) return;
    Array.prototype.forEach.call(document.querySelectorAll('[data-app-control]'), function(control) {
      if (control.getAttribute('data-state') !== 'stopped') {
        refreshAppControl(control).catch(function() {});
      }
    });
  }
  window.setInterval(refreshActiveApplications, 10000);
  window.addEventListener('focus', refreshActiveApplications);
  document.addEventListener('visibilitychange', function() {
    if (!document.hidden) refreshActiveApplications();
  });
  document.getElementById('btn-toggle-hidden').addEventListener('click', function() {
    window.location.href = this.getAttribute('data-href');
  });

  var folderModal = document.getElementById('folder-modal');
  var folderName = document.getElementById('folder-name');
  var folderCreate = document.getElementById('folder-create');
  var folderCancel = document.getElementById('folder-cancel');
  function closeFolderModal() {
    folderModal.style.display = 'none';
    folderName.onkeydown = null;
  }
  function createFolder() {
    var name = folderName.value.trim();
    if (!name) return;
    var xhr = new XMLHttpRequest();
    xhr.open('POST', mkdirURL, true);
    xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded');
    status.textContent = 'Creating folder ' + name + '...';
    xhr.addEventListener('load', function() {
      if (xhr.status >= 200 && xhr.status < 300) {
        closeFolderModal();
        status.textContent = 'Folder created. Reloading...';
        setTimeout(function() { window.location.reload(); }, 250);
      } else {
        status.textContent = 'Create folder failed: ' + errorMessage(xhr, 'request failed');
      }
    });
    xhr.addEventListener('error', function() {
      status.textContent = 'Create folder failed: network error';
    });
    xhr.send('name=' + encodeURIComponent(name));
  }
  document.getElementById('btn-create-folder').addEventListener('click', function() {
    folderName.value = '';
    folderModal.style.display = 'block';
    folderName.focus();
    folderName.onkeydown = function(e) {
      if (e.key === 'Enter') { e.preventDefault(); createFolder(); }
      if (e.key === 'Escape') closeFolderModal();
    };
  });
  folderCreate.addEventListener('click', createFolder);
  folderCancel.addEventListener('click', closeFolderModal);

  function stop(e) { e.preventDefault(); e.stopPropagation(); }
  ['dragenter', 'dragover'].forEach(function(eventName) {
    dz.addEventListener(eventName, function(e) { stop(e); dz.classList.add('dragging'); });
  });
  ['dragleave', 'drop'].forEach(function(eventName) {
    dz.addEventListener(eventName, function(e) { stop(e); dz.classList.remove('dragging'); });
  });
  document.body.addEventListener('dragover', function(e) { e.preventDefault(); });
  document.body.addEventListener('drop', function(e) { e.preventDefault(); });
  dz.addEventListener('drop', function(e) {
    var files = e.dataTransfer && e.dataTransfer.files;
    if (files && files.length) uploadFiles(files);
  });

  function endpointWithOverwrite(endpoint, overwrite) {
    if (!overwrite) return endpoint;
    return endpoint + (endpoint.indexOf('?') === -1 ? '?' : '&') + 'overwrite=1';
  }
  function responseJSON(xhr) {
    try { return JSON.parse(xhr.responseText); } catch (e) { return {}; }
  }
  function errorMessage(xhr, fallback) {
    var body = responseJSON(xhr);
    return body.error || xhr.statusText || fallback;
  }
  function showConflictDialog(fileName, callback) {
    conflictMessage.textContent = '"' + fileName + '" already exists in this folder.';
    conflictApplyAll.checked = false;
    conflictModal.style.display = 'block';
    conflictSkip.focus();
    function finish(action) {
      var applyAll = conflictApplyAll.checked;
      conflictModal.style.display = 'none';
      conflictSkip.onclick = null;
      conflictOverwrite.onclick = null;
      callback(action, applyAll);
    }
    conflictSkip.onclick = function() { finish('skip'); };
    conflictOverwrite.onclick = function() { finish('overwrite'); };
  }
  function uploadFiles(files) {
    var i = 0;
    var skipped = 0;
    var savedConflictAction = '';
    function finishBatch() {
      if (skipped > 0) {
        status.textContent = 'Uploads complete. Skipped ' + skipped + ' file(s). Reloading...';
      } else {
        status.textContent = 'All uploads complete. Reloading...';
      }
      setTimeout(function() { window.location.reload(); }, 400);
    }
    function resolveConflict(fileName, callback) {
      if (savedConflictAction) {
        callback(savedConflictAction);
        return;
      }
      showConflictDialog(fileName, function(action, applyAll) {
        if (applyAll) savedConflictAction = action;
        callback(action);
      });
    }
    function skipFile(fileName) {
      skipped++;
      status.textContent = 'Skipped ' + fileName + '.';
      next();
    }
    function sendFile(file, overwrite) {
      var form = new FormData();
      form.append('file', file, file.name);
      var xhr = new XMLHttpRequest();
      xhr.open('POST', endpointWithOverwrite(uploadURL, overwrite), true);
      xhr.upload.addEventListener('progress', function(e) {
        if (e.lengthComputable) {
          var percent = (e.loaded / e.total) * 100;
          status.textContent = 'Uploading ' + file.name + ' (' + i + '/' + files.length + '): ' + percent.toFixed(0) + '%';
        }
      });
      xhr.addEventListener('load', function() {
        if (xhr.status >= 200 && xhr.status < 300) {
          next();
        } else if (xhr.status === 409 && !overwrite) {
          status.textContent = 'Conflict: ' + file.name + ' already exists.';
          resolveConflict(file.name, function(action) {
            if (action === 'overwrite') {
              sendFile(file, true);
            } else {
              skipFile(file.name);
            }
          });
        } else {
          status.textContent = 'Upload failed: ' + errorMessage(xhr, 'request failed');
        }
      });
      xhr.addEventListener('error', function() {
        status.textContent = 'Upload failed: network error';
      });
      xhr.send(form);
    }
    function next() {
      if (i >= files.length) {
        finishBatch();
        return;
      }
      var file = files[i++];
      sendFile(file, false);
    }
    next();
  }

  // With JavaScript available, replace the plain upload form with the three-button row.
  var uploadForm = document.getElementById('upload-form');
  var fileInput = document.getElementById('file-input');
  uploadForm.style.display = 'none';
  document.getElementById('upload-buttons').style.display = '';
  document.getElementById('btn-upload-files').addEventListener('click', function() {
    fileInput.click();
  });
  fileInput.addEventListener('change', function() {
    if (fileInput.files && fileInput.files.length) uploadFiles(fileInput.files);
  });

  var pasteModal = document.getElementById('paste-modal');
  var pasteName = document.getElementById('paste-name');
  var pasteOK = document.getElementById('paste-ok');
  var pasteCancel = document.getElementById('paste-cancel');
  function extensionFromType(type) {
    var extensions = {
      'image/png': '.png',
      'image/jpeg': '.jpg',
      'image/gif': '.gif',
      'image/webp': '.webp',
      'image/bmp': '.bmp',
      'image/svg+xml': '.svg',
      'text/plain': '.txt',
      'text/csv': '.csv',
      'application/json': '.json',
      'application/pdf': '.pdf',
      'application/zip': '.zip'
    };
    return extensions[type] || '';
  }
  function defaultPasteName(type) {
    var date = new Date();
    function pad(number) { return (number < 10 ? '0' : '') + number; }
    return 'paste-' + date.getFullYear() + pad(date.getMonth() + 1) + pad(date.getDate()) +
      '-' + pad(date.getHours()) + pad(date.getMinutes()) + pad(date.getSeconds()) + extensionFromType(type);
  }
  function promptPasteName(file) {
    pasteName.value = file.name || defaultPasteName(file.type);
    pasteModal.style.display = 'block';
    pasteName.focus();
    pasteName.select();
    function close() {
      pasteModal.style.display = 'none';
      pasteOK.onclick = null;
      pasteCancel.onclick = null;
      pasteName.onkeydown = null;
    }
    pasteOK.onclick = function() {
      var name = pasteName.value.trim();
      if (!name) return;
      if (name.indexOf('.') === -1) name += extensionFromType(file.type);
      close();
      uploadFiles([new File([file], name, {type: file.type, lastModified: file.lastModified})]);
    };
    pasteCancel.onclick = function() {
      close();
      status.textContent = 'Paste cancelled.';
    };
    pasteName.onkeydown = function(e) {
      if (e.key === 'Enter') { e.preventDefault(); pasteOK.onclick(); }
      if (e.key === 'Escape') pasteCancel.onclick();
    };
  }

  // The button arms a one-shot paste listener. The browser shortcut supplies the file.
  var pasteButton = document.getElementById('btn-paste-file');
  var disarmPaste = null;
  var pasteKeyLabel = /Mac|iP(hone|ad|od)/.test(navigator.platform || '') ? 'Cmd+V' : 'Ctrl+V';
  function armPasteCapture() {
    status.textContent = 'Press ' + pasteKeyLabel + ' to paste a file (Esc or click again to cancel).';
    pasteButton.textContent = 'Waiting for ' + pasteKeyLabel + '… (click to cancel)';
    function cleanup() {
      document.removeEventListener('paste', onPaste);
      document.removeEventListener('keydown', onKey);
      pasteButton.textContent = 'Paste file';
      disarmPaste = null;
    }
    function onPaste(e) {
      cleanup();
      var file = null;
      var files = e.clipboardData && e.clipboardData.files;
      if (files && files.length) file = files[0];
      var items = e.clipboardData && e.clipboardData.items;
      if (!file && items) {
        for (var index = 0; index < items.length; index++) {
          if (items[index].kind === 'file') {
            file = items[index].getAsFile();
            if (file) break;
          }
        }
      }
      if (!file) {
        status.textContent = 'No file in clipboard.';
        return;
      }
      e.preventDefault();
      promptPasteName(file);
    }
    function onKey(e) {
      if (e.key === 'Escape') {
        cleanup();
        status.textContent = 'Paste cancelled.';
      }
    }
    document.addEventListener('paste', onPaste);
    document.addEventListener('keydown', onKey);
    disarmPaste = function() {
      cleanup();
      status.textContent = 'Paste cancelled.';
    };
  }
  pasteButton.addEventListener('click', function() {
    if (disarmPaste) {
      disarmPaste();
      return;
    }
    armPasteCapture();
  });

  var urlRow = document.getElementById('url-row');
  var urlInput = document.getElementById('url-input');
  document.getElementById('btn-from-url').addEventListener('click', function() {
    var hidden = urlRow.style.display === 'none';
    urlRow.style.display = hidden ? '' : 'none';
    if (hidden) urlInput.focus();
  });
  function fetchFromURL(overwrite) {
    var source = urlInput.value.trim();
    if (!source) return;
    var xhr = new XMLHttpRequest();
    xhr.open('POST', endpointWithOverwrite(importURL, overwrite), true);
    xhr.setRequestHeader('Content-Type', 'application/x-www-form-urlencoded');
    status.textContent = 'Fetching URL on this device...';
    xhr.addEventListener('load', function() {
      var body = responseJSON(xhr);
      if (xhr.status >= 200 && xhr.status < 300) {
        status.textContent = 'Saved ' + (body.path || 'file') + '. Reloading...';
        setTimeout(function() { window.location.reload(); }, 400);
      } else if (xhr.status === 409 && !overwrite) {
        var destination = body.path || source;
        var name = destination.split('/').pop() || destination;
        showConflictDialog(name, function(action) {
          if (action === 'overwrite') {
            fetchFromURL(true);
          } else {
            status.textContent = 'Skipped ' + name + '.';
          }
        });
      } else {
        status.textContent = 'URL fetch failed: ' + errorMessage(xhr, 'request failed');
      }
    });
    xhr.addEventListener('error', function() {
      status.textContent = 'URL fetch failed: network error';
    });
    xhr.send('url=' + encodeURIComponent(source));
  }
  document.getElementById('url-fetch').addEventListener('click', function() { fetchFromURL(false); });
  urlInput.addEventListener('keydown', function(e) {
    if (e.key === 'Enter') { e.preventDefault(); fetchFromURL(false); }
  });
})();
</script>
</body>
</html>`
