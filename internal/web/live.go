package web

import "html/template"

var livePDFPage = template.Must(template.New("live-pdf").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Live PDF — {{.Title}}</title>
<style>
html, body { height: 100%; margin: 0; font-family: sans-serif; }
body { display: flex; flex-direction: column; }
#status { padding: 0.4em 0.7em; background: #f2f2f2; font-family: monospace; }
#pdf { width: 100%; flex: 1; border: 0; }
</style>
</head>
<body>
{{if .PlainHTTP}}<div role="alert" style="padding: 0.4em 0.7em; background: #fff3cd; color: #4d3800; font-family: sans-serif;"><strong>Security warning:</strong> Token-protected plain HTTP; traffic is not encrypted.</div>{{end}}
<div id="status">Live PDF: {{.Title}} — {{if .InitialReady}}watching every 2 seconds{{else}}waiting for PDF compilation{{end}}</div>
<iframe id="pdf" title="{{.Title}}"{{if .InitialReady}} src="{{.PreviewURL}}"{{end}}></iframe>
<script>
(function() {
  var statusURL = {{.StatusURL}};
  var previewURL = {{.PreviewURL}};
  var currentVersion = {{.InitialVersion}};
  var pendingVersion = '';
  var stablePolls = 0;
  var status = document.getElementById('status');
  var frame = document.getElementById('pdf');
  function poll() {
    fetch(statusURL, {cache: 'no-store'}).then(function(response) {
      if (!response.ok) throw new Error('status ' + response.status);
      return response.json();
    }).then(function(data) {
      var version = data.version || '';
      if (!data.ready) {
        pendingVersion = '';
        stablePolls = 0;
        status.textContent = 'Live PDF: {{.Title}} — waiting for PDF compilation';
        return;
      }
      if (!version || version === currentVersion) {
        pendingVersion = '';
        stablePolls = 0;
        return;
      }
      if (version === pendingVersion) {
        stablePolls++;
      } else {
        pendingVersion = version;
        stablePolls = 1;
      }
      if (stablePolls >= 2) {
        currentVersion = version;
        pendingVersion = '';
        stablePolls = 0;
        frame.src = previewURL + (previewURL.indexOf('?') === -1 ? '?' : '&') + 'live=' + encodeURIComponent(version);
        status.textContent = 'Live PDF: {{.Title}} — refreshed ' + new Date().toLocaleTimeString();
      }
    }).catch(function() {
      status.textContent = 'Live PDF: {{.Title}} — waiting for the file';
    });
  }
  setInterval(poll, 2000);
})();
</script>
</body>
</html>`))
