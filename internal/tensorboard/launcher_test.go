package tensorboard

import (
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

func TestRemoteCommandQuotesPathsAndForwardsLoopback(t *testing.T) {
	executable, arguments, err := remoteCommand("ssh-wrapper", "lab", "", "/data/run 'one'", "/tensorboard/abc", 60123, 51234)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(arguments, " ")
	for _, want := range []string{"-q", "127.0.0.1:60123:127.0.0.1:51234", "lab", `'--logdir' '/data/run '\''one'\'''`, `'--path_prefix' '/tensorboard/abc'`, "trap cleanup EXIT", `exec 3<&0`, `cat <&3 >/dev/null`, `kill -TERM "$$"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("remote arguments %q do not contain %q", joined, want)
		}
	}
	if strings.Contains(joined, "StdinNull=yes") {
		t.Fatalf("remote arguments disable the control pipe: %q", joined)
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
	_, remoteArguments, err := remoteCommand("ssh", "lab", "/opt/venv/bin/python", "/data/run", "/tensorboard/abc", 60123, 51234)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(remoteArguments, " "), `'/opt/venv/bin/python' '-m' 'tensorboard.main'`) {
		t.Fatalf("remote Python arguments = %q", remoteArguments)
	}
}

func TestRetryablePortError(t *testing.T) {
	for _, message := range []string{"bind: Address already in use", "Port 51234 is in use", "remote port forwarding failed", "cannot listen to port: 60123"} {
		if !retryablePortError(errors.New(message)) {
			t.Errorf("%q was not classified as retryable", message)
		}
	}
}
