package jupyter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestServerArguments(t *testing.T) {
	arguments := serverArguments("/data/run one", "/jupyter/abc/", 60123, "open-server-kernel")
	joined := strings.Join(arguments, " ")
	for _, want := range []string{
		"--ServerApp.ip=127.0.0.1",
		"--ServerApp.port=60123",
		"--ServerApp.port_retries=0",
		"--ServerApp.certfile=",
		"--ServerApp.keyfile=",
		"--ServerApp.ssl_options={}",
		"--ServerApp.base_url=/jupyter/abc/",
		"--ServerApp.root_dir=/data/run one",
		"--ServerApp.open_browser=False",
		"--ServerApp.allow_remote_access=False",
		"--ServerApp.quit_button=False",
		"--ServerApp.contents_manager_class=" + contentsManagerClass,
		"--MappingKernelManager.default_kernel_name=open-server-kernel",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("server arguments %q do not contain %q", joined, want)
		}
	}
}

func TestJupyterCommandUsesConfiguredPython(t *testing.T) {
	executable, arguments, err := jupyterCommand(
		"/opt/my environment/bin/python", "/data/run", "/jupyter/abc/", 51234, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	if executable != "/opt/my environment/bin/python" {
		t.Fatalf("executable = %q", executable)
	}
	if got := strings.Join(arguments[:2], " "); got != "-m jupyterlab" {
		t.Fatalf("Python module arguments = %q", got)
	}

	executable, arguments, err = jupyterCommand("", "/data/run", "/jupyter/abc/", 51234, "")
	if err != nil {
		t.Fatal(err)
	}
	if executable != "jupyter-lab" || len(arguments) == 0 {
		t.Fatalf("PATH command = %q %q", executable, arguments)
	}
}

func TestRemoteCommandUsesPrivateUnixSocketAndHidesLaunchSecrets(t *testing.T) {
	const (
		directory    = "/data/run 'one'"
		kernelPython = "/opt/kernel env/bin/python"
		token        = "private-jupyter-token"
	)
	runDirectory := "/tmp/osj-0123456789abcdef0123456789abcdef"
	socketPath := runDirectory + "/j.sock"
	executable, arguments, err := remoteCommand(
		"ssh-wrapper", "lab", "/opt/server env/bin/python", 60123, runDirectory, socketPath,
	)
	if err != nil {
		t.Fatal(err)
	}
	if executable != "ssh-wrapper" {
		t.Fatalf("executable = %q", executable)
	}
	joined := strings.Join(arguments, " ")
	for _, want := range []string{
		"127.0.0.1:60123:" + socketPath,
		"lab",
		"SessionType=default",
		"StdinNull=no",
		"ServerApp.class_traits",
		"KernelManager.transport=ipc",
		"ServerApp.contents_manager_class",
		"JUPYTER_RUNTIME_DIR",
		"PYTHONPATH",
		"IdentityProvider",
		`setsid`,
		`kill -TERM -"$jupyter_pid"`,
		`cat <&3 >/dev/null`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("remote arguments do not contain %q:\n%s", want, joined)
		}
	}
	for _, secret := range []string{directory, kernelPython, token} {
		if strings.Contains(joined, secret) {
			t.Fatalf("remote SSH arguments contain launch secret %q", secret)
		}
	}
	for _, unwanted := range []string{"-q", "StdinNull=yes"} {
		for _, argument := range arguments {
			if argument == unwanted {
				t.Fatalf("remote SSH arguments contain %q: %#v", unwanted, arguments)
			}
		}
	}
	remoteScript := arguments[len(arguments)-1]
	bootstrapExitGuard := shellQuote(socketPath) + " || exit 1\nruntime_created=1\nJUPYTER_RUNTIME_DIR="
	if !strings.Contains(remoteScript, bootstrapExitGuard) {
		t.Fatalf("remote bootstrap is not fail-closed:\n%s", remoteScript)
	}
	if !strings.Contains(remoteScript, `if [ -n "${runtime_created:-}" ]; then`) {
		t.Fatalf("remote cleanup is not gated on runtime creation:\n%s", remoteScript)
	}
	serviceCommand := `setsid "$server_python" -m jupyterlab ` +
		`"--ServerApp.sock=$socket_path" "--ServerApp.sock_mode=0600" ` +
		`"--ServerApp.certfile=" "--ServerApp.keyfile=" "--ServerApp.ssl_options={}" ` +
		`"--ServerApp.contents_manager_class=` + contentsManagerClass + `" ` +
		`"--KernelManager.transport=ipc"`
	if !strings.Contains(remoteScript, serviceCommand) {
		t.Fatalf("socket and IPC settings are not command-line overrides:\n%s", remoteScript)
	}
	if strings.Contains(joined, "127.0.0.1:51234") {
		t.Fatalf("remote arguments retain a TCP backend: %q", joined)
	}

	configuration := remoteLaunchConfig{
		ContentsManager: contentsManagerPython,
		Directory:       directory,
		KernelName:      "open-server-kernel",
		KernelPython:    kernelPython,
		PathPrefix:      "/jupyter/abc/",
		RunDirectory:    runDirectory,
		SocketPath:      socketPath,
		Token:           token,
	}
	frame, err := remoteConfigFrame(configuration)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.SplitN(strings.TrimSuffix(string(frame), "\n"), " ", 3)
	if len(fields) != 3 || fields[0] != "OSJ1" {
		t.Fatalf("invalid frame header: %q", frame)
	}
	payload, err := base64.StdEncoding.DecodeString(fields[2])
	if err != nil {
		t.Fatal(err)
	}
	wantLength, err := strconv.Atoi(fields[1])
	if err != nil || wantLength != len(payload) {
		t.Fatalf("frame length = %q, payload length = %d", fields[1], len(payload))
	}
	var decoded remoteLaunchConfig
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != configuration {
		t.Fatalf("decoded frame = %#v, want %#v", decoded, configuration)
	}
}

