package xray

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

// reloadFixture is a listener that answers every connection with its name.
func reloadFixture(t *testing.T, name string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_, _ = connection.Write([]byte(name))
			_ = connection.Close()
		}
	}()
	return listener.Addr().String()
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// A per-app change replaces routing rules and the outbounds they name. The running core takes
// that as it is, with its listeners and its other outbounds untouched; anything more needs a
// restart and changes nothing.
func TestReloadRoutingReplacesRulesAndTheirOutbounds(t *testing.T) {
	first, second := reloadFixture(t, "first"), reloadFixture(t, "second")
	port := freePort(t)
	config := func(inboundPort int, outbounds, rules string) string {
		return fmt.Sprintf(`{"log":{"loglevel":"none"},
			"inbounds":[{"tag":"in","listen":"127.0.0.1","port":%d,"protocol":"socks","settings":{"auth":"noauth"}}],
			"outbounds":[%s],"routing":{"domainStrategy":"AsIs","rules":[%s]}}`, inboundPort, outbounds, rules)
	}
	direct := `{"tag":"direct","protocol":"freedom"}`
	block := `{"tag":"block","protocol":"blackhole"}`
	// What app targets compile to: an outbound of their own, here one that always goes to the second listener.
	app := `{"tag":"elarni-app-0","protocol":"freedom","settings":{"redirect":"` + second + `"}}`
	toBlock := `{"ruleTag":"elarni-per-app-other","inboundTag":["in"],"outboundTag":"block"}`
	toApp := `{"ruleTag":"elarni-per-app-listed-0","inboundTag":["in"],"outboundTag":"elarni-app-0"}`

	if err := RunXrayFromJSON(config(port, direct+","+block, toBlock)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = StopXray() })
	read := func() string {
		t.Helper()
		dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", port), nil, &net.Dialer{Timeout: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		connection, err := dialer.Dial("tcp", first)
		if err != nil {
			return "refused"
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
		answer, _ := io.ReadAll(connection)
		return string(answer)
	}
	if answer := read(); answer == "first" || answer == "second" {
		t.Fatalf("the blocking rule let the connection through: %q", answer)
	}

	// A connection that stays open across the reloads keeps its route.
	held, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	// The rule goes: the first outbound takes the connection.
	if err := ReloadRouting(config(port, direct+","+block, "")); err != nil {
		t.Fatal(err)
	}
	if answer := read(); answer != "first" {
		t.Fatalf("after the rule left: %q", answer)
	}
	// A rule with an outbound of its own comes, and an outbound nothing names leaves.
	if err := ReloadRouting(config(port, direct+","+app, toApp)); err != nil {
		t.Fatal(err)
	}
	if answer := read(); answer != "second" {
		t.Fatalf("after the app's rule came: %q", answer)
	}
	// The listener was never closed.
	if _, err := held.Write([]byte{5, 1, 0}); err != nil {
		t.Fatalf("the reloads closed an open connection: %v", err)
	}
	reply := make([]byte, 2)
	_ = held.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(held, reply); err != nil || reply[0] != 5 {
		t.Fatalf("the reloads closed an open connection: %v %v", reply, err)
	}

	// More than rules and their outbounds: refused, and the core routes as before.
	for name, changed := range map[string]string{
		"another listener":        config(freePort(t), direct+","+app, toApp),
		"a kept outbound changed": config(port, direct+","+strings.Replace(app, second, first, 1), toApp),
		"another first outbound":  config(port, app+","+direct, toApp),
		"another strategy":        strings.Replace(config(port, direct+","+app, toApp), "AsIs", "IPIfNonMatch", 1),
		"an outbound without tag": config(port, direct+","+app+`,{"protocol":"blackhole"}`, toApp),
	} {
		if err := ReloadRouting(changed); !errors.Is(err, ErrRestartNeeded) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A rule the core cannot build fails the reload and takes back the outbound it added.
	if err := ReloadRouting(config(port, direct+","+app+","+block, `{"inboundTag":["in"],"ip":["not an address"],"outboundTag":"block"}`)); err == nil || errors.Is(err, ErrRestartNeeded) {
		t.Errorf("a broken rule: %v", err)
	}
	if answer := read(); answer != "second" {
		t.Fatalf("after refused reloads: %q", answer)
	}
	if err := ReloadRouting(config(port, direct+","+app+","+block, toApp)); err != nil {
		t.Fatalf("the failed reload left its outbound behind: %v", err)
	}

	// The per-app gate the opened-connections pass reads follows the reload.
	if len(tunnelRules) != 0 || !tunnelRest {
		t.Fatalf("gate %+v %v", tunnelRules, tunnelRest)
	}
	withProcess := strings.Replace(toApp, `"inboundTag":["in"]`, `"inboundTag":["in"],"process":["curl"]`, 1)
	if err := ReloadRouting(config(port, direct+","+app+","+block, withProcess)); err != nil {
		t.Fatal(err)
	}
	if len(tunnelRules) != 1 || tunnelRules[0].processes[0] != "curl" {
		t.Fatalf("gate after a reload: %+v", tunnelRules)
	}

	if err := StopXray(); err != nil {
		t.Fatal(err)
	}
	if err := ReloadRouting(config(port, direct, "")); err == nil {
		t.Fatal("a stopped core took a reload")
	}
}
