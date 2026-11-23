package router

import (
	"path"
	"testing"
)

// Elarni: an update moves many apps to a new path: Squirrel's app-<version>, Edge WebView2's
// Application\<version>, Battle.net.<build>, Adobe's yearly folders, a Snap's revision. A rule
// saved for one version stopped matching, app by app. Paths compare without their version numbers.
func TestPathsCompareWithoutVersionNumbers(t *testing.T) {
	for _, tc := range []struct{ goos, path, want string }{
		{"windows", `C:\Users\me\AppData\Local\Discord\app-1.0.9265\Discord.exe`, "c:/users/me/appdata/local/discord/app-*/discord.exe"},
		{"windows", "c:/users/me/appdata/local/discord/app-1.0.9270/discord.exe", "c:/users/me/appdata/local/discord/app-*/discord.exe"},
		{"windows", "c:/program files (x86)/microsoft/edgewebview/application/131.0.2903.86/msedgewebview2.exe",
			"c:/program files (x86)/microsoft/edgewebview/application/*/msedgewebview2.exe"},
		{"windows", "c:/program files (x86)/battle.net/battle.net.15173/battle.net.exe", "c:/program files (x86)/battle.net/battle.net.*/battle.net.exe"},
		{"windows", "c:/program files/adobe/adobe photoshop 2025/photoshop.exe", "c:/program files/adobe/adobe photoshop */photoshop.exe"},
		{"windows", "c:/program files/java/jdk-21.0.2/bin/java.exe", "c:/program files/java/jdk-*/bin/java.exe"},
		{"windows", "c:/tools/rufus-4.6.exe", "c:/tools/rufus-*.exe"},
		{"windows", "c:/program files/unity/hub/editor/2022.3.10f1/editor/unity.exe", "c:/program files/unity/hub/editor/*f1/editor/unity.exe"},
		// A Store package still compares as its family, whatever its version and architecture.
		{"windows", `C:\Program Files\WindowsApps\OpenAI.ChatGPT-Desktop_1.2025.112.0_x64__2p2nqsd0c76g0\app\ChatGPT.exe`,
			"c:/program files/windowsapps/openai.chatgpt-desktop_2p2nqsd0c76g0/app/chatgpt.exe"},
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
	} {
		if got := NewProcessNameMatcher([]string{tc.rule}).MatchesProcess(77, path.Base(tc.path), tc.path); got != tc.want {
			t.Errorf("%s: rule %q matched %q: %v, want %v", tc.name, tc.rule, tc.path, got, tc.want)
		}
	}
}
