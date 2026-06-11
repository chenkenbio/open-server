package main

const htmlTemplate = `<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<title>{{.PageTitle}}</title>
</head>
<body>
<h1>{{.PageTitle}}</h1>
<table>
<tr><th align="left"><a href="{{.Sort.NameHref}}">Name{{.Sort.NameMarker}}</a></th><th align="left"><a href="{{.Sort.ModifiedHref}}">Last modified{{.Sort.ModifiedMarker}}</a></th><th align="right"><a href="{{.Sort.SizeHref}}">Size{{.Sort.SizeMarker}}</a></th><th align="right">Path</th></tr>
<tr><th colspan="4"><hr></th></tr>
{{if .ParentDir}}<tr><td><a href="{{.ParentDir}}{{.Sort.QuerySuffix}}">Parent Directory</a></td><td>&nbsp;</td><td align="right">  - </td><td align="right">&nbsp;&nbsp;<button type="button" class="copy-path" data-path="{{.ParentPath}}">Copy path</button></td></tr>
{{end}}{{range .Entries}}<tr><td><a href="{{.Href}}{{$.Sort.QuerySuffix}}">{{.Name}}</a></td><td>&nbsp;&nbsp;{{.ModTime}}&nbsp;&nbsp;</td><td align="right">{{.Size}}</td><td align="right">&nbsp;&nbsp;<button type="button" class="copy-path" data-path="{{.FullPath}}">Copy path</button></td></tr>
{{end}}<tr><th colspan="4"><hr></th></tr>
</table>
<hr>
<div id="drop-zone" style="padding: 1.2em; border: 2px dashed #999; text-align: center; margin: 1em 0; font-family: sans-serif;">
<p style="margin: 0 0 0.6em 0;">Drop files here, or use the form to upload.</p>
<form id="upload-form" action="/upload?token={{.Token}}" method="POST" enctype="multipart/form-data" style="margin: 0;">
<input type="file" name="file" multiple>
<button type="submit">Upload</button>
</form>
<p id="upload-status" style="margin: 0.6em 0 0 0; font-family: monospace; min-height: 1.2em;"></p>
</div>
<script>
(function() {
  var dz = document.getElementById('drop-zone');
  var status = document.getElementById('upload-status');
  var token = "{{.Token}}";
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
  function copyPath(button) {
    var text = button.getAttribute('data-path');
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
    button.addEventListener('click', function() { copyPath(button); });
  });
  function stop(e) { e.preventDefault(); e.stopPropagation(); }
  ['dragenter','dragover'].forEach(function(ev) {
    dz.addEventListener(ev, function(e) { stop(e); dz.style.background = '#eef'; });
  });
  ['dragleave','drop'].forEach(function(ev) {
    dz.addEventListener(ev, function(e) { stop(e); dz.style.background = ''; });
  });
  document.body.addEventListener('dragover', function(e) { e.preventDefault(); });
  document.body.addEventListener('drop', function(e) { e.preventDefault(); });
  dz.addEventListener('drop', function(e) {
    var files = e.dataTransfer && e.dataTransfer.files;
    if (files && files.length) uploadFiles(files);
  });
  function uploadFiles(files) {
    var i = 0;
    function next() {
      if (i >= files.length) {
        status.textContent = 'All uploads complete. Reloading...';
        setTimeout(function() { window.location.reload(); }, 400);
        return;
      }
      var f = files[i++];
      var fd = new FormData();
      fd.append('file', f, f.name);
      var xhr = new XMLHttpRequest();
      xhr.open('POST', '/upload?token=' + encodeURIComponent(token), true);
      xhr.upload.addEventListener('progress', function(e) {
        if (e.lengthComputable) {
          var pct = (e.loaded / e.total) * 100;
          status.textContent = 'Uploading ' + f.name + ' (' + i + '/' + files.length + '): ' + pct.toFixed(0) + '%';
        }
      });
      xhr.addEventListener('load', function() {
        if (xhr.status === 200) {
          next();
        } else {
          status.textContent = 'Upload failed: ' + xhr.status + ' ' + xhr.statusText;
        }
      });
      xhr.addEventListener('error', function() {
        status.textContent = 'Upload failed: network error';
      });
      xhr.send(fd);
    }
    next();
  }
})();
</script>
</body>
</html>
`

const forbiddenTemplate = `<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<title>403 Forbidden</title>
</head>
<body>
<h1>403 Forbidden &mdash; {{.Title}}</h1>
<p>{{.Message}}</p>
<p>{{.Detail}}</p>
</body>
</html>
`
