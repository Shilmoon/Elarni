package shadowquic

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/common/errors"
)

// How long the client program may take to answer after it was started, and to end after
// its input was closed.
const (
	programStartTimeout = 10 * time.Second
	programStopTimeout  = 3 * time.Second
)

var programPath atomic.Pointer[string]

// SetProgramPath names the client program. Without it the program is `elarni-shadowquic`
// in the directory of the running executable.
func SetProgramPath(path string) { programPath.Store(&path) }

func clientProgram() (string, error) {
	if path := programPath.Load(); path != nil {
		return *path, nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	name := "elarni-shadowquic"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(filepath.Dir(self), name), nil
}

// program is the client as a child process. It serves until its input ends, so it
// cannot outlive this process.
type program struct {
	command *exec.Cmd
	input   io.WriteCloser
	port    uint16
	ended   chan struct{}
	stop    sync.Once
}

func startProgram(description []byte) (nativeClient, error) {
	path, err := clientProgram()
	if err != nil {
		return nil, err
	}
	command := exec.Command(path)
	hideWindow(command)
	input, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	diagnostics, err := command.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, errors.New("failed to start the ShadowQUIC client program").Base(err)
	}
	p := &program{command: command, input: input, ended: make(chan struct{})}

	type answer struct {
		Port  uint16 `json:"port"`
		Error string `json:"error"`
	}
	answers := make(chan answer, 1)
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		lines := bufio.NewReader(output)
		var first answer
		if line, err := lines.ReadBytes('\n'); err != nil || json.Unmarshal(line, &first) != nil {
			first = answer{Error: "the program gave no answer"}
		}
		answers <- first
		_, _ = io.Copy(io.Discard, lines)
	}()
	go func() {
		defer readers.Done()
		lines := bufio.NewScanner(diagnostics)
		for lines.Scan() {
			logLine(lines.Text())
		}
		_, _ = io.Copy(io.Discard, diagnostics)
	}()
	go func() {
		// Wait closes the pipes, so the readers finish first.
		readers.Wait()
		_ = command.Wait()
		close(p.ended)
	}()

	if _, err := input.Write(append(description, '\n')); err != nil {
		p.Stop()
		return nil, errors.New("failed to configure the ShadowQUIC client program").Base(err)
	}
	select {
	case first := <-answers:
		if first.Error != "" || first.Port == 0 {
			p.Stop()
			return nil, errors.New("the ShadowQUIC client did not start: ", first.Error)
		}
		p.port = first.Port
		return p, nil
	case <-time.After(programStartTimeout):
		p.Stop()
		return nil, errors.New("the ShadowQUIC client program did not answer")
	}
}

func (p *program) Port() uint16 { return p.port }

func (p *program) Running() bool {
	select {
	case <-p.ended:
		return false
	default:
		return true
	}
}

func (p *program) Stop() {
	p.stop.Do(func() {
		_ = p.input.Close()
		select {
		case <-p.ended:
		case <-time.After(programStopTimeout):
			_ = p.command.Process.Kill()
			<-p.ended
		}
	})
}
