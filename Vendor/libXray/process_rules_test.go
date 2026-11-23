package libXray

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	xnet "github.com/xtls/xray-core/common/net"
)

func warningCodes(check ProcessRuleCheck) []string {
	var codes []string
	for _, warning := range check.Warnings {
		codes = append(codes, warning.Code+"="+warning.Suggestion)
	}
	return codes
}

// A rule that names no program the core will see matches nothing, silently. The editor
// shows why: the path is gone, names a folder without the trailing separator, a script
// (the interpreter runs), a symlink (the target runs) or the wrong letter case.
func TestProcessRuleChecksExplainRulesThatMatchNothing(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved // macOS: /var is a link to /private/var
	}
	program := filepath.Join(root, "Tools", "app")
	if runtime.GOOS == "windows" {
		program += ".exe"
	}
	if err := os.MkdirAll(filepath.Dir(program), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, []byte("\x7fELF"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "Tools", "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec app\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(root, "Tools")
	sep := string(filepath.Separator)
	want := map[string][]string{
		program:                            nil,
		folder + sep:                       nil,
		folder:                             {"folder=" + folder + sep},
		filepath.Join(root, "Gone", "app"): {"missing="},
		filepath.Join(root, "Gone") + sep:  {"missing="},
		program + sep:                      {"notFolder=" + program},
		"curl":                             nil,
		"-1":                               nil,
	}
	if runtime.GOOS != "windows" {
		want[script] = []string{"script="}
		link := filepath.Join(root, "app-link")
		if err := os.Symlink(program, link); err != nil {
			t.Fatal(err)
		}
		want[link] = []string{"symlink=" + program}
		// Rules compare case-sensitively here, also on macOS's case-insensitive disks.
		want[filepath.Join(root, "tools", "app")] = []string{"case=" + program}
	}
	if runtime.GOOS == "windows" {
		// Windows rules ignore case; launchers start another program.
		want[strings.ToUpper(program)] = nil
		for _, name := range []string{"start.bat", "start.cmd", "App.lnk"} {
			path := filepath.Join(root, "Tools", name)
			if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			want[path] = []string{"launcher="}
		}
		// A Squirrel app's root .exe only starts app-<version>\<name>.exe.
		squirrel := filepath.Join(root, "Discord")
		for _, dir := range []string{squirrel, filepath.Join(squirrel, "app-1.0.9")} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"Update.exe", "Discord.exe"} {
			if err := os.WriteFile(filepath.Join(squirrel, name), []byte("MZ"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		want[filepath.Join(squirrel, "Discord.exe")] = []string{"launcher=" + squirrel + sep}
		// A rule on another computer is not opened, which would sign in to that server.
		want[`\\192.0.2.1\share\app.exe`] = nil
	}
	if runtime.GOOS == "darwin" {
		// A bare name is compared with the executable's name, never "Example.app".
		want["Example.app"] = []string{"bundleName="}
	}
	var opened []string
	previous := ruleLstat
	ruleLstat = func(path string) (os.FileInfo, error) {
		opened = append(opened, path)
		return previous(path)
	}
	t.Cleanup(func() { ruleLstat = previous })
	var rules []string
	for rule := range want {
		rules = append(rules, rule)
	}
	slices.Sort(rules)
	result := checkProcessRules(rules)
	if len(result.Rules) != len(rules) {
		t.Fatalf("%d results for %d rules", len(result.Rules), len(rules))
	}
	for i, check := range result.Rules {
		if check.Rule != rules[i] {
			t.Fatalf("result %d is for %q, want %q", i, check.Rule, rules[i])
		}
		if got := warningCodes(check); !slices.Equal(got, want[check.Rule]) {
			t.Errorf("%q: warnings %v, want %v", check.Rule, got, want[check.Rule])
		}
	}
	for _, path := range opened {
		if strings.HasPrefix(path, `\\`) {
			t.Errorf("opened %q, on another computer", path)
		}
	}
}

// Flatpak and AppImage rules are checked on Linux, service rules on Windows: an app that is
// not installed, an image that is gone or a folder without its separator. Suggestions keep the
// rule's kind.
func TestProcessRuleChecksOfTypedRules(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	want := map[string][]string{}
	switch runtime.GOOS {
	case "linux":
		images := filepath.Join(root, "Applications")
		image := filepath.Join(images, "Cursor.AppImage")
		if err := os.MkdirAll(images, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(image, []byte("\x7fELF"), 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "cursor")
		if err := os.Symlink(image, link); err != nil {
			t.Fatal(err)
		}
		installed := filepath.Join(root, "flatpak", "app", "org.example.Installed")
		if err := os.MkdirAll(installed, 0o755); err != nil {
			t.Fatal(err)
		}
		previous := flatpakInstallations
		flatpakInstallations = func() []string { return []string{filepath.Join(root, "missing"), filepath.Join(root, "flatpak")} }
		t.Cleanup(func() { flatpakInstallations = previous })
		want["appimage:"+image] = nil
		want["appimage:"+images+"/"] = nil
		want["appimage:"+images] = []string{"folder=appimage:" + images + "/"}
		want["appimage:"+filepath.Join(images, "Gone.AppImage")] = []string{"missing="}
		want["appimage:"+link] = []string{"symlink=appimage:" + image}
		want["appimage:"+filepath.Join(root, "applications", "Cursor.AppImage")] = []string{"case=appimage:" + image}
		want["flatpak:org.example.Installed"] = nil
		want["flatpak:org.example.Gone"] = []string{"missing="}
		want["flatpak:not an id"] = []string{"missing="}
		want["service:wuauserv"] = nil
	case "windows":
		// The RPC service runs on every Windows.
		want["service:RpcSs"] = nil
		want["service:rpcss"] = nil
		want["service:ElarniNoSuchService"] = []string{"missing="}
		want["flatpak:org.example.Gone"] = nil
		want[`appimage:C:\Gone.AppImage`] = nil
	default:
		want["flatpak:org.example.Gone"] = nil
		want["service:wuauserv"] = nil
	}
	var rules []string
	for rule := range want {
		rules = append(rules, rule)
	}
	slices.Sort(rules)
	for i, check := range checkProcessRules(rules).Rules {
		if check.Rule != rules[i] {
			t.Fatalf("result %d is for %q, want %q", i, check.Rule, rules[i])
		}
		if got := warningCodes(check); !slices.Equal(got, want[check.Rule]) {
			t.Errorf("%q: warnings %v, want %v", check.Rule, got, want[check.Rule])
		}
		if runtime.GOOS == "windows" && strings.EqualFold(check.Rule, "service:RpcSs") && check.Running != 1 {
			t.Errorf("%q: %d running", check.Rule, check.Running)
		}
	}
}

// A rule that also takes the programs an app starts is checked as the app's rule; its fixes
// keep "children:".
func TestProcessRuleChecksOfChildrenRules(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	folder := filepath.Dir(executable)
	gone := filepath.Join(t.TempDir(), "gone")
	sep := string(filepath.Separator)
	rules := []string{"children:" + executable, "children:" + folder, "children:" + gone}
	result := checkProcessRules(rules)
	want := [][]string{nil, {"folder=children:" + folder + sep}, {"missing="}}
	for i, check := range result.Rules {
		if got := warningCodes(check); !slices.Equal(got, want[i]) {
			t.Errorf("%q: warnings %v, want %v", check.Rule, got, want[i])
		}
	}
	if result.Listed && result.Rules[0].Running == 0 {
		t.Errorf("%q matches no running program", rules[0])
	}
}

// The count uses the core's own matcher, so it agrees with what routing will do.
func TestProcessRuleChecksCountRunningPrograms(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	name := filepath.Base(executable)
	rules := []string{executable, filepath.Dir(executable) + string(filepath.Separator), name,
		filepath.Join(t.TempDir(), "never-running")}
	raw := Invoke(`{"apiVersion":1,"method":"checkProcessRules","payload":` + mustJSON(t, map[string]any{"rules": rules}) + `}`)
	var reply struct {
		Success bool                     `json:"success"`
		Data    ProcessRuleCheckResponse `json:"data"`
		Error   string                   `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &reply); err != nil || !reply.Success {
		t.Fatalf("checkProcessRules: %s", raw)
	}
	if !reply.Data.Listed {
		t.Fatal("running programs were not listed")
	}
	for i, check := range reply.Data.Rules {
		running := i < 3
		if (check.Running > 0) != running {
			t.Errorf("%q: %d running, want running=%v", check.Rule, check.Running, running)
		}
	}
}

func TestProcessRuleChecksRejectInvalidRequests(t *testing.T) {
	long := strings.Repeat("a", 1025)
	tooMany := make([]string, 257)
	for i := range tooMany {
		tooMany[i] = "curl"
	}
	for _, rules := range [][]string{{long}, tooMany, {""}, {"a\nb"}} {
		raw := Invoke(`{"apiVersion":1,"method":"checkProcessRules","payload":` + mustJSON(t, map[string]any{"rules": rules}) + `}`)
		if !strings.Contains(raw, `"success":false`) {
			t.Errorf("%d rules accepted: %s", len(rules), raw[:min(len(raw), 120)])
		}
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Routing compares paths without version numbers, so a rule saved for one version still matches
// after an update removed that version's files. The check looks for the version on disk as
// routing would, and says nothing about a folder it may not list, such as WindowsApps.
func TestProcessRuleChecksFollowTheVersionOnDisk(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	name := "example"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	installed := filepath.Join(root, "Example", "app-1.0.2")
	if err := os.MkdirAll(installed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, name), []byte("\x7fELF"), 0o755); err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(root, "Example", "app-1.0.1")
	sep := string(filepath.Separator)
	want := map[string][]string{
		filepath.Join(saved, name):       nil,
		saved + sep:                      nil,
		saved:                            {"folder=" + installed + sep},
		filepath.Join(saved, name) + sep: {"notFolder=" + filepath.Join(installed, name)},
		filepath.Join(saved, "gone"):     {"missing="},
		filepath.Join(root, "Example", "other-1.0.1", name): {"missing="},
		// A pre-release that is gone is followed to the release that replaced it.
		filepath.Join(root, "Example", "app-1.0.1-beta") + sep:   nil,
		filepath.Join(root, "Example", "app-1.0.1-custom") + sep: {"missing="},
		filepath.Join(root, "Absent", "app-1.0.1", name):         {"missing="},
		"children:" + filepath.Join(saved, name):                 nil,
		"children:" + filepath.Join(root, "Absent", "1.2", name): {"missing="},
	}
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		// Entries can be opened by name but not listed, as in WindowsApps.
		locked := filepath.Join(root, "Locked")
		if err := os.MkdirAll(filepath.Join(locked, "2.0"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0o111); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o755) })
		want[filepath.Join(locked, "1.0", name)] = nil
	}
	var rules []string
	for rule := range want {
		rules = append(rules, rule)
	}
	slices.Sort(rules)
	for _, check := range checkProcessRules(rules).Rules {
		if got := warningCodes(check); !slices.Equal(got, want[check.Rule]) {
			t.Errorf("%q: warnings %v, want %v", check.Rule, got, want[check.Rule])
		}
	}
}

// A rule that matches a running program is not missing, whatever the disk says: a Store app's
// earlier version was reported as not found while it matched every program of the app. A program
// that is gone while the same one runs from a sibling folder, such as one named by a build's
// hash, is offered that path.
func TestProcessRuleChecksOfProgramsRunningFromAnotherPath(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	at := func(parts ...string) string { return filepath.Join(append([]string{root}, parts...)...) }
	running := []string{
		at("Store", "app-2.0.0", "chat"),
		at("Hashed", "d4e5f6", "bin", "tool"),
		at("Two", "x1y", "tool"), at("Two", "z2w", "tool"), at("Two", "z2w", "tool"),
		at("Deep", "one", "two", "far"),
	}
	previous := listProcesses
	listProcesses = func() ([]xnet.Process, error) {
		var processes []xnet.Process
		for i, path := range running {
			// The core reports paths with forward slashes.
			processes = append(processes, xnet.Process{PID: 100 + i, Name: filepath.Base(path), Path: filepath.ToSlash(path)})
		}
		return processes, nil
	}
	t.Cleanup(func() { listProcesses = previous })
	sep := string(filepath.Separator)
	for _, tc := range []struct {
		rule     string
		running  int
		warnings []string
	}{
		{at("Store", "app-1.0.0", "chat"), 1, nil},
		{"children:" + at("Store", "app-1.0.0", "chat"), 1, nil},
		{at("Hashed", "a1b2c3", "bin", "tool"), 0, []string{"moved=" + at("Hashed", "d4e5f6", "bin", "tool")}},
		{"children:" + at("Hashed", "a1b2c3", "bin", "tool"), 0, []string{"moved=children:" + at("Hashed", "d4e5f6", "bin", "tool")}},
		// Not the same program: two candidates, another name, another depth, two folders renamed.
		{at("Two", "aaa", "tool"), 0, []string{"missing="}},
		{at("Hashed", "a1b2c3", "bin", "other"), 0, []string{"missing="}},
		{at("Hashed", "a1b2c3", "tool"), 0, []string{"missing="}},
		{at("Deep", "uno", "dos", "far"), 0, []string{"missing="}},
		// A folder names no single program.
		{at("Hashed", "a1b2c3") + sep, 0, []string{"missing="}},
	} {
		check := checkProcessRules([]string{tc.rule}).Rules[0]
		if got := warningCodes(check); check.Running != tc.running || !slices.Equal(got, tc.warnings) {
			t.Errorf("%q: %d running, warnings %v; want %d, %v", tc.rule, check.Running, got, tc.running, tc.warnings)
		}
	}
}

// Routing follows a renamed folder only when no build of what a rule names is left, so the
// check looks for the rule's program in another build before it looks through a renamed folder.
func TestProcessRuleChecksPreferAnotherBuildToARenamedFolder(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	name := "example"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	at := func(parts ...string) string { return filepath.Join(append([]string{root}, parts...)...) }
	// The rule's version holds a renamed folder where a folder stands in the program's place;
	// the next version holds the program.
	if err := os.MkdirAll(at("Tool", "app-1.0.1", "jre-22", name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(at("Tool", "app-1.0.2", "jre-21"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(at("Tool", "app-1.0.2", "jre-21", name), []byte("\x7fELF"), 0o755); err != nil {
		t.Fatal(err)
	}
	rule := at("Tool", "app-1.0.1", "jre-21", name)
	if got := warningCodes(checkProcessRules([]string{rule}).Rules[0]); len(got) != 0 {
		t.Fatalf("%q: warnings %v, want none: its program is in the next version", rule, got)
	}
}

// A rule whose folder an update replaced is checked as what replaced it, as routing follows it:
// jdk-21 to jdk-22, "Photoshop 2025" to "Photoshop 2026". An "exact:" rule names its path alone.
func TestProcessRuleChecksFollowAReplacedFolder(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	name := "example"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	at := func(parts ...string) string { return filepath.Join(append([]string{root}, parts...)...) }
	for _, folder := range []string{at("java", "jdk-22"), at("Adobe", "Photoshop 2026"), at("Squirrel", "app-1.0.2"), at("Two", "v1", "w2")} {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, name), []byte("\x7fELF"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sep := string(filepath.Separator)
	want := map[string][]string{
		at("java", "jdk-21", name):                     nil,
		at("java", "jdk-21") + sep:                     nil,
		at("java", "jdk-21"):                           {"folder=" + at("java", "jdk-22") + sep},
		at("Adobe", "Photoshop 2025", name):            nil,
		at("java", "jre-21", name):                     {"missing="},
		at("java", "jdk-21", "other"):                  {"missing="},
		at("Two", "v3", "w4", name):                    {"missing="}, // two folders were replaced
		at("Squirrel", "app-1.0.1", name):              nil,
		"exact:" + at("java", "jdk-22", name):          nil,
		"exact:" + at("java", "jdk-21", name):          {"missing="},
		"exact:" + at("Squirrel", "app-1.0.1", name):   {"missing="},
		"exact:" + at("java", "jdk-22"):                {"folder=exact:" + at("java", "jdk-22") + sep},
		"children:exact:" + at("java", "jdk-21") + sep: {"missing="},
	}
	var rules []string
	for rule := range want {
		rules = append(rules, rule)
	}
	slices.Sort(rules)
	for _, check := range checkProcessRules(rules).Rules {
		if got := warningCodes(check); !slices.Equal(got, want[check.Rule]) {
			t.Errorf("%q: warnings %v, want %v", check.Rule, got, want[check.Rule])
		}
	}

	// The running program counts for the rule it replaced, and no other path is offered.
	previous := listProcesses
	listProcesses = func() ([]xnet.Process, error) {
		return []xnet.Process{{PID: 100, Name: name, Path: filepath.ToSlash(at("java", "jdk-22", name))}}, nil
	}
	t.Cleanup(func() { listProcesses = previous })
	for rule, running := range map[string]int{
		at("java", "jdk-21", name):            1,
		at("java", "jdk-21") + sep:            1,
		"exact:" + at("java", "jdk-21", name): 0,
		"exact:" + at("java", "jdk-22", name): 1,
	} {
		check := checkProcessRules([]string{rule}).Rules[0]
		if check.Running != running {
			t.Errorf("%q: %d running, want %d", rule, check.Running, running)
		}
		if strings.HasPrefix(rule, "exact:") && running == 0 && !slices.Equal(warningCodes(check), []string{"missing="}) {
			t.Errorf("%q: warnings %v, want it missing without an offer", rule, warningCodes(check))
		}
	}
}

// The editor shows an app as listed when a saved rule is that app's rule, also one written for
// another version. It compared the rules' text, so after an update the app showed as not listed,
// and ticking it added a second rule that the first one overruled.
func TestProcessRuleChecksTellWhichRuleAnAppIs(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	at := func(parts ...string) string { return filepath.Join(append([]string{root}, parts...)...) }
	sep := string(filepath.Separator)
	for _, folder := range []string{at("Example", "app-1.0.2"), at("java", "jdk-22", "bin"), at("Games", "Anno 1800"), at("Games", "Anno 2070")} {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rules := []string{
		at("Example", "app-1.0.1", "example"),
		"children:" + at("java", "jdk-21", "bin", "java"),
		at("Games", "Anno 1800") + sep,
		"flatpak:org.example.App",
	}
	apps := []string{
		at("Example", "app-1.0.2", "example"),
		at("java", "jdk-22", "bin", "java"),
		at("Games", "Anno 1800") + sep,
		"flatpak:org.example.App",
		at("Games", "Anno 2070") + sep,
		"flatpak:org.example.Other",
		at("Example", "app-1.0.2", "other"),
		at("Example") + sep,
	}
	want := map[string]string{apps[0]: rules[0], apps[1]: rules[1], apps[2]: rules[2], apps[3]: rules[3]}
	if got := sameProcessRules(rules, apps); !maps.Equal(got, want) {
		t.Errorf("same rules %v, want %v", got, want)
	}

	var response struct {
		Success bool
		Data    ProcessRuleCheckResponse
		Error   string
	}
	request := mustJSON(t, map[string]any{"apiVersion": 1, "method": "checkProcessRules", "payload": map[string]any{"rules": rules, "apps": apps}})
	if err := json.Unmarshal([]byte(Invoke(request)), &response); err != nil || !response.Success {
		t.Fatalf("check failed: %v %q", err, response.Error)
	}
	if !maps.Equal(response.Data.Same, want) {
		t.Errorf("the check's same rules %v, want %v", response.Data.Same, want)
	}
	for _, apps := range [][]string{{""}, {" padded"}, {"line\nbreak"}, make([]string, 4097)} {
		request := mustJSON(t, map[string]any{"apiVersion": 1, "method": "checkProcessRules", "payload": map[string]any{"rules": rules, "apps": apps}})
		if err := json.Unmarshal([]byte(Invoke(request)), &response); err != nil || response.Success || response.Error != "invalid_rules" {
			t.Errorf("apps %.20q: accepted (%v, %q)", apps, err, response.Error)
		}
	}
}
