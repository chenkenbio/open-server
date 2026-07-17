package tensorboard

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestLocalArguments(t *testing.T) {
	arguments := localArguments("/data/run one", "/tensorboard/abc", 60123)
	if got := strings.Join(arguments, " "); got != "--logdir /data/run one --host 127.0.0.1 --port 60123 --path_prefix /tensorboard/abc" {
		t.Fatalf("local arguments = %q", got)
	}
}

func TestRemoteCommandUsesPrivateUnixSocketAndHardenedSSH(t *testing.T) {
	runtimeDirectory := "/tmp/open-server-tb-0123456789abcdef0123456789abcdef"
	remoteSocket := runtimeDirectory + "/tensorboard.sock"
	executable, arguments, err := remoteCommand("ssh-wrapper", "lab", "", 60123, runtimeDirectory, remoteSocket)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(arguments, " ")
	for _, want := range []string{
		"127.0.0.1:60123:" + remoteSocket,
		"lab",
		"ExitOnForwardFailure=yes",
		"SessionType=default",
		"StdinNull=no",
		"trap cleanup EXIT",
		"chmod 600",
		"setsid",
		"command -v tensorboard",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("remote arguments %q do not contain %q", joined, want)
		}
	}
	for _, unwanted := range []string{"-q", "StdinNull=yes", "127.0.0.1:51234", "/data/run", "/tensorboard/abc", "private-token"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("remote arguments %q unexpectedly contain %q", joined, unwanted)
		}
	}
	if executable != "ssh-wrapper" {
		t.Fatalf("executable = %q", executable)
	}
}

func TestPythonInterpreterCommand(t *testing.T) {
	executable, arguments, err := tensorBoardCommand("/opt/venv/bin/python", "/data/run", "/tensorboard/abc", 51234)
	if err != nil {
		t.Fatal(err)
	}
	if executable != "/opt/venv/bin/python" || strings.Join(arguments[:2], " ") != "-m tensorboard.main" {
		t.Fatalf("Python command = %q %q", executable, arguments)
	}
	runtimeDirectory := "/tmp/open-server-tb-0123456789abcdef0123456789abcdef"
	_, remoteArguments, err := remoteCommand(
		"ssh", "lab", "/opt/venv/bin/python", 60123,
		runtimeDirectory, runtimeDirectory+"/tensorboard.sock",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(remoteArguments, " "), `python_executable='/opt/venv/bin/python'`) {
		t.Fatalf("remote Python arguments = %q", remoteArguments)
	}
}

func TestRemoteCommandRejectsOptionLikePython(t *testing.T) {
	runtimeDirectory := "/tmp/open-server-tb-0123456789abcdef0123456789abcdef"
	_, _, err := remoteCommand(
		"ssh", "lab", "-malicious", 60123,
		runtimeDirectory, runtimeDirectory+"/tensorboard.sock",
	)
	if err == nil {
		t.Fatal("option-like Python interpreter was accepted")
	}
}

func TestRemoteConfigurationFrameKeepsSecretsOutOfArguments(t *testing.T) {
	configuration := remoteConfiguration{
		Version:      1,
		LogDirectory: "/data/private run",
		PathPrefix:   "/tensorboard/abc",
		SocketPath:   "/tmp/open-server-tb-0123456789abcdef0123456789abcdef/tensorboard.sock",
		Token:        strings.Repeat("a", 64),
	}
	frame, err := remoteConfigurationFrame(configuration)
	if err != nil {
		t.Fatal(err)
	}
	if got := int(binary.BigEndian.Uint32(frame[:4])); got != len(frame)-4 {
		t.Fatalf("frame length = %d, want %d", got, len(frame)-4)
	}
	var decoded remoteConfiguration
	if err := json.Unmarshal(frame[4:], &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != configuration {
		t.Fatalf("decoded configuration = %#v, want %#v", decoded, configuration)
	}
	if !bytes.Contains(frame, []byte(configuration.Token)) || !bytes.Contains(frame, []byte(configuration.LogDirectory)) {
		t.Fatal("private launch values were not delivered in the stdin frame")
	}
}

func TestRandomBearerTokenHas256Bits(t *testing.T) {
	first, err := randomHex(32)
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomHex(32)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 64 || len(second) != 64 || first == second {
		t.Fatalf("generated bearer tokens have unexpected shape: %q %q", first, second)
	}
}

func TestCaptureWriterRedactsSplitToken(t *testing.T) {
	const token = "0123456789abcdef"
	var output bytes.Buffer
	w := newCaptureWriter(&output, token)
	_, _ = w.Write([]byte("token=01234567"))
	_, _ = w.Write([]byte("89abcdef\n"))
	w.Flush()
	if strings.Contains(output.String(), token) || !strings.Contains(output.String(), "[redacted]") {
		t.Fatalf("captured output did not redact token: %q", output.String())
	}
}

func TestReadinessWriterSuppressesSplitSentinel(t *testing.T) {
	var output bytes.Buffer
	w := newReadinessWriter(&output, remoteReadySentinel)
	cut := len(remoteReadySentinel) / 2
	_, _ = w.Write([]byte("meaningful diagnostic\n" + remoteReadySentinel[:cut]))
	_, _ = w.Write([]byte(remoteReadySentinel[cut:] + "after readiness\n"))
	select {
	case <-w.Ready():
	default:
		t.Fatal("split readiness sentinel was not detected")
	}
	if strings.Contains(output.String(), remoteReadySentinel) {
		t.Fatalf("readiness sentinel leaked into diagnostics: %q", output.String())
	}
	for _, want := range []string{"meaningful diagnostic", "after readiness"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("diagnostics %q do not contain %q", output.String(), want)
		}
	}
}

func TestRetryablePortError(t *testing.T) {
	for _, message := range []string{"bind: Address already in use", "Port 51234 is in use", "remote port forwarding failed", "cannot listen to port: 60123"} {
		if !retryablePortError(errors.New(message)) {
			t.Errorf("%q was not classified as retryable", message)
		}
	}
}
