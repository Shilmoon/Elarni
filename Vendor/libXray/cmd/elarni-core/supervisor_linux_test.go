//go:build linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/xtls/libxray/share"
	"github.com/xtls/libxray/xray"
)

// The root supervisor must keep running the binary pkexec approved even when
// a same-user process replaces the file at its path (portable bundle in $HOME).
func TestPinnedExecutableSurvivesReplacement(t *testing.T) {
	run := "-test.run=^TestPinnedExecutableSurvivesReplacement$"
	switch os.Getenv("ELARNI_PIN_TEST") {
	case "print":
		fmt.Print("original")
		os.Exit(0)
	case "pin":
		file, path, err := pinnedLinuxExecutable()
		if err != nil {
			os.Exit(2)
		}
		defer file.Close()
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		command := exec.Command(path, run)
		command.Env = append(os.Environ(), "ELARNI_PIN_TEST=print")
		output, _ := command.Output()
		os.Stdout.Write(output)
		os.Exit(0)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "elarni-core")
	if err := os.WriteFile(path, binary, 0o755); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(path, run)
	child.Env = append(os.Environ(), "ELARNI_PIN_TEST=pin")
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	child.Stdout = &output
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	replacement := path + ".new"
	if err := os.WriteFile(replacement, []byte("#!/bin/sh\necho replaced\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	input.Write([]byte("\n"))
	input.Close()
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	if output.String() != "original" {
		t.Fatalf("supervisor child ran %q, want the pinned original", output.String())
	}
}

// The root service and a desktop user's Connect must not run two tunnels: each
// tunnel's traffic matches the other's policy rule.
func TestLinuxVPNLockAdmitsOneTunnelPerMachine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elarni-vpn.lock")
	first, err := acquireLinuxVPNLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := acquireLinuxVPNLock(path); err == nil || err.Error() != "vpn_guard_busy" {
		if second != nil {
			second.Close()
		}
		t.Fatalf("a second tunnel was admitted: %v", err)
	}
	first.Close()
	again, err := acquireLinuxVPNLock(path)
	if err != nil {
		t.Fatal("the lock outlived its tunnel", err)
	}
	again.Close()
}

// A worker for the supervisor and broker tests: this test binary in fixture mode.
func TestLinuxWorkerFixture(t *testing.T) {
	mode := os.Getenv("ELARNI_LINUX_WORKER_FIXTURE")
	if mode == "" {
		return
	}
	input := bufio.NewReader(os.Stdin)
	command, err := linuxReadCommand(input)
	if err != nil || command.Action != "start" || mode == "fail" {
		os.Exit(1)
	}
	if linuxWriteFrame(os.Stdout, linuxReply{Ready: true, Adapter: command.Network.Name}) != nil {
		os.Exit(1)
	}
	if mode == "events" {
		_ = linuxWriteFrame(os.Stdout, linuxReply{Logs: []xray.LogEntry{{Seq: 1, Level: "info", Text: "fixture"}}})
		_ = linuxWriteFrame(os.Stdout, linuxReply{Lost: true})
	}
	// Until the owner closes the input. A reload is answered as the worker does: still running,
	// with a code when the tunnel cannot take it (here, a request that asks for 1 MB).
	for {
		next, err := linuxReadCommand(input)
		if err != nil || next.Action != "reload" {
			break
		}
		reply := linuxReply{Ready: true}
		if next.Request.MemoryLimitMB == 1 {
			reply.Error = "vpn_reload_restart"
		}
		// As the supervisor answers when no worker runs.
		if next.Request.MemoryLimitMB == 2 {
			reply = linuxReply{Error: "vpn_reload_restart"}
		}
		if linuxWriteFrame(os.Stdout, reply) != nil {
			os.Exit(1)
		}
	}
	os.Exit(0)
}

func linuxWorkerFixture(mode string) *exec.Cmd {
	command := exec.Command(os.Args[0], "-test.run=^TestLinuxWorkerFixture$")
	command.Env = append(os.Environ(), "ELARNI_LINUX_WORKER_FIXTURE="+mode)
	return command
}

// Regression: a start failure or a worker that ended by itself (its core stopped)
// ended the root supervisor, so the next connect asked for polkit consent again.
func TestLinuxSupervisorOutlivesFailedAndLostTunnels(t *testing.T) {
	for _, block := range []bool{false, true} {
		t.Run(fmt.Sprint("blockWithoutVPN=", block), func(t *testing.T) {
			lock := filepath.Join(t.TempDir(), "vpn.lock")
			var calls []string
			var mu sync.Mutex
			call := func(format string, values ...any) {
				mu.Lock()
				defer mu.Unlock()
				calls = append(calls, fmt.Sprintf(format, values...))
			}
			took := func() []string {
				mu.Lock()
				defer mu.Unlock()
				taken := calls
				calls = nil
				return taken
			}
			modes := []string{"fail", "run", "run", "run"}
			workers := make(chan *linuxChild, len(modes))
			reserved := 0
			host := linuxTunnelHost{
				lock: func() (*os.File, error) { return acquireLinuxVPNLock(lock) },
				reserve: func() (linuxNetwork, error) {
					reserved++
					network := linuxNetwork{Name: fmt.Sprintf("rv%012d", reserved), Table: 12000 + reserved, Priority: 12000 + reserved, Mark: 0x52560000 | uint32(12000+reserved), DNSPort: 5353}
					call("reserve %s", network.Name)
					return network, nil
				},
				cleanup: func(network linuxNetwork) error { call("cleanup %s", network.Name); return nil },
				forget:  func(network linuxNetwork) { call("forget %s", network.Name) },
				worker: func(events func(linuxReply)) (*linuxChild, error) {
					child, err := startLinuxChild(linuxWorkerFixture(modes[0]), events)
					modes = modes[1:]
					workers <- child
					return child, err
				},
			}
			commands, closed, replies := make(chan linuxCommand), make(chan struct{}), make(chan linuxReply, 4)
			done := make(chan int, 1)
			go func() {
				done <- superviseLinuxTunnels(context.Background(), commands, closed, func(reply linuxReply) error { replies <- reply; return nil }, os.Getuid(), host)
			}()
			reply := func() linuxReply {
				t.Helper()
				select {
				case reply := <-replies:
					return reply
				case <-time.After(10 * time.Second):
					t.Fatal("the supervisor did not answer")
					return linuxReply{}
				}
			}
			order := func(command linuxCommand) {
				t.Helper()
				select {
				case commands <- command:
				case code := <-done:
					t.Fatalf("the supervisor exited with %d before %s", code, command.Action)
				}
			}
			request := vpnRequest{Config: linuxConfigFixture}
			if block {
				request.RoutePolicy = &share.WindowsRoutePolicy{IncludedRoutes: []string{"0.0.0.0/0"}, BlockWithoutVPN: true}
			}
			start := linuxCommand{Action: "start", Request: request}
			stop, release := linuxCommand{Action: "stop"}, linuxCommand{Action: "stop", Release: true}
			want := func(stage string, expected ...string) {
				t.Helper()
				if got := took(); fmt.Sprint(got) != fmt.Sprint(expected) {
					t.Fatalf("%s: host calls %v, want %v", stage, got, expected)
				}
			}
			locked := func() bool {
				file, err := acquireLinuxVPNLock(lock)
				if err == nil {
					file.Close()
				}
				return err != nil
			}

			order(start)
			if got := reply(); got.Ready || got.Error != "vpn_start_failed" {
				t.Fatalf("failed start answered %+v", got)
			}
			if block {
				// What the failed start installed stays as the kill switch.
				want("failed start", "reserve rv000000000001")
			} else {
				want("failed start", "reserve rv000000000001", "cleanup rv000000000001", "cleanup rv000000000001", "forget rv000000000001")
			}
			if locked() != block {
				t.Fatal("the tunnel lock does not follow the kept network")
			}
			<-workers

			order(start)
			if got := reply(); !got.Ready || got.Adapter != "rv000000000002" {
				t.Fatalf("the next start answered %+v", got)
			}
			if block {
				// The kept kill switch goes only once the next tunnel has its own.
				want("second start", "reserve rv000000000002", "cleanup rv000000000001", "forget rv000000000001")
			} else {
				want("second start", "reserve rv000000000002")
			}
			worker := <-workers
			_ = worker.command.Process.Kill() // its core stopped
			if got := reply(); !got.Lost {
				t.Fatalf("a lost tunnel was reported as %+v", got)
			}
			if block {
				want("lost tunnel")
			} else {
				want("lost tunnel", "cleanup rv000000000002", "cleanup rv000000000002", "forget rv000000000002")
			}
			if locked() != block {
				t.Fatal("the tunnel lock does not follow the kept network")
			}

			// The app's cleanup stop after a loss keeps the kill switch.
			order(stop)
			if got := reply(); !got.Ready {
				t.Fatalf("idle stop answered %+v", got)
			}
			want("idle stop")

			order(start)
			if got := reply(); !got.Ready || got.Adapter != "rv000000000003" {
				t.Fatalf("reconnect answered %+v", got)
			}
			if block {
				want("reconnect", "reserve rv000000000003", "cleanup rv000000000002", "forget rv000000000002")
			} else {
				want("reconnect", "reserve rv000000000003")
			}
			order(stop)
			if got := reply(); !got.Ready {
				t.Fatalf("stop answered %+v", got)
			}
			want("stop", "cleanup rv000000000003", "cleanup rv000000000003", "forget rv000000000003")
			if locked() {
				t.Fatal("a stopped tunnel kept the lock")
			}

			// Regression: the user's Disconnect after a loss kept the kill switch too, and the
			// device offline with Elarni disconnected. A stop that releases lifts it.
			<-workers // the reconnect's
			order(start)
			if got := reply(); !got.Ready || got.Adapter != "rv000000000004" {
				t.Fatalf("the last start answered %+v", got)
			}
			want("last start", "reserve rv000000000004")
			worker = <-workers
			_ = worker.command.Process.Kill()
			if got := reply(); !got.Lost {
				t.Fatalf("a lost tunnel was reported as %+v", got)
			}
			if block {
				want("second lost tunnel")
			} else {
				want("second lost tunnel", "cleanup rv000000000004", "cleanup rv000000000004", "forget rv000000000004")
			}
			order(release)
			if got := reply(); !got.Ready {
				t.Fatalf("releasing stop answered %+v", got)
			}
			if block {
				want("releasing stop", "cleanup rv000000000004", "forget rv000000000004")
			} else {
				want("releasing stop")
			}
			if locked() {
				t.Fatal("a releasing stop kept the lock")
			}
			close(closed)
			select {
			case code := <-done:
				if code != 0 {
					t.Fatalf("exit %d", code)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the supervisor outlived its owner")
			}
		})
	}
}

// Regression: with Block traffic without VPN, each restart the app made (another server,
// a settings change, failover) removed the kill switch with the old tunnel, and a start
// removed a kept one before the new tunnel was up: traffic left outside for 60-120 ms
// on a change and about a second after a lost tunnel.
// A reload goes to the running tunnel's worker and is answered as a running tunnel, taken or
// refused; without a tunnel it is refused, and the supervisor stays for the next start.
// A reload that finds no tunnel (the worker ended a moment before) is refused, not lost: the
// broker used to take the refusal for a missing answer and stop its root supervisor, so the
// next connect asked for polkit consent again.
func TestLinuxBrokerKeepsItsSupervisorWhenNoTunnelTakesAReload(t *testing.T) {
	child, err := startLinuxChild(linuxWorkerFixture("run"), func(linuxReply) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := child.request(linuxCommand{Action: "start", Network: linuxNetwork{Name: "rv000000000002"}}, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	child.up.Store(true)
	linuxBroker.Lock()
	before, closed := linuxBroker.child, linuxBroker.closed
	linuxBroker.child, linuxBroker.closed = child, false
	linuxBroker.Unlock()
	t.Cleanup(func() {
		_ = child.stop()
		linuxBroker.Lock()
		linuxBroker.child, linuxBroker.closed = before, closed
		linuxBroker.Unlock()
	})
	payload, err := json.Marshal(vpnRequest{Config: linuxConfigFixture, MemoryLimitMB: 2, RoutePolicy: &share.WindowsRoutePolicy{IncludedRoutes: []string{"0.0.0.0/0"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reloadVPN(payload); err == nil || err.Error() != "vpn_reload_restart" {
		t.Fatalf("the reload answered %v", err)
	}
	linuxBroker.Lock()
	kept := linuxBroker.child == child
	linuxBroker.Unlock()
	select {
	case <-child.done:
		kept = false
	default:
	}
	if !kept {
		t.Fatal("a refused reload stopped the supervisor")
	}
}

func TestLinuxSupervisorPassesAReloadToItsWorker(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "vpn.lock")
	host := linuxTunnelHost{
		lock: func() (*os.File, error) { return acquireLinuxVPNLock(lock) },
		reserve: func() (linuxNetwork, error) {
			return linuxNetwork{Name: "rv000000000001", Table: 12001, Priority: 12001, Mark: 0x52560000 | 12001, DNSPort: 5353}, nil
		},
		cleanup: func(linuxNetwork) error { return nil },
		forget:  func(linuxNetwork) {},
		worker: func(events func(linuxReply)) (*linuxChild, error) {
			return startLinuxChild(linuxWorkerFixture("run"), events)
		},
	}
	commands, closed, replies := make(chan linuxCommand), make(chan struct{}), make(chan linuxReply, 4)
	done := make(chan int, 1)
	go func() {
		done <- superviseLinuxTunnels(context.Background(), commands, closed, func(reply linuxReply) error { replies <- reply; return nil }, os.Getuid(), host)
	}()
	ask := func(stage string, command linuxCommand) linuxReply {
		t.Helper()
		select {
		case commands <- command:
		case code := <-done:
			t.Fatalf("%s: the supervisor exited with %d", stage, code)
		}
		select {
		case reply := <-replies:
			return reply
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: the supervisor did not answer", stage)
		}
		return linuxReply{}
	}
	request := vpnRequest{Config: linuxConfigFixture, RoutePolicy: &share.WindowsRoutePolicy{IncludedRoutes: []string{"0.0.0.0/0"}}}
	if reply := ask("reload without a tunnel", linuxCommand{Action: "reload", Request: request}); reply.Ready || reply.Error != "vpn_reload_restart" {
		t.Fatalf("reload without a tunnel answered %+v", reply)
	}
	if reply := ask("start", linuxCommand{Action: "start", Request: request}); !reply.Ready {
		t.Fatalf("start answered %+v", reply)
	}
	if reply := ask("reload", linuxCommand{Action: "reload", Request: request}); !reply.Ready || reply.Adapter != "rv000000000001" || reply.Error != "" {
		t.Fatalf("reload answered %+v", reply)
	}
	refused := request
	refused.MemoryLimitMB = 1
	if reply := ask("refused reload", linuxCommand{Action: "reload", Request: refused}); !reply.Ready || reply.Adapter != "rv000000000001" || reply.Error != "vpn_reload_restart" {
		t.Fatalf("refused reload answered %+v", reply)
	}
	// The tunnel runs on: it takes another reload and a stop.
	if reply := ask("reload after a refusal", linuxCommand{Action: "reload", Request: request}); !reply.Ready || reply.Error != "" {
		t.Fatalf("reload after a refusal answered %+v", reply)
	}
	if reply := ask("stop", linuxCommand{Action: "stop"}); !reply.Ready {
		t.Fatalf("stop answered %+v", reply)
	}
	close(closed)
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the supervisor did not end")
	}
}

func TestLinuxSupervisorKeepsTheKillSwitchAcrossARestart(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "vpn.lock")
	var calls []string
	var mu sync.Mutex
	call := func(format string, values ...any) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, fmt.Sprintf(format, values...))
	}
	modes := []string{"run", "run", "run", "fail", "run", "run"}
	reserved := 0
	host := linuxTunnelHost{
		lock: func() (*os.File, error) { return acquireLinuxVPNLock(lock) },
		reserve: func() (linuxNetwork, error) {
			reserved++
			network := linuxNetwork{Name: fmt.Sprintf("rv%012d", reserved), Table: 12000 + reserved, Priority: 12000 + reserved, Mark: 0x52560000 | 12001, DNSPort: 5353}
			call("reserve %s", network.Name)
			return network, nil
		},
		cleanup: func(network linuxNetwork) error { call("cleanup %s", network.Name); return nil },
		forget:  func(network linuxNetwork) { call("forget %s", network.Name) },
		worker: func(events func(linuxReply)) (*linuxChild, error) {
			child, err := startLinuxChild(linuxWorkerFixture(modes[0]), events)
			modes = modes[1:]
			return child, err
		},
	}
	commands, closed, replies := make(chan linuxCommand), make(chan struct{}), make(chan linuxReply, 4)
	done := make(chan int, 1)
	go func() {
		done <- superviseLinuxTunnels(context.Background(), commands, closed, func(reply linuxReply) error { replies <- reply; return nil }, os.Getuid(), host)
	}()
	// Each step is a command, whether it is answered ready, and the host calls it makes.
	step := func(stage string, command linuxCommand, ready bool, expected ...string) {
		t.Helper()
		select {
		case commands <- command:
		case code := <-done:
			t.Fatalf("%s: the supervisor exited with %d", stage, code)
		}
		select {
		case reply := <-replies:
			if reply.Ready != ready {
				t.Fatalf("%s answered %+v", stage, reply)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: the supervisor did not answer", stage)
		}
		mu.Lock()
		got := calls
		calls = nil
		mu.Unlock()
		if fmt.Sprint(got) != fmt.Sprint(expected) {
			t.Fatalf("%s: host calls %v, want %v", stage, got, expected)
		}
	}
	locked := func() bool {
		file, err := acquireLinuxVPNLock(lock)
		if err == nil {
			file.Close()
		}
		return err != nil
	}
	blocking := linuxCommand{Action: "start", Request: vpnRequest{Config: linuxConfigFixture,
		RoutePolicy: &share.WindowsRoutePolicy{IncludedRoutes: []string{"0.0.0.0/0"}, BlockWithoutVPN: true}}}
	open := linuxCommand{Action: "start", Request: vpnRequest{Config: linuxConfigFixture}}
	stop, hold := linuxCommand{Action: "stop"}, linuxCommand{Action: "stop", Hold: true}

	step("start", blocking, true, "reserve rv000000000001")
	step("restart stop", hold, true)
	if !locked() {
		t.Fatal("the kept kill switch gave up the tunnel lock")
	}
	step("restart start", blocking, true, "reserve rv000000000002", "cleanup rv000000000001", "forget rv000000000001")

	// The user disconnects during a restart: the kept kill switch goes.
	step("second restart stop", hold, true)
	step("repeated restart stop", hold, true)
	step("disconnect", stop, true, "cleanup rv000000000002", "forget rv000000000002")
	if locked() {
		t.Fatal("a disconnect during a restart kept the tunnel lock")
	}

	// The replacement fails to start: what it installed goes, the whole kill switch stays.
	step("third start", blocking, true, "reserve rv000000000003")
	step("third restart stop", hold, true)
	step("failed restart", blocking, false, "reserve rv000000000004", "cleanup rv000000000004", "cleanup rv000000000004", "forget rv000000000004")
	if !locked() {
		t.Fatal("a failed restart gave up the tunnel lock")
	}
	// A failed start is a lost tunnel: only the next tunnel or Repair lifts its kill switch.
	step("stop after the failure", stop, true)
	step("retry", blocking, true, "reserve rv000000000005", "cleanup rv000000000003", "forget rv000000000003")

	// Without Block traffic without VPN the replacement's failure must not block anything.
	step("turning the kill switch off", hold, true)
	step("open start", open, true, "reserve rv000000000006", "cleanup rv000000000005", "forget rv000000000005")
	step("open restart stop", hold, true, "cleanup rv000000000006", "cleanup rv000000000006", "forget rv000000000006")
	if locked() {
		t.Fatal("a tunnel without the kill switch kept the lock through a restart")
	}
	close(closed)
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the supervisor outlived its owner")
	}
}

// Regression: a frame with only log lines (metrics off) or a tunnel loss was taken
// for the reply to the next command.
func TestLinuxBrokerTellsEventsFromReplies(t *testing.T) {
	events := make(chan linuxReply, 4)
	child, err := startLinuxChild(linuxWorkerFixture("events"), func(reply linuxReply) { events <- reply })
	if err != nil {
		t.Fatal(err)
	}
	defer child.stop()
	reply, err := child.request(linuxCommand{Action: "start", Network: linuxNetwork{Name: "rv000000000001"}}, 10*time.Second)
	if err != nil || !reply.Ready {
		t.Fatal("start failed", err)
	}
	for _, want := range []string{"logs", "lost"} {
		select {
		case event := <-events:
			if want == "logs" && len(event.Logs) != 1 || want == "lost" && !event.Lost {
				t.Fatalf("event %+v, want %s", event, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no %s event", want)
		}
	}
	if child.up.Load() {
		t.Fatal("a lost tunnel still reads as up")
	}
	select {
	case <-child.done:
		t.Fatal("events ended the child")
	default:
	}
}
