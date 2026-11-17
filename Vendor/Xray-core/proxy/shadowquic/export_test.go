package shadowquic

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// Artifact returns a file built from native/elarni-shadowquic: the tests need the client
// program, the client library and the test server. `cargo build -p elarni-shadowquic --lib
// --bins --examples` puts them in target/debug; ELARNI_SHADOWQUIC_ARTIFACTS names
// another directory.
func Artifact(t testing.TB, name string) string {
	t.Helper()
	directory := os.Getenv("ELARNI_SHADOWQUIC_ARTIFACTS")
	if directory == "" {
		_, file, _, _ := runtime.Caller(0)
		directory = filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "target", "debug")
	}
	path := filepath.Join(directory, filepath.FromSlash(name))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s is missing; run `cargo build -p elarni-shadowquic --lib --bins --examples`: %v", path, err)
	}
	return path
}

func executable(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// UseClientProgram points the outbound at the client program the tests built.
func UseClientProgram(t testing.TB) {
	SetProgramPath(Artifact(t, executable("elarni-shadowquic")))
}

// onlyProgram makes the outbound start the client program even where a client is
// linked into the test.
func onlyProgram(t testing.TB) {
	UseClientProgram(t)
	startClient = startProgram
	t.Cleanup(func() { startClient = startNative })
}

// StartTestServer runs a ShadowQUIC server on 127.0.0.1 (user "user", password "pass",
// server name "localhost") until the test ends and returns its port.
func StartTestServer(t testing.TB) uint16 {
	t.Helper()
	command := exec.Command(Artifact(t, "examples/"+executable("shadowquic-test-server")))
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		input.Close()
		command.Wait()
	})
	var answer struct {
		Port uint16 `json:"port"`
	}
	line, err := bufio.NewReader(output).ReadBytes('\n')
	if err != nil || json.Unmarshal(line, &answer) != nil || answer.Port == 0 {
		t.Fatalf("the test server gave no port: %q %v", line, err)
	}
	return answer.Port
}
