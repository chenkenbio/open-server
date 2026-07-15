package web

const directoryTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
.latex-logo { font-family: "Times New Roman", Times, serif; font-weight: normal; white-space: nowrap; }
.latex-logo-a { font-size: 0.72em; vertical-align: 0.32em; margin-left: -0.28em; margin-right: -0.12em; }
.latex-logo-e { display: inline-block; font-size: 0.85em; vertical-align: -0.18em; margin-left: -0.08em; margin-right: -0.05em; }
</style>
</head>
<body>
<h1 style="font-family: monospace;">{{.Title}}</h1>
{{if .PlainHTTPWarning}}<div role="alert" style="margin: 0 0 0.8em 0; padding: 0.7em; border: 1px solid #a66; background: #fff3cd; color: #4d3800; font-family: sans-serif;"><strong>Security warning:</strong> This server uses token-protected plain HTTP. The token controls access, but traffic is not encrypted. Use only on a trusted network.</div>{{end}}
<p style="margin: 0 0 0.3em 0; font-family: monospace;">Host: <strong>{{.SSHHost}}</strong></p>
<p style="margin: 0 0 0.3em 0; font-family: monospace;">Root: <strong>{{.RootPath}}</strong></p>
<p style="margin: 0 0 0.6em 0; font-family: monospace;">Path: {{range $i, $crumb := .Breadcrumbs}}{{if $i}} / {{end}}<a href="{{$crumb.URL}}">{{$crumb.Name}}</a>{{end}}</p>
<div id="path-actions" style="margin: 0 0 0.8em 0; font-family: sans-serif;">
<button id="btn-create-folder" type="button">Create folder</button>
<button id="btn-toggle-hidden" type="button" data-href="{{.HiddenToggleURL}}">{{.HiddenToggleLabel}}</button>
<button type="button" class="copy-path" data-path="{{.CurrentPath}}">Copy current path</button>
</div>
<div id="drop-zone" style="padding: 1.2em; border: 2px dashed #999; text-align: center; margin: 0 0 1em 0; font-family: sans-serif;">
<p style="margin: 0 0 0.6em 0;">Drop files here, or use the buttons to upload.</p>
<form id="upload-form" action="{{.UploadURL}}" method="POST" enctype="multipart/form-data" style="margin: 0;">
<input id="file-input" type="file" name="file" multiple>
<button type="submit">Upload</button>
</form>
<div id="upload-buttons" style="display: none;">
<button id="btn-upload-files" type="button">Upload files</button>
<button id="btn-paste-file" type="button">Paste file</button>
<button id="btn-from-url" type="button">From URL</button>
</div>
<div id="url-row" style="display: none; margin: 0.6em 0 0 0;">
<input id="url-input" type="url" placeholder="https://example.com/file.png" style="width: 24em; max-width: 70%;">
<button id="url-fetch" type="button">Fetch</button>
</div>
<p id="upload-status" role="status" aria-live="polite" style="margin: 0.6em 0 0 0; font-family: monospace; min-height: 1.2em;"></p>
</div>
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
<table>
{{$view := .}}<tr><th align="left"{{if .LaTeXEnabled}} rowspan="2"{{end}}><a href="{{.NameSortURL}}">Name{{.NameSortMarker}}</a></th><th align="left"{{if .LaTeXEnabled}} rowspan="2"{{end}}><a href="{{.ModifiedSortURL}}">Last modified{{.ModifiedSortMarker}}</a></th><th align="right"{{if .LaTeXEnabled}} rowspan="2"{{end}}><a href="{{.SizeSortURL}}">Size{{.SizeSortMarker}}</a></th><th align="right"{{if .LaTeXEnabled}} rowspan="2"{{end}}>Path</th><th align="right"{{if .LaTeXEnabled}} rowspan="2"{{end}}>Download</th>{{if .TensorBoardEnabled}}<th align="right"{{if .LaTeXEnabled}} rowspan="2"{{end}}>TensorBoard</th>{{end}}{{if .LaTeXEnabled}}<th align="center" colspan="3" aria-label="LaTeX tools"><span class="latex-logo" aria-hidden="true">L<span class="latex-logo-a">A</span>T<span class="latex-logo-e">E</span>X</span> tools</th>{{end}}</tr>
{{if .LaTeXEnabled}}<tr><th align="right">Table</th><th align="right">Figure</th><th align="right">Preview</th></tr>{{end}}
<tr><th colspan="{{.ColumnCount}}"><hr></th></tr>
{{if .HasParent}}<tr><td><a href="{{.ParentURL}}">Parent Directory</a></td><td>&nbsp;</td><td align="right"> - </td><td align="right">&nbsp;&nbsp;<button type="button" class="copy-path" data-path="{{.ParentPath}}">Copy path</button></td><td align="right">&nbsp;</td>{{if .TensorBoardEnabled}}<td align="right"><form action="{{.ParentTensorBoard}}" method="post" target="_blank" style="margin: 0;"><button type="submit">TensorBoard</button></form></td>{{end}}{{if .LaTeXEnabled}}<td>&nbsp;</td><td>&nbsp;</td><td>&nbsp;</td>{{end}}</tr>
{{end}}{{range .Entries}}<tr><td><a href="{{.URL}}">{{.Name}}{{if .IsDir}}/{{end}}</a>{{if .IsLink}}&nbsp;→ {{.LinkTarget}}{{if .Broken}} (broken){{end}}{{end}}</td><td>&nbsp;&nbsp;{{time .ModTime}}&nbsp;&nbsp;</td><td align="right">{{if .IsDir}} - {{else}}{{size .Size}}{{end}}</td><td align="right">&nbsp;&nbsp;<button type="button" class="copy-path" data-path="{{.FullPath}}">Copy path</button></td><td align="right">{{if .IsDir}}&nbsp;{{else}}&nbsp;&nbsp;<button type="button" class="download-file" data-href="{{.Download}}">Download</button>{{end}}</td>{{if $view.TensorBoardEnabled}}<td align="right">{{if .TensorBoard}}<form action="{{.TensorBoard}}" method="post" target="_blank" style="margin: 0;"><button type="submit">TensorBoard</button></form>{{else}}&nbsp;{{end}}</td>{{end}}{{if $view.LaTeXEnabled}}<td align="right">{{if .TableSnippet}}<button type="button" class="copy-snippet" aria-label="Copy LaTeX table snippet" title="Copy LaTeX table snippet" data-snippet="{{.TableSnippet}}">Table</button>{{else}}&nbsp;{{end}}</td><td align="right">{{if .FigureSnippet}}<button type="button" class="copy-snippet" aria-label="Copy LaTeX figure snippet" title="Copy LaTeX figure snippet" data-snippet="{{.FigureSnippet}}">Figure</button>{{else}}&nbsp;{{end}}</td><td align="right">{{if .LiveURL}}<button type="button" class="open-live" aria-label="Open live PDF preview in a new tab" title="Open live PDF preview in a new tab" data-href="{{.LiveURL}}">Preview</button>{{else}}&nbsp;{{end}}</td>{{end}}</tr>
{{else}}<tr><td colspan="{{.ColumnCount}}">This directory is empty.</td></tr>
{{end}}<tr><th colspan="{{.ColumnCount}}"><hr></th></tr>
</table>
<script>
(function() {
  var dz = document.getElementById('drop-zone');
  var status = document.getElementById('upload-status');
  var uploadURL = {{.UploadURL}};
  var importURL = {{.ImportURL}};
  var mkdirURL = {{.MkdirURL}};
  var conflictModal = document.getElementById('conflict-modal');
  var conflictMessage = document.getElementById('conflict-message');
  var conflictApplyAll = document.getElementById('conflict-apply-all');
  var conflictSkip = document.getElementById('conflict-skip');
  var conflictOverwrite = document.getElementById('conflict-overwrite');

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
  function showCopyResult(button, label) {
    var old = button.textContent;
    button.textContent = label;
    setTimeout(function() { button.textContent = old; }, 900);
  }
  function copyText(button, attribute) {
	var text = button.getAttribute(attribute);
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(function() {
        showCopyResult(button, 'Copied');
      }).catch(function() {
        showCopyResult(button, fallbackCopy(text) ? 'Copied' : 'Copy failed');
      });
      return;
    }
    showCopyResult(button, fallbackCopy(text) ? 'Copied' : 'Copy failed');
  }
  Array.prototype.forEach.call(document.querySelectorAll('.copy-path'), function(button) {
    button.addEventListener('click', function() { copyText(button, 'data-path'); });
  });
  Array.prototype.forEach.call(document.querySelectorAll('.copy-snippet'), function(button) {
    button.addEventListener('click', function() { copyText(button, 'data-snippet'); });
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
    dz.addEventListener(eventName, function(e) { stop(e); dz.style.background = '#eef'; });
  });
  ['dragleave', 'drop'].forEach(function(eventName) {
    dz.addEventListener(eventName, function(e) { stop(e); dz.style.background = ''; });
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