func TestValidateLaunch(t *testing.T) {
	for _, test := range []struct {
		directory, python, prefix string
	}{
		{"", "", "/jupyter/abc"},
		{"/data", "bad\npython", "/jupyter/abc"},
		{"/data", "", "/tensorboard/abc"},
		{"/data", "", "/jupyter/a/b"},
		{"/data", "", "/jupyter/a?b"},
	} {
		if err := validateLaunch(test.directory, test.python, test.prefix); err == nil {
			t.Errorf("validateLaunch(%q, %q, %q) unexpectedly succeeded", test.directory, test.python, test.prefix)
		}
	}
	if err := validateLaunch("/data", "/opt/my env/bin/python", "/jupyter/abc-_1/"); err != nil {
		t.Fatalf("valid launch rejected: %v", err)
	}
}

func TestCaptureWriterRedactsSplitToken(t *testing.T) {
	const token = "0123456789abcdef"
	var output bytes.Buffer
	w := newCaptureWriter(&output, token)
	_, _ = w.Write([]byte("url?token=01234567"))
	_, _ = w.Write([]byte("89abcdef\n"))
	w.Flush()
	if strings.Contains(output.String(), token) {
		t.Fatalf("captured output leaked token: %q", output.String())
	}
	if !strings.Contains(output.String(), "[redacted]") {
		t.Fatalf("captured output was not visibly redacted: %q", output.String())
	}
}

func TestRemoteSocketDiagnosticFilterSuppressesOnlyMissingSocketNoise(t *testing.T) {
	var output bytes.Buffer
	filter := newRemoteSocketDiagnosticFilter(&output)
	chunks := []string{
		"channel 12: open failed: connect ",
		"failed: open failed\n",
		"channel 13: open failed: administratively prohibited: open failed\n",
		"channel unknown: open failed: connect failed: open failed\n",
		"remote command failed without a newline",
	}
	for _, chunk := range chunks {
		if written, err := filter.Write([]byte(chunk)); err != nil || written != len(chunk) {
			t.Fatalf("filter.Write(%q) = (%d, %v)", chunk, written, err)
		}
	}
	filter.Flush()
	got := output.String()
	if strings.Contains(got, "channel 12:") {
		t.Fatalf("transient missing-socket diagnostic was retained: %q", got)
	}
	for _, want := range []string{
		"administratively prohibited",
		"channel unknown: open failed: connect failed: open failed",
		"remote command failed without a newline",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("meaningful SSH diagnostic %q was suppressed: %q", want, got)
		}
	}
}

func TestShutdownKernelsUsesAuthenticatedAPI(t *testing.T) {
	const token = "private-token"
	var mu sync.Mutex
	kernelRunning := true
	deleteCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "token "+token {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/jupyter/abc/api/kernels":
			mu.Lock()
			defer mu.Unlock()
			writer.Header().Set("Content-Type", "application/json")
			if kernelRunning {
				_, _ = writer.Write([]byte(`[{"id":"kernel one"}]`))
			} else {
				_, _ = writer.Write([]byte(`[]`))
			}
		case request.Method == http.MethodDelete && request.URL.EscapedPath() == "/jupyter/abc/api/kernels/kernel%20one":
			mu.Lock()
			kernelRunning = false
			deleteCount++
			mu.Unlock()
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	shutdownKernels(target, "/jupyter/abc/", token)
	mu.Lock()
	defer mu.Unlock()
	if deleteCount != 1 || kernelRunning {
		t.Fatalf("kernel shutdown state: deleteCount=%d running=%v", deleteCount, kernelRunning)
	}
}

func TestListKernelsRejectsUnauthorizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	if _, err := listKernels(context.Background(), target, "/jupyter/abc/", "wrong"); err == nil {
		t.Fatal("unauthorized kernel response was accepted")
	}
}
