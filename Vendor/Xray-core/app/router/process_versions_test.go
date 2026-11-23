package router

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
)

// Elarni: an update moves many apps to a new path: Squirrel's app-<version>, Edge WebView2's
// Application\<version>, Battle.net.<build>, a folder named by a build's hash, a Snap's revision. A
// rule saved for one version stopped matching, app by app. Paths compare without what names a build.
func TestPathsCompareWithoutVersionNumbers(t *testing.T) {
	for _, tc := range []struct{ goos, path, want string }{
		{"windows", `C:\Users\me\AppData\Local\Discord\app-1.0.9265\Discord.exe`, "c:/users/me/appdata/local/discord/app-*/discord.exe"},
		{"windows", "c:/users/me/appdata/local/discord/app-1.0.9270/discord.exe", "c:/users/me/appdata/local/discord/app-*/discord.exe"},
		{"windows", "c:/program files (x86)/microsoft/edgewebview/application/131.0.2903.86/msedgewebview2.exe",
			"c:/program files (x86)/microsoft/edgewebview/application/*/msedgewebview2.exe"},
		{"windows", "c:/program files (x86)/battle.net/battle.net.15173/battle.net.exe", "c:/program files (x86)/battle.net/battle.net.*/battle.net.exe"},
		// A folder named by a build's hash: Roblox, Visual Studio Code and its server.
		{"windows", "c:/users/me/appdata/local/roblox/versions/version-0a1b2c3d4e5f6a7b/robloxplayerbeta.exe",
			"c:/users/me/appdata/local/roblox/versions/version-*/robloxplayerbeta.exe"},
		{"windows", "c:/users/me/appdata/local/programs/microsoft vs code/0f1a2b3c4d/code.exe", "c:/users/me/appdata/local/programs/microsoft vs code/*/code.exe"},
		{"linux", "/home/me/.vscode-server/bin/1a5daa3a0231a0fbba4f14db7ec463cf99d7768e/node", "/home/me/.vscode-server/bin/*/node"},
		{"linux", "/nix/store/7g3bslhh3gm3mrqmzb1kc7r1xy5fvkq5-firefox-131.0/bin/firefox", "/nix/store/*-firefox-*/bin/firefox"},
		// A number after a space, or one that is a folder's whole name, is part of a name: two
		// products, two users, two processes. Such a rule follows only a folder that replaced its own.
		{"windows", "c:/program files/adobe/adobe photoshop 2025/photoshop.exe", "c:/program files/adobe/adobe photoshop 2025/photoshop.exe"},
		{"windows", "c:/games/anno 1800/bin/anno.exe", "c:/games/anno 1800/bin/anno.exe"},
		{"linux", "/proc/12345/root/usr/bin/example", "/proc/12345/root/usr/bin/example"},
		{"linux", "/home/1001/bin/example", "/home/1001/bin/example"},
		// Words and numbers are no hash.
		{"windows", "c:/tools/deadbeef/tool.exe", "c:/tools/deadbeef/tool.exe"},
		{"windows", "c:/tools/12345678/tool.exe", "c:/tools/12345678/tool.exe"},
		{"windows", "c:/tools/cafe123/tool.exe", "c:/tools/cafe123/tool.exe"},
		{"windows", "c:/tools/feedback2024a/tool.exe", "c:/tools/feedback2024a/tool.exe"},
		{"windows", "c:/tools/0f1a2b3c4d.exe", "c:/tools/0f1a2b3c4d.exe"},
		{"windows", "c:/users/a1234567/app/app.exe", "c:/users/a1234567/app/app.exe"},
		{"windows", "c:/tools/decade2000/tool.exe", "c:/tools/decade2000/tool.exe"},
		{"windows", "c:/program files/java/jdk-21.0.2/bin/java.exe", "c:/program files/java/jdk-*/bin/java.exe"},
		{"windows", "c:/tools/rufus-4.6.exe", "c:/tools/rufus-*.exe"},
		{"windows", "c:/program files/unity/hub/editor/2022.3.10f1/editor/unity.exe", "c:/program files/unity/hub/editor/*f1/editor/unity.exe"},
		// A Store package still compares as its family, whatever its version and architecture.
		{"windows", `C:\Program Files\WindowsApps\OpenAI.ChatGPT-Desktop_1.2025.112.0_x64__2p2nqsd0c76g0\app\ChatGPT.exe`,
			"c:/program files/windowsapps/openai.chatgpt-desktop_2p2nqsd0c76g0/app/chatgpt.exe"},
		// A family's name keeps its numbers: Python 3.12 and 3.13 are two packages.
		{"windows", `C:\Program Files\WindowsApps\PythonSoftwareFoundation.Python.3.12_3.12.2800.0_x64__qbz5n2kfra8p0\python3.12.exe`,
			"c:/program files/windowsapps/pythonsoftwarefoundation.python.3.12_qbz5n2kfra8p0/python*.exe"},
		{"windows", `C:\Program Files\WindowsApps\PythonSoftwareFoundation.Python.3.12_qbz5n2kfra8p0\`,
			"c:/program files/windowsapps/pythonsoftwarefoundation.python.3.12_qbz5n2kfra8p0/"},
		// Numbers that are no version stay: short ones, and those inside a name or a file's name.
		{"windows", "c:/windows/system32/svchost.exe", "c:/windows/system32/svchost.exe"},
		{"windows", "c:/program files (x86)/steam/steamapps/common/portal 2/portal2.exe", "c:/program files (x86)/steam/steamapps/common/portal 2/portal2.exe"},
		{"windows", "c:/program files/microsoft office/root/office16/winword.exe", "c:/program files/microsoft office/root/office16/winword.exe"},
		{"windows", "c:/games/battlefield/bf2042.exe", "c:/games/battlefield/bf2042.exe"},
		{"windows", "c:/tools/7z2408-x64.exe", "c:/tools/7z2408-x64.exe"},
		{"windows", "c:/tools/x264/x264.exe", "c:/tools/x264/x264.exe"},
		{"windows", "c:/python312/python.exe", "c:/python312/python.exe"},
		{"windows", "c:/users/user1990/app/app.exe", "c:/users/user1990/app/app.exe"},
		{"linux", "/snap/firefox/5187/usr/lib/firefox/firefox", "/snap/firefox/*/usr/lib/firefox/firefox"},
		{"linux", "/snap/hello/42/bin/hello", "/snap/hello/*/bin/hello"},
		{"linux", "/snap/hello/x1/bin/hello", "/snap/hello/*/bin/hello"},
		{"linux", "/snap/hello/current/bin/hello", "/snap/hello/current/bin/hello"},
		{"linux", "/home/me/.local/share/claude/versions/2.1.300", "/home/me/.local/share/claude/versions/*"},
		{"linux", "/usr/bin/python3.12", "/usr/bin/python*"},
		{"linux", "/opt/Example/Example", "/opt/Example/Example"},
		// Other systems keep the letter case and have no Store packages.
		{"darwin", "/opt/homebrew/Cellar/node/23.1.0/bin/node", "/opt/homebrew/Cellar/node/*/bin/node"},
		{"darwin", "/Applications/Example.app/", "/Applications/Example.app/"},
		// macOS runs a quarantined app from a folder it names anew at each start.
		{"darwin", "/private/var/folders/ab/cd_ef0123/T/AppTranslocation/6E2A9F0C-1B2D-4C3E-9F10-AABBCCDDEE01/d/Example.app/Contents/MacOS/Example",
			"/private/var/folders/ab/cd_ef0123/T/AppTranslocation/*/d/Example.app/Contents/MacOS/Example"},
		{"linux", "/tmp/AppTranslocation/6E2A9F0C-1B2D-4C3E-9F10-AABBCCDDEEFF/d/example", "/tmp/AppTranslocation/6E2A9F0C-1B2D-4C3E-9F10-AABBCCDDEEFF/d/example"},
		{"darwin", "/Users/me/WindowsApps/claude_4.23118.0.0_x64__pzs8sxrjxfjjc/claude", "/Users/me/WindowsApps/claude_*_x64__pzs8sxrjxfjjc/claude"},
		{"linux", "", ""},
		{"linux", "curl", "curl"},
	} {
		if got := pathKey(tc.path, tc.goos); got != tc.want {
			t.Errorf("pathKey(%q, %s) = %q, want %q", tc.path, tc.goos, got, tc.want)
		}
	}
}

// A rule for one version takes the next version's connections, as an exact path and as a folder,
// and never another program's.
func TestRulesForOneVersionMatchTheNext(t *testing.T) {
	// The rules' own paths are on this computer: both versions, both products are installed.
	previous := rulePathGone
	rulePathGone = func(string, bool) bool { return false }
	t.Cleanup(func() { rulePathGone = previous })
	for _, tc := range []struct {
		name, rule, path string
		want             bool
	}{
		{"exact path", "/opt/Example/app-1.0.9265/example", "/opt/Example/app-1.0.9270/example", true},
		{"folder", "/opt/Example/app-1.0.9265/", "/opt/Example/app-1.0.9270/lib/helper", true},
		{"the folder of versions", "/opt/Example/", "/opt/Example/app-1.0.9270/example", true},
		{"build number", "/opt/Example/Example.15173/example", "/opt/Example/Example.15200/example", true},
		{"with the programs it starts", "children:/opt/Example/1.2.3/example", "/opt/Example/1.2.4/example", true},
		{"another program of that version", "/opt/Example/app-1.0.9265/example", "/opt/Example/app-1.0.9270/other", false},
		{"another app", "/opt/Example/app-1.0.9265/example", "/opt/Other/app-1.0.9265/example", false},
		{"a version is no folder", "/opt/Example/1.2/", "/opt/Example/1.25", false},
		{"a bare name keeps its numbers", "python3.12", "/usr/bin/python3.13", false},
		{"a build's hash", "/opt/Example/version-0a1b2c3d4e5f/example", "/opt/Example/version-9f8e7d6c5b4a/example", true},
		{"a number in a product's name", "/games/Anno 1800/", "/games/Anno 2070/bin/game", false},
		{"the same file in another product", "/games/Manager 2023/fm", "/games/Manager 2024/fm", false},
		// "exact:" compares a rule as it is written.
		{"exact, the path itself", "exact:/opt/Example/app-1.0.9265/example", "/opt/Example/app-1.0.9265/example", true},
		{"exact, the next version", "exact:/opt/Example/app-1.0.9265/example", "/opt/Example/app-1.0.9270/example", false},
		{"exact folder, itself", "exact:/opt/Python/3.12/", "/opt/Python/3.12/bin/python", true},
		{"exact folder, another version", "exact:/opt/Python/3.12/", "/opt/Python/3.13/bin/python", false},
		{"exact, with the programs it starts", "children:exact:/opt/Python/3.12/", "/opt/Python/3.13/bin/python", false},
		{"exact without a path", "exact:", "", false},
		{"exact, a bare name", "exact:example", "/opt/Example/example", true},
	} {
		if got := NewProcessNameMatcher([]string{tc.rule}).MatchesProcess(77, path.Base(tc.path), tc.path); got != tc.want {
			t.Errorf("%s: rule %q matched %q: %v, want %v", tc.name, tc.rule, tc.path, got, tc.want)
		}
	}
}

// Many updates rename a folder in ways no rule can tell from another product's folder: jdk-21 to
// jdk-22, Python312 to Python313, "Photoshop 2025" to "Photoshop 2026". What tells them apart is
// the disk: a rule whose own path is gone names what replaced it, the same path through one
// folder whose name differs only in its numbers. While its path exists, a rule names only that.
func TestRulesFollowAReplacedFolder(t *testing.T) {
	gone := true
	asked := 0
	previous := rulePathGone
	rulePathGone = func(string, bool) bool { asked++; return gone }
	t.Cleanup(func() { rulePathGone = previous })

	for _, tc := range []struct {
		name, rule, path string
		want             bool
	}{
		{"a short number", "/opt/java/jdk-21/bin/java", "/opt/java/jdk-22/bin/java", true},
		{"a number inside the name", "/opt/Python312/python", "/opt/Python313/python", true},
		{"a number after a letter", "/opt/app v5/app", "/opt/app v6/app", true},
		{"a year in the name", "/opt/Adobe Photoshop 2025/photoshop", "/opt/Adobe Photoshop 2026/photoshop", true},
		{"a whole name", "/opt/Example/41/example", "/opt/Example/42/example", true},
		{"a pre-release", "/opt/Example/app-4.42.0-beta1/example", "/opt/Example/app-4.43.0/example", true},
		{"a release candidate", "/opt/Example/1.2.3-rc.1/example", "/opt/Example/1.2.3/example", true},
		{"a package revision", "/opt/homebrew/Cellar/node/23.1.0/bin/node", "/opt/homebrew/Cellar/node/23.1.0_1/bin/node", true},
		{"a build letter", "/opt/Unity/2022.3.10f1/Unity", "/opt/Unity/2022.3.10f2/Unity", true},
		{"a renamed file", "/opt/tools/example-v5", "/opt/tools/example-v6", true},
		{"a folder rule", "/opt/java/jdk-21/", "/opt/java/jdk-22/bin/java", true},
		{"with the programs it starts", "children:/opt/java/jdk-21/bin/java", "/opt/java/jdk-22/bin/java", true},
		{"two folders differ", "/opt/a1/b1/example", "/opt/a2/b2/example", false},
		{"another name", "/opt/alpha1/example", "/opt/beta1/example", false},
		{"a name without a number", "/opt/alpha/example", "/opt/beta/example", false},
		{"another program", "/opt/java/jdk-21/bin/java", "/opt/java/jdk-22/bin/javac", false},
		{"a folder rule's own level", "/opt/java/jdk-21/", "/opt/java/jdk-22", false},
		{"deeper than the rule", "/opt/java/jdk-21/bin/java", "/opt/java/jdk-22/bin/java/x", false},
		{"exact", "exact:/opt/java/jdk-21/bin/java", "/opt/java/jdk-22/bin/java", false},
	} {
		matcher := NewProcessNameMatcher([]string{tc.rule})
		if got := matcher.MatchesProcess(77, path.Base(tc.path), tc.path); got != tc.want {
			t.Errorf("%s: rule %q followed to %q: %v, want %v", tc.name, tc.rule, tc.path, got, tc.want)
		}
	}

	// While the rule's own path is there, it names only that.
	gone = false
	for _, tc := range [][2]string{
		{"/opt/java/jdk-21/bin/java", "/opt/java/jdk-22/bin/java"},
		{"/opt/Adobe Photoshop 2025/", "/opt/Adobe Photoshop 2026/photoshop"},
	} {
		if NewProcessNameMatcher([]string{tc[0]}).MatchesProcess(77, path.Base(tc[1]), tc[1]) {
			t.Errorf("rule %q, still on disk, matched %q", tc[0], tc[1])
		}
	}

	// Routing asks for each connection; the disk is asked once in a while, and only for a
	// program that could be the rule's.
	gone = true
	matcher := NewProcessNameMatcher([]string{"/opt/java/jdk-21/bin/java"})
	asked = 0
	for range 50 {
		if !matcher.MatchesProcess(77, "java", "/opt/java/jdk-22/bin/java") {
			t.Fatal("the rule stopped following")
		}
		matcher.MatchesProcess(78, "other", "/opt/other/bin/other")
	}
	if asked != 1 {
		t.Fatalf("the disk was asked %d times for 100 lookups, want 1", asked)
	}
}

// The same, on a real disk.
func TestRuleFollowsAFolderReplacedOnDisk(t *testing.T) {
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	program := func(version string) string {
		return filepath.Join(root, "Example", version, "bin", "example")
	}
	install := func(version string) {
		if err := os.MkdirAll(filepath.Dir(program(version)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(program(version), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	install("build-7")
	rule := program("build-6")
	if !NewProcessNameMatcher([]string{rule}).MatchesProcess(77, "example", program("build-7")) {
		t.Fatalf("rule %q did not follow to the folder that replaced its own", rule)
	}
	if !NewProcessNameMatcher([]string{filepath.Dir(filepath.Dir(rule)) + string(filepath.Separator)}).MatchesProcess(77, "example", program("build-7")) {
		t.Fatal("a folder rule did not follow")
	}
	install("build-6")
	if NewProcessNameMatcher([]string{rule}).MatchesProcess(77, "example", program("build-7")) {
		t.Fatalf("rule %q matched another version beside its own", rule)
	}
}

// An app whose folders name its builds loses its rule's own path at every update, and that tells
// nothing: the rule follows a renamed folder only when no build of what it names is left.
func TestRuleStaysWithItsAppWhileABuildOfItIsThere(t *testing.T) {
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	at := func(parts ...string) string { return filepath.Join(append([]string{root}, parts...)...) }
	install := func(parts ...string) string {
		program := at(parts...)
		if err := os.MkdirAll(filepath.Dir(program), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(program, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		return program
	}
	matches := func(rule, program string) bool {
		return NewProcessNameMatcher([]string{rule}).MatchesProcess(77, filepath.Base(program), program)
	}

	// Two channels of one product, each in a folder of builds; the rule's build was replaced.
	stable := install("Example", "ch-0", "241.2.1", "bin", "example")
	preview := install("Example", "ch-1", "242.1.3", "bin", "example")
	rule := at("Example", "ch-0", "233.1.5", "bin", "example")
	if !matches(rule, stable) {
		t.Fatalf("rule %q did not match its app's next build", rule)
	}
	if matches(rule, preview) {
		t.Fatalf("rule %q took the channel installed beside its own", rule)
	}
	if matches(at("Example", "ch-0")+string(filepath.Separator), preview) {
		t.Fatal("a folder rule took the channel installed beside its own")
	}
	// The same through a folder of the app's version: the build beside the rule's is the app.
	install("Tool", "app-1.0.2", "jre-21", "bin", "java")
	other := install("Tool", "app-1.0.2", "jre-22", "bin", "java")
	if matches(at("Tool", "app-1.0.1", "jre-21", "bin", "java"), other) {
		t.Fatal("a rule took another folder of the version that still holds its own")
	}
	// Its channel removed, the rule follows the one that is left.
	if err := os.RemoveAll(at("Example", "ch-0")); err != nil {
		t.Fatal(err)
	}
	if !matches(rule, preview) {
		t.Fatalf("rule %q did not follow to the folder that replaced its own", rule)
	}
	// A folder that an uninstaller left without the program does not hold the rule back.
	if err := os.MkdirAll(at("Left", "build-6", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	next := install("Left", "build-7", "bin", "example")
	if !matches(at("Left", "build-6", "bin", "example"), next) {
		t.Fatal("a rule did not follow past the folder an uninstaller left")
	}
}

// The editor shows which versions a rule takes now. A program's version of a rule is what
// stands in its path where the rule's own path stands.
func TestRuleVersionOfAProgram(t *testing.T) {
	for _, tc := range []struct{ goos, rule, program, want string }{
		{"linux", "/opt/Python/3.12/bin/python", "/opt/Python/3.13/bin/python", "/opt/Python/3.13/bin/python"},
		{"linux", "/opt/Python/3.12/bin/python", "/opt/Python/3.12/bin/python", "/opt/Python/3.12/bin/python"},
		{"linux", "/opt/Python/3.12/", "/opt/Python/3.13/bin/python", "/opt/Python/3.13/"},
		{"linux", "children:/opt/Python/3.12/", "/opt/Python/3.13/lib/x/helper", "/opt/Python/3.13/"},
		{"linux", "/opt/java/jdk-21/bin/java", "/opt/java/jdk-22/bin/java", "/opt/java/jdk-22/bin/java"},
		{"linux", "/opt/java/jdk-21/", "/opt/java/jdk-22/bin/java", "/opt/java/jdk-22/"},
		{"windows", `C:\Python\3.12\python.exe`, `C:\Python\3.13\python.exe`, `C:\Python\3.13\python.exe`},
		{"windows", `C:\Python\3.12\`, `c:/python/3.13/Scripts/pip.exe`, `c:/python/3.13/`},
		// Not the rule's path in another version.
		{"linux", "/opt/Python/3.12/bin/python", "/opt/Ruby/3.13/bin/python", ""},
		{"linux", "/opt/Python/3.12/bin/python", "/opt/Python/3.13/bin/pip", ""},
		{"linux", "/opt/Python/3.12/", "/opt/Python/3.13", ""},
		{"linux", "/opt/a1/b1/example", "/opt/a2/b2/example", ""},
		// Rules that name no version: one path, a name, an app by what installed it.
		{"linux", "exact:/opt/Python/3.12/bin/python", "/opt/Python/3.12/bin/python", ""},
		{"linux", "python", "/opt/Python/3.13/bin/python", ""},
		{"linux", "flatpak:org.example.App", "/app/bin/example", ""},
		// A Store app is its package, in any version.
		{"windows", `C:\Program Files\WindowsApps\OpenAI.ChatGPT_2p2nqsd0c76g0\`, `C:\Program Files\WindowsApps\OpenAI.ChatGPT_1.2025.1.0_x64__2p2nqsd0c76g0\ChatGPT.exe`, ""},
	} {
		if got := ruleVersion(tc.rule, tc.program, tc.goos); got != tc.want {
			t.Errorf("%s: rule %q, program %q: version %q, want %q", tc.goos, tc.rule, tc.program, got, tc.want)
		}
	}
}

// A rule that names a build is one that routing reads without it: only such a rule changes when
// it is made exact.
func TestRulesThatNameABuild(t *testing.T) {
	for _, tc := range []struct {
		goos, rule string
		want       bool
	}{
		{"linux", "/opt/Python/3.12/bin/python", true},
		{"linux", "/opt/Example/app-1.0.9265/", true},
		{"linux", "children:/opt/Example/app-1.0.9265/example", true},
		{"linux", "exact:/opt/Python/3.12/bin/python", true},
		{"linux", "/snap/example/41/bin/example", true},
		{"windows", `C:\Program Files (x86)\Battle.net\Battle.net.15173\Battle.net.exe`, true},
		{"linux", "/opt/java/jdk-21/bin/java", false},
		{"windows", `C:\Program Files (x86)\Steam\steam.exe`, false},
		{"darwin", "/Applications/Example.app/", false},
		{"linux", "python3.12", false},
		{"linux", "flatpak:org.example.App", false},
		{"windows", "service:Dnscache", false},
		{"linux", "-1", false},
		{"windows", `C:\Program Files\WindowsApps\OpenAI.ChatGPT_2p2nqsd0c76g0\`, false},
		{"windows", `C:\Program Files\WindowsApps\OpenAI.ChatGPT_1.2025.1.0_x64__2p2nqsd0c76g0\ChatGPT.exe`, false},
	} {
		if got := ruleNamesBuild(tc.rule, tc.goos); got != tc.want {
			t.Errorf("%s: rule %q names a build: %v, want %v", tc.goos, tc.rule, got, tc.want)
		}
	}
}

// The versions of a rule's path on this computer: itself first, then what routing reads as the
// same, spelled as the disk has them.
func TestRuleVersionsOnDisk(t *testing.T) {
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	at := func(parts ...string) string { return filepath.Join(append([]string{root}, parts...)...) }
	for _, version := range []string{"3.12", "3.13"} {
		if err := os.MkdirAll(at("Python", version, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(at("Python", version, "bin", "python"), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(at("Python", "3.14", "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	sep := string(filepath.Separator)
	for _, tc := range []struct {
		name, rule string
		want       []string
	}{
		{"its own first", at("Python", "3.13", "bin", "python"), []string{at("Python", "3.13", "bin", "python"), at("Python", "3.12", "bin", "python")}},
		{"a version that is gone", at("Python", "3.11", "bin", "python"), []string{at("Python", "3.12", "bin", "python"), at("Python", "3.13", "bin", "python")}},
		{"a folder", at("Python", "3.12") + sep, []string{at("Python", "3.12") + sep, at("Python", "3.13") + sep, at("Python", "3.14") + sep}},
		{"with the programs it starts", "children:" + at("Python", "3.12", "bin", "python"), []string{at("Python", "3.12", "bin", "python"), at("Python", "3.13", "bin", "python")}},
		{"exact", "exact:" + at("Python", "3.12", "bin", "python"), nil},
		{"a path without a version", at("Python"), []string{at("Python")}},
		{"nothing there", at("Ruby", "3.3", "bin", "ruby"), nil},
		{"a name", "python", nil},
	} {
		if got := RuleVersionsOnDisk(tc.rule); !slices.Equal(got, tc.want) {
			t.Errorf("%s: rule %q is on disk as %q, want %q", tc.name, tc.rule, got, tc.want)
		}
	}
}

// What a folder's name is without its numbers and hashes; empty for a name that has none.
func TestPathPartShapes(t *testing.T) {
	for _, tc := range [][2]string{
		{"jdk-21", "jdk-#"},
		{"jdk-21.0.2", "jdk-#"},
		{"Python312", "Python#"},
		{"app-4.42.0-beta1", "app-#"},
		{"app-4.43.0", "app-#"},
		{"1.2.3-rc.1", "#"},
		{"1.2.3-preview", "#"},
		{"23.1.0_1", "#"},
		{"2022.3.10f1", "#f#"},
		{"version-0a1b2c3d4e5f6a7b", "version-#"},
		{"1a5daa3a0231a0fbba4f14db7ec463cf99d7768e", "#"},
		{"Adobe Photoshop 2025", "Adobe Photoshop #"},
		{"Example (x86) 12", "Example (x#) #"},
		{"2024-01-15", "#"},
		{"Example", ""},
		{"deadbeef", ""},
		{"", ""},
	} {
		if got := PathPartShape(tc[0]); got != tc[1] {
			t.Errorf("PathPartShape(%q) = %q, want %q", tc[0], got, tc[1])
		}
	}
}

// The editor asks which saved rule an app in its list already is: the same rule written for
// another version, not only the same text.
func TestSameProcessRule(t *testing.T) {
	gone := false
	previous := rulePathGone
	rulePathGone = func(string, bool) bool { return gone }
	t.Cleanup(func() { rulePathGone = previous })
	for _, tc := range []struct {
		name, saved, app string
		gone, want       bool
	}{
		{"the same text", "/opt/Example/example", "/opt/Example/example", false, true},
		{"another version", "/opt/Example/app-1.0.9265/example", "/opt/Example/app-1.0.9270/example", false, true},
		{"another version's folder", "/opt/Example/app-1.0.9265/", "/opt/Example/app-1.0.9270/", false, true},
		{"with the programs it starts", "children:/opt/Example/1.2/example", "/opt/Example/1.3/example", false, true},
		{"a replaced folder", "/opt/java/jdk-21/bin/java", "/opt/java/jdk-22/bin/java", true, true},
		{"a replaced folder, as a folder", "/opt/java/jdk-21/", "/opt/java/jdk-22/", true, true},
		{"a folder beside its own", "/opt/java/jdk-21/bin/java", "/opt/java/jdk-22/bin/java", false, false},
		{"a folder and a program in it", "/opt/Example/", "/opt/Example/example", false, false},
		{"a program and its folder", "/opt/Example/example", "/opt/Example/", false, false},
		{"a bare name", "example", "example", false, true},
		{"a bare name and a path", "example", "/opt/Example/example", false, false},
		{"exact and its path", "exact:/opt/Example/1.2/example", "/opt/Example/1.2/example", false, true},
		{"exact and another version", "exact:/opt/Example/1.2/example", "/opt/Example/1.3/example", false, false},
		{"Flatpak", "flatpak:org.example.App", "flatpak:org.example.App", false, true},
		{"another Flatpak", "flatpak:org.example.App", "flatpak:org.example.Other", false, false},
		{"a service, in any case", "service:DoSvc", "service:dosvc", false, true},
		{"an AppImage and a path", "appimage:/opt/Example.AppImage", "/opt/Example.AppImage", false, false},
		{"unidentified traffic", "-1", "-1", false, true},
	} {
		gone = tc.gone
		if got := SameProcessRule(tc.saved, tc.app); got != tc.want {
			t.Errorf("%s: SameProcessRule(%q, %q) = %v, want %v", tc.name, tc.saved, tc.app, got, tc.want)
		}
	}
	if runtime.GOOS == "windows" {
		if !SameProcessRule(`C:\Example\Example.exe`, `c:\example\EXAMPLE.EXE`) || !SameProcessRule("Example.exe", "example") {
			t.Error("Windows rules compare without letter case")
		}
	}
}

// macOS runs a quarantined app from a copy in a folder it names anew at each start. All the copy
// keeps of where the app is, is its bundle, so the rule's bundle names it.
func TestTranslocatedAppsMatchTheirBundle(t *testing.T) {
	const run = "/private/var/folders/ab/cd_ef0123/T/AppTranslocation/6E2A9F0C-1B2D-4C3E-9F10-AABBCCDDEE01/d/Example.app/Contents/MacOS/Example"
	if got := translocatedPath(run); got != "Example.app/Contents/MacOS/Example" {
		t.Errorf("translocatedPath = %q", got)
	}
	if got := translocatedPath("/Applications/Example.app/Contents/MacOS/Example"); got != "" {
		t.Errorf("an app in its own place is translocated: %q", got)
	}
	for rule, want := range map[string]string{
		"/Users/me/Downloads/Example.app/":                          "Example.app/",
		"/Applications/Example.app/Contents/MacOS/Example":          "Example.app/Contents/MacOS/Example",
		"/Applications/Example.app/Contents/Frameworks/Helper.app/": "Example.app/Contents/Frameworks/Helper.app/",
		"/usr/bin/curl":    "",
		"/opt/Example/":    "",
		"/opt/.app/tool":   "",
		"/opt/Example.app": "Example.app",
	} {
		if got := bundleTail(rule); got != want {
			t.Errorf("bundleTail(%q) = %q, want %q", rule, got, want)
		}
	}
	if runtime.GOOS != "darwin" {
		return
	}
	for _, tc := range []struct {
		name, rule string
		want       bool
	}{
		{"the bundle where it was downloaded", "/Users/me/Downloads/Example.app/", true},
		{"a program in the bundle", "/Applications/Example.app/Contents/MacOS/Example", true},
		{"saved from an earlier start", "/private/var/folders/ab/cd_ef0123/T/AppTranslocation/11111111-2222-3333-4444-555555555555/d/Example.app/", true},
		{"another bundle", "/Applications/Other.app/", false},
		{"another program in the bundle", "/Applications/Example.app/Contents/MacOS/Helper", false},
		{"exact", "exact:/Users/me/Downloads/Example.app/", false},
	} {
		if got := NewProcessNameMatcher([]string{tc.rule}).MatchesProcess(77, "Example", run); got != tc.want {
			t.Errorf("%s: rule %q matched the translocated app: %v, want %v", tc.name, tc.rule, got, tc.want)
		}
	}
}

// A Microsoft Store app's rule names its package family. Windows tells the family of a process,
// so the app's processes match wherever they run from: System32's WWAHost.exe hosts some apps,
// backgroundTaskHost.exe their background tasks.
func TestStoreAppsMatchByTheirProcessPackage(t *testing.T) {
	for key, want := range map[string]string{
		"c:/program files/windowsapps/openai.chatgpt-desktop_2p2nqsd0c76g0/":                "openai.chatgpt-desktop_2p2nqsd0c76g0",
		"d:/windowsapps/claude_pzs8sxrjxfjjc/":                                              "claude_pzs8sxrjxfjjc",
		"c:/program files/windowsapps/openai.chatgpt-desktop_2p2nqsd0c76g0/app/":            "",
		"c:/program files/windowsapps/openai.chatgpt-desktop_2p2nqsd0c76g0/app/chatgpt.exe": "",
		"c:/program files/windowsapps/":                                                     "",
		"c:/program files/example/":                                                         "",
	} {
		if got := packageFamilyFolder(key); got != want {
			t.Errorf("packageFamilyFolder(%q) = %q, want %q", key, got, want)
		}
	}
	previous := processPackage
	asked := 0
	processPackage = func(pid int) string {
		asked++
		if pid == 77 {
			return "OpenAI.ChatGPT-Desktop_2p2nqsd0c76g0"
		}
		return ""
	}
	t.Cleanup(func() { processPackage = previous })
	matcher := &ProcessNameMatcher{Packages: []string{"openai.chatgpt-desktop_2p2nqsd0c76g0"}}
	if !matcher.MatchesProcess(77, "WWAHost.exe", `C:\Windows\System32\WWAHost.exe`) {
		t.Error("a process of the package did not match")
	}
	if matcher.MatchesProcess(78, "WWAHost.exe", `C:\Windows\System32\WWAHost.exe`) {
		t.Error("another app's host matched")
	}
	asked = 0
	if (&ProcessNameMatcher{ProcessNames: []string{"example"}}).MatchesProcess(77, "other", "/opt/other"); asked != 0 {
		t.Error("Windows was asked for a package without a package rule")
	}
}

// A burst of connections of a program the rule may follow asks the disk once: the first answer is
// waited for, and while a later one is being fetched the others take the answer that stands.
func TestABurstOfConnectionsAsksTheDiskOnce(t *testing.T) {
	var asked atomic.Int32
	release := make(chan struct{})
	previous := rulePathGone
	rulePathGone = func(string, bool) bool { asked.Add(1); <-release; return true }
	t.Cleanup(func() { rulePathGone = previous })

	matcher := NewProcessNameMatcher([]string{"/opt/java/jdk-21/bin/java"})
	var followed atomic.Int32
	burst := func() *sync.WaitGroup {
		var group sync.WaitGroup
		for range 16 {
			group.Add(1)
			go func() {
				defer group.Done()
				if matcher.MatchesProcess(77, "java", "/opt/java/jdk-22/bin/java") {
					followed.Add(1)
				}
			}()
		}
		return &group
	}
	first := burst()
	for asked.Load() == 0 {
		runtime.Gosched()
	}
	close(release)
	first.Wait()
	if asked.Load() != 1 || followed.Load() != 16 {
		t.Fatalf("the first burst asked the disk %d times and followed %d of 16", asked.Load(), followed.Load())
	}

	// The answer is stale: one caller asks again, the others do not wait for it.
	for _, rule := range matcher.followers {
		rule.asked.Store(1)
	}
	release = make(chan struct{})
	followed.Store(0)
	second := burst()
	for asked.Load() == 1 {
		runtime.Gosched()
	}
	for followed.Load() != 15 {
		runtime.Gosched()
	}
	close(release)
	second.Wait()
	if asked.Load() != 2 || followed.Load() != 16 {
		t.Errorf("the second burst asked the disk %d times in all and followed %d of 16", asked.Load(), followed.Load())
	}
}
