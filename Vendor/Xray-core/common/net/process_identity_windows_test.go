//go:build windows

package net

import (
	"os"
	"strings"
	"testing"
)

// svchost runs most Windows services; a service rule names the process that runs it, and a
// connection from a host of one service is named after that service.
func TestServicesOfARunningProcess(t *testing.T) {
	byPID, err := listServices()
	if err != nil {
		t.Fatal(err)
	}
	processes, err := ListProcesses()
	if err != nil {
		t.Fatal(err)
	}
	hosts := map[int]bool{}
	for _, process := range processes {
		if svchostName(process.Name) { // the list names programs without ".exe"
			hosts[process.PID] = true
		}
	}
	single, shared := 0, 0
	for pid, names := range byPID {
		if !hosts[int(pid)] {
			continue
		}
		got := ProcessServices(int(pid), "svchost.exe")
		for _, name := range names {
			found := false
			for _, service := range got {
				found = found || strings.EqualFold(service, name)
			}
			if !found {
				t.Fatalf("pid %d runs %s, lookup %v", pid, name, got)
			}
		}
		rule := AppRule(int(pid), "svchost.exe", `C:\Windows\System32\svchost.exe`)
		if len(names) == 1 {
			single++
			if !strings.EqualFold(rule, "service:"+names[0]) {
				t.Fatalf("pid %d runs %s, rule %q", pid, names[0], rule)
			}
		} else {
			shared++
			// A rule for one of several services would take the others' connections too.
			if rule != "" {
				t.Fatalf("pid %d runs %v, rule %q", pid, names, rule)
			}
		}
	}
	if single == 0 {
		t.Fatalf("no svchost runs one service: %v", byPID)
	}
	t.Logf("%d svchost processes run one service, %d run several", single, shared)
	if got := ProcessServices(os.Getpid(), "test.exe"); len(got) != 0 {
		t.Fatalf("this test runs %v", got)
	}
	if got := AppRule(os.Getpid(), "test.exe", `C:\test.exe`); got != "" {
		t.Fatalf("this test: %q", got)
	}
}

// Windows tells the package a process runs as. A program without one has no family; a Store
// app's process has the family its folder names, and the rule the app list gives that app.
func TestPackageFamilyOfProcesses(t *testing.T) {
	if family := PackageFamily(os.Getpid()); family != "" {
		t.Errorf("this test has the package family %q", family)
	}
	if rule := AppRule(os.Getpid(), "net.test.exe", `C:\Windows\System32\net.test.exe`); rule != "" {
		t.Errorf("a program without a package is named %q", rule)
	}
	processes, err := ListProcesses()
	if err != nil {
		t.Fatal(err)
	}
	const parent, system = `\windowsapps\`, `\windows\systemapps\`
	checked, parts := 0, 0
	for _, process := range processes {
		lower := strings.ToLower(strings.ReplaceAll(process.Path, "/", `\`))
		// Windows's own packaged programs (the Start menu, Search) run from a folder named by
		// their family, or by the end of it (ShellExperienceHost_<publisher> is
		// Microsoft.Windows.ShellExperienceHost_<publisher>), and get no rule: their path
		// names them.
		if at := strings.Index(lower, system); at >= 0 {
			folder, _, _ := strings.Cut(lower[at+len(system):], `\`)
			if family := PackageFamily(process.PID); family != "" {
				if !strings.HasSuffix(strings.ToLower(family), folder) {
					t.Errorf("%s: package family %q, want one ending in %q", process.Path, family, folder)
				}
				if rule := AppRule(process.PID, process.Name, process.Path); rule != "" {
					t.Errorf("%s: named by the rule %q", process.Path, rule)
				}
				parts++
			}
			continue
		}
		at := strings.Index(lower, parent)
		if at < 0 {
			continue
		}
		folder, _, _ := strings.Cut(lower[at+len(parent):], `\`)
		fields := strings.Split(folder, "_")
		family := PackageFamily(process.PID)
		if len(fields) != 5 || family == "" {
			continue // exited, or not this user's to open
		}
		want := fields[0] + "_" + fields[4]
		if strings.ToLower(family) != want {
			t.Errorf("%s: package family %q, want %q", process.Path, family, want)
		}
		if rule := packageRule(process.PID); strings.ToLower(rule) != lower[:at+len(parent)]+want+`\` {
			t.Errorf("%s: package rule %q", process.Path, rule)
		}
		checked++
	}
	t.Logf("checked %d Store app processes and %d packaged parts of Windows", checked, parts)
}

// The process list names a program with forward slashes, the Windows directory comes with
// backslashes: a program of Windows is one either way, and nothing beside the directory is.
func TestProgramOfWindowsTakesEitherSeparator(t *testing.T) {
	for path, want := range map[string]bool{
		`C:/Windows/System32/WWAHost.exe`:   true,
		`C:\Windows\System32\WWAHost.exe`:   true,
		`c:/windows/system32/wwahost.exe`:   true,
		`C:/WindowsApps/WWAHost.exe`:        false,
		`C:/Windows`:                        false,
		`D:/Windows/System32/WWAHost.exe`:   false,
		`C:/Users/me/Windows/x/WWAHost.exe`: false,
	} {
		if got := programOfWindows(`C:\Windows`, path); got != want {
			t.Errorf("%s: %v, want %v", path, got, want)
		}
	}
}
