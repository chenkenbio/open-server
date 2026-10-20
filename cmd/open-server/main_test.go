package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"open-server/internal/filesystem"
	"open-server/internal/jupyter"
	"open-server/internal/sessions"
	"open-server/internal/target"
	"open-server/internal/tensorboard"
)

func TestParseFlags(t *testing.T) {
	t.Parallel()
	defaults, err := parseFlags([]string{"lab:/tmp"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.duration != defaultSessionDuration {
		t.Fatalf("default duration = %s, want %s", defaults.duration, defaultSessionDuration)
	}
	if defaults.fontSize != 14 {
		t.Fatalf("default font size = %d, want 14", defaults.fontSize)
	}
	if defaults.changePort {
		t.Fatal("port changes are allowed by default")
	}

	if defaults.reconnectGrace != defaultReconnectGrace {
		t.Fatalf("default reconnect grace = %s, want %s", defaults.reconnectGrace, defaultReconnectGrace)
	}
	if defaults.noReconnect {
		t.Fatal("reconnect is enabled by default")
	}

	configuration, err := parseFlags([]string{"--port", "8123", "--allow-port-change", "--duration", "90m", "--title", "Files", "lab:/tmp", "--fontsize", "18", "--tensorboard", "--jupyter", "--python-interpreter", "/env/bin/python", "--latex", "--no-reconnect", "--reconnect-grace", "45m"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if configuration.port != 8123 || !configuration.changePort || configuration.duration != 90*time.Minute || configuration.title != "Files" || configuration.fontSize != 18 || len(configuration.targets) != 1 || configuration.targets[0] != "lab:/tmp" || !configuration.tensorBoard || !configuration.jupyter || configuration.python != "/env/bin/python" || !configuration.latex || !configuration.noReconnect || configuration.reconnectGrace != 45*time.Minute {
		t.Fatalf("configuration = %#v", configuration)
	}
	for _, value := range []string{"8", "72"} {
		bounded, err := parseFlags([]string{"lab:/tmp", "--fontsize=" + value}, io.Discard)
		if err != nil {
			t.Errorf("parse fontsize %s: %v", value, err)
			continue
		}
		want, _ := strconv.Atoi(value)
		if bounded.fontSize != want {
			t.Errorf("font size = %d, want %d", bounded.fontSize, want)
		}
	}
	add, err := parseFlags([]string{"--add", "work", "lab:/srv/work"}, io.Discard)
	if err != nil || add.addName != "work" || len(add.targets) != 1 || add.targets[0] != "lab:/srv/work" {
		t.Fatalf("add configuration = %#v, %v", add, err)
	}
	list, err := parseFlags([]string{"-list"}, io.Discard)
	if err != nil || !list.list {
		t.Fatalf("list configuration = %#v, %v", list, err)
	}
	remove, err := parseFlags([]string{"-delete", "work"}, io.Discard)
	if err != nil || remove.delete != "work" {
		t.Fatalf("delete configuration = %#v, %v", remove, err)
	}
	edit, err := parseFlags([]string{"-edit"}, io.Discard)
	if err != nil || !edit.edit {
		t.Fatalf("edit configuration = %#v, %v", edit, err)
	}
	serve, err := parseFlags([]string{"-serve"}, io.Discard)
	if err != nil || !serve.serve || len(serve.targets) != 1 || serve.targets[0] != "." {
		t.Fatalf("serve configuration = %#v, %v", serve, err)
	}
	multiple, err := parseFlags([]string{"session-one", "lab:/two", "./local"}, io.Discard)
	if err != nil || len(multiple.targets) != 3 {
		t.Fatalf("multiple-target configuration = %#v, %v", multiple, err)
	}
	for _, arguments := range [][]string{
		{}, {"--port", "70000", "lab:/tmp"}, {"--duration", "-1s", "lab:/tmp"},
		{"--reconnect-grace", "-1s", "lab:/tmp"},
		{"lab:/tmp", "--fontsize", "7"}, {"lab:/tmp", "--fontsize", "73"}, {"lab:/tmp", "--fontsize", "-1"},
		{"--add", "work"}, {"--add", "work", "lab:/one", "lab:/two"}, {"--list", "lab:/tmp"},
		{"--delete", "work", "lab:/tmp"}, {"--add", "work", "--list", "lab:/tmp"},
		{"--serve", "--local", "."}, {"--list", "--local"}, {"--edit", "lab:/tmp"},
		{"--edit", "--list"}, {"--edit", "--add", "work", "lab:/tmp"}, {"--edit", "--serve"},
		{"--edit", "--local"},
		{"--serve", "--jupyter"},
		{"--serve", "--tensorboard"},
	} {
		if _, err := parseFlags(arguments, io.Discard); err == nil {
			t.Errorf("parseFlags(%q) succeeded, want error", arguments)
		}
	}
}

func TestSavedSessionCommands(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)

	var addOutput bytes.Buffer
	if err := run([]string{"--add", "work", "lab:/srv/work", "--tensorboard", "--jupyter", "-py", "/env/bin/python", "--latex", "--title", "Dashboards", "--fontsize", "18", "--no-reconnect", "--reconnect-grace", "45m"}, &addOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(addOutput.String(), `Saved session "work" -> lab:/srv/work`) {
		t.Fatalf("add output = %q", addOutput.String())
	}
	if !strings.Contains(addOutput.String(), "-port=61000") || !strings.Contains(addOutput.String(), "-fontsize=18") || !strings.Contains(addOutput.String(), "-tensorboard=true") || !strings.Contains(addOutput.String(), "-jupyter=true") || !strings.Contains(addOutput.String(), `-python-interpreter="/env/bin/python"`) || !strings.Contains(addOutput.String(), "-latex=true") || !strings.Contains(addOutput.String(), "-no-reconnect=true") || !strings.Contains(addOutput.String(), "-reconnect-grace=45m0s") {
		t.Fatalf("add output does not show saved options: %q", addOutput.String())
	}

	var listOutput bytes.Buffer
	if err := run([]string{"--list"}, &listOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listOutput.String(), "NAME  TARGET") || !strings.Contains(listOutput.String(), "work  lab:/srv/work") {
		t.Fatalf("list output = %q", listOutput.String())
	}

	resolved, options, err := resolveRemoteTarget("work")
	if err != nil || resolved.Host != "lab" || resolved.Path != "/srv/work" {
		t.Fatalf("resolveRemoteTarget(work) = %#v, %v", resolved, err)
	}
	if options.Port == nil || *options.Port != savedSessionPortStart || options.FontSize == nil || *options.FontSize != 18 || options.TensorBoard == nil || !*options.TensorBoard || options.Jupyter == nil || !*options.Jupyter || options.Python == nil || *options.Python != "/env/bin/python" || options.LaTeX == nil || !*options.LaTeX || options.Title == nil || *options.Title != "Dashboards" || options.NoReconnect == nil || !*options.NoReconnect || options.ReconnectGrace == nil || *options.ReconnectGrace != "45m0s" {
		t.Fatalf("saved options = %#v", options)
	}
	invocation, err := parseFlags([]string{"work", "--tensorboard=false", "--jupyter=false", "--latex=false"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := applySavedSessionOptions(&invocation, options); err != nil {
		t.Fatal(err)
	}
	if invocation.tensorBoard || invocation.jupyter || invocation.python != "/env/bin/python" || invocation.latex || invocation.title != "Dashboards" || invocation.fontSize != 18 || !invocation.noReconnect || invocation.reconnectGrace != 45*time.Minute {
		t.Fatalf("merged saved options = %#v", invocation)
	}
	explicitReconnect, err := parseFlags([]string{"work", "--no-reconnect=false", "--reconnect-grace", "10m"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := applySavedSessionOptions(&explicitReconnect, options); err != nil {
		t.Fatal(err)
	}
	if explicitReconnect.noReconnect || explicitReconnect.reconnectGrace != 10*time.Minute {
		t.Fatalf("explicit reconnect override = %#v", explicitReconnect)
	}
	override, err := parseFlags([]string{"work", "--fontsize", "20"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := applySavedSessionOptions(&override, options); err != nil {
		t.Fatal(err)
	}
	if override.fontSize != 20 {
		t.Fatalf("explicit font-size override = %d, want 20", override.fontSize)
	}
	if _, _, err := resolveRemoteTarget("missing"); err == nil || !strings.Contains(err.Error(), "use --list") {
		t.Fatalf("resolveRemoteTarget(missing) error = %v", err)
	}

	localDirectory := t.TempDir()
	var localAddOutput bytes.Buffer
	if err := run([]string{"--add", "local-paper", "--local", localDirectory, "--latex=false"}, &localAddOutput); err != nil {
		t.Fatal(err)
	}
	localSaved, err := resolveTarget("local-paper", false)
	if err != nil || localSaved.savedName != "local-paper" || localSaved.kind != localTarget || localSaved.local != filepath.ToSlash(localDirectory) {
		t.Fatalf("resolveTarget(local-paper) = %#v, %v", localSaved, err)
	}
	if localSaved.options.LaTeX == nil || *localSaved.options.LaTeX {
		t.Fatalf("saved local options = %#v", localSaved.options)
	}
	if localSaved.options.Port == nil || *localSaved.options.Port != savedSessionPortStart+1 {
		t.Fatalf("second saved session port = %#v, want %d", localSaved.options.Port, savedSessionPortStart+1)
	}
	localPort := *localSaved.options.Port
	if err := run([]string{"--add", "local-paper", "--local", localDirectory, "--title", "Updated paper"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	localSaved, err = resolveTarget("local-paper", false)
	if err != nil || localSaved.options.Port == nil || *localSaved.options.Port != localPort {
		t.Fatalf("updated saved session port = %#v, %v; want preserved %d", localSaved.options.Port, err, localPort)
	}
	if err := run([]string{"--add", "work", "lab:/srv/work"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	_, resetOptions, err := resolveRemoteTarget("work")
	if err != nil {
		t.Fatal(err)
	}
	if resetOptions.FontSize != nil {
		t.Fatalf("font size after re-add without -fontsize = %#v, want nil", resetOptions.FontSize)
	}
	resetInvocation, err := parseFlags([]string{"work"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := applySavedSessionOptions(&resetInvocation, resetOptions); err != nil {
		t.Fatal(err)
	}
	if resetInvocation.fontSize != 14 {
		t.Fatalf("font size after re-add = %d, want default 14", resetInvocation.fontSize)
	}

	var deleteOutput bytes.Buffer
	if err := run([]string{"--delete", "work"}, &deleteOutput); err != nil {
		t.Fatal(err)
	}
	if got, want := deleteOutput.String(), "Deleted session \"work\"\n"; got != want {
		t.Fatalf("delete output = %q, want %q", got, want)
	}
	if err := run([]string{"--delete", "work"}, io.Discard); err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Fatalf("second delete error = %v", err)
	}
}

func TestTensorBoardLauncherIsNilWhenDisabled(t *testing.T) {
	configuration := config{rsh: "ssh"}
	remoteLauncher, closeRemote := remoteTensorBoardLauncher(configuration, "lab", io.Discard)
	defer closeRemote()
	if remoteLauncher != nil {
		t.Fatalf("disabled remote TensorBoard launcher = %#v, want nil", remoteLauncher)
	}
	localLauncher, closeLocal := localTensorBoardLauncher(configuration, io.Discard)
	defer closeLocal()
	if localLauncher != nil {
		t.Fatalf("disabled local TensorBoard launcher = %#v, want nil", localLauncher)
	}

	configuration.tensorBoard = true
	remoteLauncher, closeRemote = remoteTensorBoardLauncher(configuration, "lab", io.Discard)
	defer closeRemote()
	localLauncher, closeLocal = localTensorBoardLauncher(configuration, io.Discard)
	defer closeLocal()
	if remoteLauncher == nil || localLauncher == nil {
		t.Fatal("enabled TensorBoard launcher is nil")
	}
}

func TestJupyterLauncherIsNilWhenDisabled(t *testing.T) {
	configuration := config{rsh: "ssh"}
	remoteLauncher, closeRemote := remoteJupyterLauncher(configuration, "lab", io.Discard)
	defer closeRemote()
	if remoteLauncher != nil {
		t.Fatalf("disabled remote Jupyter launcher = %#v, want nil", remoteLauncher)
	}
	localLauncher, closeLocal := localJupyterLauncher(configuration, io.Discard)
	defer closeLocal()
	if localLauncher != nil {
		t.Fatalf("disabled local Jupyter launcher = %#v, want nil", localLauncher)
	}

	configuration.jupyter = true
	remoteLauncher, closeRemote = remoteJupyterLauncher(configuration, "lab", io.Discard)
	defer closeRemote()
	localLauncher, closeLocal = localJupyterLauncher(configuration, io.Discard)
	defer closeLocal()
	if remoteLauncher == nil || localLauncher == nil {
		t.Fatal("enabled Jupyter launcher is nil")
	}
}

func TestHelpersCannotBeUsedWithServe(t *testing.T) {
	_, err := parseFlags([]string{"--serve", "--jupyter"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "jupyter cannot be used with serve") {
		t.Fatalf("parseFlags serve Jupyter error = %v", err)
	}
	_, err = parseFlags([]string{"--serve", "--tensorboard"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "tensorboard cannot be used with serve") {
		t.Fatalf("parseFlags serve TensorBoard error = %v", err)
	}
}

func TestJupyterFlagAndPythonHelp(t *testing.T) {
	var output bytes.Buffer
	if _, err := parseFlags([]string{"--help"}, &output); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("--help error = %v, want flag.ErrHelp", err)
	}
	help := output.String()
	for _, want := range []string{"-jupyter", "Python interpreter containing TensorBoard and JupyterLab", "-fontsize", "file browser font size in pixels (8-72)", "-allow-port-change", "choose and remember another port", "-no-reconnect", "automatically reconnect after a dropped SSH connection", "-reconnect-grace", "how long remote TensorBoard/Jupyter services survive"} {
		if !strings.Contains(help, want) {
			t.Errorf("help output does not contain %q: %s", want, help)
		}
	}
	if _, err := parseFlags([]string{"--jupyterlab", "lab:/tmp"}, io.Discard); err == nil {
		t.Fatal("unsupported --jupyterlab alias was accepted")
	}
}

func TestLocalServeRoot(t *testing.T) {
	directory := t.TempDir()
	root, initialPath, err := localServeRoot(directory)
	if err != nil || root != filepath.ToSlash(directory) || initialPath != "" {
		t.Fatalf("localServeRoot(%q) = %q, %q, %v", directory, root, initialPath, err)
	}
	fileName := filepath.Join(directory, "paper.pdf")
	if err := os.WriteFile(fileName, []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, initialPath, err = localServeRoot(fileName)
	if err != nil || root != filepath.ToSlash(directory) || initialPath != filepath.ToSlash(fileName) {
		t.Fatalf("localServeRoot(%q) = %q, %q, %v", fileName, root, initialPath, err)
	}
	if got := serveStartURL("http://127.0.0.1:60000/?token=secret-token", initialPath); !strings.Contains(got, "path=") || !strings.Contains(got, "token=secret-token") {
		t.Fatalf("serveStartURL = %q", got)
	}
	if _, _, err := localServeRoot(filepath.Join(directory, "missing")); err == nil {
		t.Fatal("localServeRoot accepted a missing path")
	}
	token, err := generateAccessToken()
	if err != nil || len(token) != 32 {
		t.Fatalf("generated token = %q, %v", token, err)
	}
}

func TestLocalTargetResolutionAndDefaults(t *testing.T) {
	directory := t.TempDir()
	resolved, err := resolveDirectTarget(directory, false)
	if err != nil || resolved.kind != localTarget || resolved.local != filepath.ToSlash(directory) {
		t.Fatalf("resolveDirectTarget(%q) = %#v, %v", directory, resolved, err)
	}
	if _, direct, err := parseDirectTarget("bare-name", false); err != nil || direct {
		t.Fatalf("bare target direct = %v, %v; want saved-session lookup", direct, err)
	}
	if _, err := resolveDirectTarget("missing-bare-name", true); err == nil || !strings.Contains(err.Error(), "access local path") {
		t.Fatalf("forced missing local error = %v", err)
	}

	base := config{explicit: make(map[string]bool)}
	effective, err := effectiveSessionConfig(base, resolvedTarget{kind: localTarget})
	if err != nil || !effective.latex {
		t.Fatalf("local default configuration = %#v, %v", effective, err)
	}
	latex := false
	effective, err = effectiveSessionConfig(base, resolvedTarget{kind: localTarget, options: sessions.Options{LaTeX: &latex}})
	if err != nil || effective.latex {
		t.Fatalf("saved local LaTeX override = %#v, %v", effective, err)
	}
	explicit := config{latex: false, explicit: map[string]bool{"latex": true}}
	effective, err = effectiveSessionConfig(explicit, resolvedTarget{kind: localTarget})
	if err != nil || effective.latex {
		t.Fatalf("explicit local LaTeX override = %#v, %v", effective, err)
	}
}

func TestDefaultPageTitle(t *testing.T) {
	tests := []struct {
		name     string
		resolved resolvedTarget
		root     string
		want     string
	}{
		{
			name:     "saved remote session",
			resolved: resolvedTarget{savedName: "work", kind: remoteTarget, remote: target.Target{Host: "lab"}},
			root:     "/srv/projects/results",
			want:     "work",
		},
		{
			name:     "saved local session",
			resolved: resolvedTarget{savedName: "paper", kind: localTarget},
			root:     "/home/user/paper",
			want:     "paper",
		},
		{
			name:     "direct remote folder",
			resolved: resolvedTarget{kind: remoteTarget, remote: target.Target{Host: "lab"}},
			root:     "/srv/projects/results",
			want:     "lab:.../results/",
		},
		{
			name:     "direct remote root",
			resolved: resolvedTarget{kind: remoteTarget, remote: target.Target{Host: "lab"}},
			root:     "/",
			want:     "lab:/",
		},
		{
			name:     "direct local folder",
			resolved: resolvedTarget{kind: localTarget},
			root:     "/home/user/paper",
			want:     "Local files — /home/user/paper",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := defaultPageTitle(test.resolved, test.root); got != test.want {
				t.Fatalf("defaultPageTitle() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRunMultipleLocalSessionsIsolatesFailureAndAllocatesPorts(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing")
	output := newNotifyWriter("never-ready")
	err := run([]string{"--duration", "100ms", "--no-open", first, missing, second}, output)
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("multi-session error = %v", err)
	}
	matches := regexp.MustCompile(`http://127\.0\.0\.1:(\d+)/`).FindAllStringSubmatch(output.String(), -1)
	if len(matches) != 2 {
		t.Fatalf("session output has %d URLs, want 2: %s", len(matches), output.String())
	}
	firstPort, _ := strconv.Atoi(matches[0][1])
	secondPort, _ := strconv.Atoi(matches[1][1])
	if secondPort <= firstPort {
		t.Fatalf("allocated ports = %d, %d; want increasing", firstPort, secondPort)
	}
}

func TestCloseSessionExitsAllSessions(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	output := newNotifyWriter("Open this local URL [" + second + "]")
	result := make(chan error, 1)
	go func() {
		result <- run([]string{"--duration", "15s", "--no-open", first, second}, output)
	}()

	select {
	case <-output.ready:
	case <-time.After(5 * time.Second):
		t.Fatalf("sessions did not start: %s", output.String())
	}
	addresses := regexp.MustCompile(`http://127\.0\.0\.1:\d+/`).FindAllString(output.String(), -1)
	if len(addresses) != 2 {
		t.Fatalf("session output has %d URLs, want 2: %s", len(addresses), output.String())
	}

	listingResponse, err := http.Get(addresses[0])
	if err != nil {
		t.Fatal(err)
	}
	listingBody, readErr := io.ReadAll(listingResponse.Body)
	_ = listingResponse.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	csrfMatch := regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`).FindSubmatch(listingBody)
	if len(csrfMatch) != 2 {
		t.Fatalf("listing has no close CSRF token: %s", listingBody)
	}
	request, err := http.NewRequest(http.MethodPost, strings.TrimSuffix(addresses[0], "/")+"/close", strings.NewReader("csrf="+string(csrfMatch[1])))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "null")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("close response = %d %s", response.StatusCode, body)
	}
	closePage := string(body)
	for _, want := range []string{"open-server is closing", "Exit reason: user close", "Exit time:", "press Ctrl-C to close it manually"} {
		if !strings.Contains(closePage, want) {
			t.Errorf("close response is missing %q", want)
		}
	}
	requestTimeMatch := regexp.MustCompile(`Exit time: ([^<]+)</p>`).FindStringSubmatch(closePage)
	if len(requestTimeMatch) != 2 {
		t.Fatalf("close response has no exit time: %s", closePage)
	}
	if _, err := time.Parse(time.RFC3339, requestTimeMatch[1]); err != nil {
		t.Fatalf("close response time = %q: %v", requestTimeMatch[1], err)
	}

	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("open-server did not exit after user close")
	}
	terminal := output.String()
	for _, want := range []string{
		"Exit reason: user close",
		"Exit requested by [" + first + "] at " + requestTimeMatch[1],
		"Exit time:",
	} {
		if !strings.Contains(terminal, want) {
			t.Errorf("terminal output is missing %q: %s", want, terminal)
		}
	}
	exitTimeMatch := regexp.MustCompile(`Exit time: (\S+)`).FindStringSubmatch(terminal)
	if len(exitTimeMatch) != 2 {
		t.Fatalf("terminal output has no exit time: %s", terminal)
	}
	if _, err := time.Parse(time.RFC3339, exitTimeMatch[1]); err != nil {
		t.Fatalf("terminal exit time = %q: %v", exitTimeMatch[1], err)
	}

	if response, err := http.Get(addresses[1]); err == nil {
		_ = response.Body.Close()
		t.Fatalf("other session remained reachable with status %d", response.StatusCode)
	}
}

func TestSavedSessionRemembersLastSuccessfulPort(t *testing.T) {
	initialProbe, initialPort, err := listenLoopbackFrom(savedSessionPortStart)
	if err != nil {
		t.Fatal(err)
	}
	if err := initialProbe.Close(); err != nil {
		t.Fatal(err)
	}
	reservedProbe, reservedPort, err := listenLoopbackFrom(initialPort + 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := reservedProbe.Close(); err != nil {
		t.Fatal(err)
	}

	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", configHome)
	root := t.TempDir()
	if err := run([]string{"--add", "remember-port", "--port", strconv.Itoa(initialPort), "--local", root}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--add", "reserved-port", "--port", strconv.Itoa(reservedPort), "--local", t.TempDir()}, io.Discard); err != nil {
		t.Fatal(err)
	}

	launch := func(extraArguments ...string) int {
		t.Helper()
		var output bytes.Buffer
		arguments := []string{"--duration", "100ms", "--no-open"}
		arguments = append(arguments, extraArguments...)
		arguments = append(arguments, "remember-port")
		if err := run(arguments, &output); err != nil {
			t.Fatal(err)
		}
		match := regexp.MustCompile(`http://127\.0\.0\.1:(\d+)/`).FindStringSubmatch(output.String())
		if len(match) != 2 {
			t.Fatalf("session output has no local URL: %s", output.String())
		}
		port, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatal(err)
		}
		return port
	}

	firstPort := launch()
	if firstPort != initialPort {
		t.Fatalf("first saved-session port = %d, want %d", firstPort, initialPort)
	}
	store, err := sessions.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.Resolve("remember-port")
	if err != nil || saved.Options.Port == nil || *saved.Options.Port != firstPort {
		t.Fatalf("saved port after first launch = %#v, %v; want %d", saved.Options.Port, err, firstPort)
	}
	if secondPort := launch(); secondPort != firstPort {
		t.Fatalf("reused saved-session port = %d, want %d", secondPort, firstPort)
	}

	reserved, err := store.Resolve("reserved-port")
	if err != nil || reserved.Options.Port == nil || *reserved.Options.Port != reservedPort {
		t.Fatalf("reserved session = %#v, %v", reserved, err)
	}
	occupied, occupiedPort, err := listenLoopbackFrom(reservedPort + 1)
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	if err := store.UpdatePort("remember-port", occupiedPort); err != nil {
		t.Fatal(err)
	}
	startBlocker, startBlockerErr := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(savedSessionPortStart)))
	if startBlockerErr == nil {
		defer startBlocker.Close()
	} else if !errors.Is(startBlockerErr, syscall.EADDRINUSE) {
		t.Fatal(startBlockerErr)
	}
	if initialPort != savedSessionPortStart {
		initialBlocker, initialBlockerErr := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(initialPort)))
		if initialBlockerErr != nil {
			t.Fatal(initialBlockerErr)
		}
		defer initialBlocker.Close()
	}
	var conflictOutput bytes.Buffer
	err = run([]string{"--duration", "100ms", "--no-open", "remember-port"}, &conflictOutput)
	if err == nil {
		t.Fatal("saved session used another port by default")
	}
	for _, want := range []string{
		strconv.Itoa(occupiedPort),
		"another open-server session may already be running",
		"--allow-port-change",
		"open-server --edit",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("port conflict error does not contain %q: %v", want, err)
		}
	}
	if strings.Contains(conflictOutput.String(), "Open this local URL") {
		t.Fatalf("failed session printed a local URL: %s", conflictOutput.String())
	}
	saved, err = store.Resolve("remember-port")
	if err != nil || saved.Options.Port == nil || *saved.Options.Port != occupiedPort {
		t.Fatalf("saved port after conflict = %#v, %v; want unchanged %d", saved.Options.Port, err, occupiedPort)
	}

	fallbackPort := launch("--allow-port-change")
	if fallbackPort == occupiedPort || fallbackPort == reservedPort || fallbackPort <= reservedPort {
		t.Fatalf("fallback saved-session port = %d with occupied preference %d and reserved port %d; want a later unreserved port", fallbackPort, occupiedPort, reservedPort)
	}
	saved, err = store.Resolve("remember-port")
	if err != nil || saved.Options.Port == nil || *saved.Options.Port != fallbackPort {
		t.Fatalf("saved fallback port = %#v, %v; want %d", saved.Options.Port, err, fallbackPort)
	}
}

func TestPortAllocationStarts(t *testing.T) {
	if automaticPortStart != 60000 {
		t.Fatalf("direct target port start = %d, want 60000", automaticPortStart)
	}
	if savedSessionPortStart != 61000 {
		t.Fatalf("saved-session port start = %d, want 61000", savedSessionPortStart)
	}
}

func TestNormalizeFlagOrderPreservesSeparator(t *testing.T) {
	got := normalizeFlagOrder([]string{"-serve", "--", "-folder"})
	want := []string{"-serve", "--", "-folder"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("normalized arguments = %q, want %q", got, want)
	}
}

func TestHelpIsRecognized(t *testing.T) {
	if _, err := parseFlags([]string{"--help"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("--help error = %v, want flag.ErrHelp", err)
	}
}

func TestVersion(t *testing.T) {
	if version != "0.2.5" {
		t.Fatalf("version = %q, want 0.2.5", version)
	}
	for _, argument := range []string{"-version", "-v"} {
		var output bytes.Buffer
		if err := run([]string{argument}, &output); err != nil {
			t.Fatal(err)
		}
		if got, want := output.String(), "open-server "+version+"\n"; got != want {
			t.Errorf("%s output = %q, want %q", argument, got, want)
		}
	}
}

func TestLocalAppWarningDescribesProxyBoundary(t *testing.T) {
	var output bytes.Buffer
	printLocalAppWarning(&output)
	for _, want := range []string{
		"open-server's unauthenticated 127.0.0.1 listener",
		"enabled TensorBoard or JupyterLab",
		"private socket",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("local app warning = %q, want %q", output.String(), want)
		}
	}
}

func TestListenLoopbackUsesIPv4Only(t *testing.T) {
	listener, err := listenLoopback(0)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("listener address = %#v, want 127.0.0.1", listener.Addr())
	}
}

func TestListenLoopbackSkipsOccupiedPort(t *testing.T) {
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	occupiedPort := occupied.Addr().(*net.TCPAddr).Port
	if occupiedPort == 65535 {
		t.Skip("ephemeral listener used the final TCP port")
	}
	listener, assignedPort, err := listenLoopbackFrom(occupiedPort)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if assignedPort <= occupiedPort {
		t.Fatalf("assigned port = %d, want greater than occupied port %d", assignedPort, occupiedPort)
	}
}

func TestLogicalRemoteRootPreservesSpecifiedSymlinkNamespace(t *testing.T) {
	backend := fixedRealPathBackend{workingDirectory: "/home/user"}
	absolute, err := logicalRemoteRoot(context.Background(), backend, "/data/logical-link/../root-link")
	if err != nil || absolute != "/data/root-link" {
		t.Fatalf("absolute logical root = %q, %v", absolute, err)
	}
	relative, err := logicalRemoteRoot(context.Background(), backend, "projects/../root-link")
	if err != nil || relative != "/home/user/root-link" {
		t.Fatalf("relative logical root = %q, %v", relative, err)
	}
	home, err := logicalRemoteRoot(context.Background(), backend, "~/projects/../root-link")
	if err != nil || home != "/home/user/root-link" {
		t.Fatalf("home-relative logical root = %q, %v", home, err)
	}
	tilde, err := logicalRemoteRoot(context.Background(), backend, "~")
	if err != nil || tilde != "/home/user" {
		t.Fatalf("home logical root = %q, %v", tilde, err)
	}
	userHome, err := logicalRemoteRoot(context.Background(), backend, "~other/projects")
	if err != nil || userHome != "/home/user/~other/projects" {
		t.Fatalf("other-user logical root = %q, %v", userHome, err)
	}
}

func TestRunDurationCleansUp(t *testing.T) {
	var output bytes.Buffer
	started := time.Now()
	err := run([]string{"--rsh", cliWrapper(t, false), "--duration", "500ms", "--no-open", "test-host:."}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 4*time.Second {
		t.Fatal("duration-based cleanup took too long")
	}
	if !strings.Contains(output.String(), "duration expired") {
		t.Fatalf("session output = %q", output.String())
	}
}

func TestRunServeDurationCleansUp(t *testing.T) {
	root := t.TempDir()
	var output bytes.Buffer
	started := time.Now()
	err := run([]string{"-serve", "-address", "127.0.0.1", "-token", "test-token", "-duration", "100ms", root}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 4*time.Second {
		t.Fatal("serve duration-based cleanup took too long")
	}
	for _, want := range []string{"Open this network URL: http://127.0.0.1:", "?token=test-token", "WARNING: serve mode uses plain, unencrypted HTTP", "duration expired"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("serve output %q does not contain %q", output.String(), want)
		}
	}
}

func TestRunSignalCleansUp(t *testing.T) {
	output := newNotifyWriter("Open this local URL")
	result := make(chan error, 1)
	wrapper := cliWrapper(t, false)
	go func() { result <- run([]string{"--rsh", wrapper, "--no-open", "test-host:."}, output) }()
	select {
	case <-output.ready:
	case <-time.After(5 * time.Second):
		t.Fatalf("session did not start: %s", output.String())
	}
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("signal did not shut down the complete session")
	}
}

func TestRunReconnectsAfterUnexpectedSSHExit(t *testing.T) {
	// Reconnect is on by default: an unexpected SSH exit should trigger a
	// reconnect rather than shutting the session down. The wrapper serves
	// briefly then exits, so open-server keeps re-establishing the session
	// until we interrupt it.
	output := newNotifyWriter("Reconnected")
	result := make(chan error, 1)
	wrapper := cliWrapper(t, true)
	go func() { result <- run([]string{"--rsh", wrapper, "--no-open", "test-host:."}, output) }()
	select {
	case <-output.ready:
	case <-time.After(20 * time.Second):
		t.Fatalf("session did not reconnect: %s", output.String())
	}
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("run after reconnect = %v, output = %q", err, output.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interrupt did not shut down the reconnecting session")
	}
	if !strings.Contains(output.String(), "Connection lost [test-host:.].") {
		t.Fatalf("output missing reconnect notice: %q", output.String())
	}
	urls := regexp.MustCompile(`(?m)^Open this local URL \[test-host:\.\]: (http://127\.0\.0\.1:\d+/)$`).FindAllStringSubmatch(output.String(), -1)
	if len(urls) < 2 {
		t.Fatalf("output must print the local URL at startup and after reconnection: %q", output.String())
	}
	for _, match := range urls[1:] {
		if match[1] != urls[0][1] {
			t.Fatalf("reconnect URL = %q, want original URL %q", match[1], urls[0][1])
		}
	}
	if !strings.Contains(output.String(), "Reconnected [test-host:.].\nOpen this local URL [test-host:.]: "+urls[0][1]+"\n") {
		t.Fatalf("successful reconnect must be followed by its session URL: %q", output.String())
	}
}

func TestRunNoReconnectReportsUnexpectedSSHExit(t *testing.T) {
	var output bytes.Buffer
	started := time.Now()
	err := run([]string{"--rsh", cliWrapper(t, true), "--no-open", "--no-reconnect", "test-host:."}, &output)
	if err == nil || !strings.Contains(err.Error(), "SSH connection closed unexpectedly") {
		t.Fatalf("run error = %v, output = %q", err, output.String())
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("unexpected SSH exit was not handled promptly")
	}
}

// reattachingTensorBoardLauncher and reattachingJupyterLauncher are minimal
// stubs satisfying tensorboard.Launcher and jupyter.Launcher (each only a
// single-method Start interface) plus the unexported Reattach(context.Context)
// method that reattachLaunchers looks for through a type assertion.
type reattachingTensorBoardLauncher struct{ calls int }

func (s *reattachingTensorBoardLauncher) Start(string, string) (*tensorboard.Instance, error) {
	return nil, nil
}

func (s *reattachingTensorBoardLauncher) Stop(*tensorboard.Instance) error { return nil }

func (s *reattachingTensorBoardLauncher) Reattach(context.Context) { s.calls++ }

type reattachingJupyterLauncher struct{ calls int }

func (s *reattachingJupyterLauncher) Start(string, string, string) (*jupyter.Instance, error) {
	return nil, nil
}

func (s *reattachingJupyterLauncher) Stop(*jupyter.Instance) error { return nil }

func (s *reattachingJupyterLauncher) Reattach(context.Context) { s.calls++ }

func TestReattachLaunchersCallsReattachOnBoth(t *testing.T) {
	tensorBoard := &reattachingTensorBoardLauncher{}
	jup := &reattachingJupyterLauncher{}
	reattachLaunchers(context.Background(), tensorBoard, jup)
	if tensorBoard.calls != 1 {
		t.Fatalf("tensorboard Reattach calls = %d, want 1", tensorBoard.calls)
	}
	if jup.calls != 1 {
		t.Fatalf("jupyter Reattach calls = %d, want 1", jup.calls)
	}
}

func TestReattachLaunchersIgnoresNilLaunchers(t *testing.T) {
	// Disabled TensorBoard/JupyterLab (-tensorboard/-jupyter omitted) leave
	// these as nil interfaces; the type assertion must fail harmlessly rather
	// than panic.
	reattachLaunchers(context.Background(), nil, nil)
}

func TestCLIHelperProcess(t *testing.T) {
	if os.Getenv("OPEN_SERVER_CLI_HELPER") != "1" {
		return
	}
	server, err := sftp.NewServer(struct {
		io.Reader
		io.WriteCloser
	}{os.Stdin, os.Stdout})
	if err != nil {
		os.Exit(2)
	}
	if os.Getenv("OPEN_SERVER_CLI_EXIT") == "1" {
		go func() { _ = server.Serve() }()
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}
	_ = server.Serve()
	_ = server.Close()
	os.Exit(0)
}

func quoteShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func cliWrapper(t *testing.T, exitEarly bool) string {
	t.Helper()
	wrapper := filepath.Join(t.TempDir(), "ssh-wrapper")
	exitVariable := ""
	if exitEarly {
		exitVariable = " OPEN_SERVER_CLI_EXIT=1"
	}
	// Preserve stdin before starting the helper in the background; POSIX shells
	// may otherwise connect an asynchronous command's stdin to /dev/null.
	contents := "#!/bin/sh\nexec 3<&0\nOPEN_SERVER_CLI_HELPER=1" + exitVariable + " " + quoteShell(os.Args[0]) + " -test.run='^TestCLIHelperProcess$' <&3 3<&- >&1 2>&2 &\nchild=$!\nexec 3<&- >/dev/null 2>/dev/null\nwait \"$child\"\n"
	if err := os.WriteFile(wrapper, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	return wrapper
}

type notifyWriter struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	needle    string
	ready     chan struct{}
	readyOnce sync.Once
}

type fixedRealPathBackend struct {
	filesystem.Backend
	workingDirectory string
}

func (b fixedRealPathBackend) RealPath(context.Context, string) (string, error) {
	return b.workingDirectory, nil
}

func newNotifyWriter(needle string) *notifyWriter {
	return &notifyWriter{needle: needle, ready: make(chan struct{})}
}

func (w *notifyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buffer.Write(p)
	if strings.Contains(w.buffer.String(), w.needle) {
		w.readyOnce.Do(func() { close(w.ready) })
	}
	return n, err
}

func (w *notifyWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}
