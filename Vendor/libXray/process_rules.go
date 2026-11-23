package libXray

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"unicode"

	"github.com/xtls/xray-core/app/router"
	xnet "github.com/xtls/xray-core/common/net"
)

// Elarni: the per-app editor asks which rules match running programs and why a rule may match
// nothing. Counts use the core's own matcher; file checks report only what they could read.

type ProcessRuleCheckRequest struct {
	Rules []string `json:"rules"`
	// The rules the editor's app list gives its apps: the answer tells which of them a saved
	// rule already is.
	Apps []string `json:"apps,omitempty"`
}

type ProcessRuleWarning struct {
	// missing, moved, folder, notFolder, script, symlink, case, launcher or bundleName. Flatpak,
	// AppImage and service rules can be missing; an AppImage or exact rule's path can also need
	// a fix.
	Code       string `json:"code"`
	Suggestion string `json:"suggestion,omitempty"`
}

// ProcessRuleVersion is one of the paths a rule names on this computer: the rule's own, the
// same in another build of its app, or in another version installed beside it.
type ProcessRuleVersion struct {
	// The program for a rule that names one, the folder for a folder rule.
	Path string `json:"path"`
	// The programs that run from it and that the rule's own path takes, not the ones they start.
	Running int `json:"running"`
	// The rule's own path.
	Own bool `json:"own,omitempty"`
}

type ProcessRuleCheck struct {
	Rule     string               `json:"rule"`
	Running  int                  `json:"running"`
	Warnings []ProcessRuleWarning `json:"warnings"`
	// What the rule names here, when that is not its own path alone: its own first. The editor
	// tells a rule that takes two versions at once, and offers it for one path ("exact:").
	Versions []ProcessRuleVersion `json:"versions,omitempty"`
	// The rule names a build, which routing reads it without: made exact, it stops following
	// its app's updates. Any other rule is the same either way.
	Follows bool `json:"follows,omitempty"`
}

type ProcessRuleCheckResponse struct {
	Rules []ProcessRuleCheck `json:"rules"`
	// False when the running programs could not be listed; the counts are then 0.
	Listed bool `json:"listed"`
	// For each of the request's apps that a saved rule names, that rule: the app's own rule, or
	// one written for another version of it. The editor compares no rule text itself.
	Same map[string]string `json:"same,omitempty"`
}

func invokeCheckProcessRules(payload json.RawMessage) string {
	request, err := decodePayload[ProcessRuleCheckRequest](payload)
	if err != nil || !validProcessRules(request.Rules, 256) || !validProcessRules(request.Apps, 4096) {
		return encodeInvokeResponse(nil, errors.New("invalid_rules"))
	}
	response := checkProcessRules(request.Rules)
	response.Same = sameProcessRules(request.Rules, request.Apps)
	return encodeInvokeResponse(response, nil)
}

// sameProcessRules maps each app's rule to the first saved rule that names the same thing.
func sameProcessRules(rules, apps []string) map[string]string {
	same := map[string]string{}
	for _, app := range apps {
		for _, rule := range rules {
			if router.SameProcessRule(rule, app) {
				same[app] = rule
				break
			}
		}
	}
	return same
}

// The same bounds as per-app settings: 256 rules of 1024 bytes without control characters. An
// app list is longer.
func validProcessRules(rules []string, most int) bool {
	if len(rules) > most {
		return false
	}
	for _, rule := range rules {
		if rule == "" || len(rule) > 1024 || strings.TrimSpace(rule) != rule ||
			strings.IndexFunc(rule, unicode.IsControl) >= 0 {
			return false
		}
	}
	return true
}

// listProcesses is swapped by tests to name the running programs.
var listProcesses = xnet.ListProcesses

func checkProcessRules(rules []string) ProcessRuleCheckResponse {
	processes, err := listProcesses()
	response := ProcessRuleCheckResponse{Rules: make([]ProcessRuleCheck, 0, len(rules)), Listed: err == nil}
	for _, rule := range rules {
		check := ProcessRuleCheck{Rule: rule, Warnings: processRuleWarnings(rule)}
		if rule != "-1" {
			matcher := router.NewProcessNameMatcher([]string{rule})
			// The programs a "children:" rule's program starts are not versions of it.
			own := matcher
			if inner, ok := strings.CutPrefix(rule, "children:"); ok {
				own = router.NewProcessNameMatcher([]string{inner})
			}
			versions := ruleVersions{rule: rule}
			// The disk is asked only where the warnings ask it: not on a network drive.
			if localRule(strings.TrimPrefix(rule, "children:")) {
				for _, path := range router.RuleVersionsOnDisk(rule) {
					versions.add(path, 0)
				}
			}
			for _, process := range processes {
				if !matcher.MatchesProcess(process.PID, process.Name, process.Path) {
					continue
				}
				check.Running++
				if own != matcher && !own.MatchesProcess(process.PID, process.Name, process.Path) {
					continue
				}
				if version := router.RuleVersion(rule, process.Path); version != "" {
					versions.add(version, 1)
				}
			}
			check.Versions = versions.told()
			check.Follows = router.RuleNamesBuild(rule)
		}
		if len(check.Warnings) == 1 && check.Warnings[0].Code == "missing" {
			// What runs is here, wherever the rule's path points: routing compares paths without
			// version numbers, and this check may not see every folder.
			if check.Running > 0 {
				check.Warnings = nil
			} else if moved := movedProgram(rule, processes); moved != "" {
				check.Warnings = []ProcessRuleWarning{{Code: "moved", Suggestion: moved}}
			}
		}
		response.Rules = append(response.Rules, check)
	}
	return response
}

// localRule reports a rule's path that the check may look for on the disk (localRulePath). Tests
// replace it.
var localRule = localRulePath

// ruleVersions collects the paths a rule names on this computer, in the order they were found.
type ruleVersions struct {
	rule  string
	found []ProcessRuleVersion
}

// written is a path as paths compare on this system, whichever way its folders are separated.
func (v *ruleVersions) written(path string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	}
	return path
}

func (v *ruleVersions) add(path string, running int) {
	written := v.written(path)
	for i := range v.found {
		if v.written(v.found[i].Path) == written {
			v.found[i].Running += running
			return
		}
	}
	own := written == v.written(strings.TrimPrefix(v.rule, "children:"))
	v.found = append(v.found, ProcessRuleVersion{Path: path, Running: running, Own: own})
}

// told returns the versions when they tell more than the rule does: nothing for a rule that
// names its own path only. The rule's own path is first and the rest are in their paths' order.
func (v *ruleVersions) told() []ProcessRuleVersion {
	if len(v.found) == 0 || len(v.found) == 1 && v.found[0].Own {
		return nil
	}
	slices.SortStableFunc(v.found, func(a, b ProcessRuleVersion) int {
		if a.Own != b.Own {
			if a.Own {
				return -1
			}
			return 1
		}
		return strings.Compare(v.written(a.Path), v.written(b.Path))
	})
	return v.found
}

func processRuleWarnings(rule string) []ProcessRuleWarning {
	warn := func(code, suggestion string) []ProcessRuleWarning {
		return []ProcessRuleWarning{{Code: code, Suggestion: suggestion}}
	}
	// "children:" also takes the programs a rule's program starts; the rule is checked as is.
	if inner, ok := strings.CutPrefix(rule, "children:"); ok {
		if inner == "" || inner == "-1" || strings.HasPrefix(inner, "children:") {
			return nil
		}
		warnings := processRuleWarnings(inner)
		for i := range warnings {
			if warnings[i].Suggestion != "" {
				warnings[i].Suggestion = "children:" + warnings[i].Suggestion
			}
		}
		return warnings
	}
	// Rules of another system's kind match nothing here; the editor refuses them.
	if id, ok := strings.CutPrefix(rule, "flatpak:"); ok {
		if runtime.GOOS != "linux" || flatpakInstalled(id) {
			return nil
		}
		return warn("missing", "")
	}
	if image, ok := strings.CutPrefix(rule, "appimage:"); ok {
		if runtime.GOOS != "linux" || !strings.HasPrefix(image, "/") {
			return nil
		}
		// The image is a file the app runs from; its suggestions stay AppImage rules.
		warnings := processRuleWarnings(image)
		for i := range warnings {
			if warnings[i].Code == "script" {
				return nil
			}
			if warnings[i].Suggestion != "" {
				warnings[i].Suggestion = "appimage:" + warnings[i].Suggestion
			}
		}
		return warnings
	}
	if service, ok := strings.CutPrefix(rule, "service:"); ok {
		if runtime.GOOS != "windows" || serviceInstalled(service) {
			return nil
		}
		return warn("missing", "")
	}
	// "exact:" names its path alone, not what an update put beside it or in its place.
	rule, exact := strings.CutPrefix(rule, "exact:")
	if exact {
		warn = func(code, suggestion string) []ProcessRuleWarning {
			if suggestion != "" {
				suggestion = "exact:" + suggestion
			}
			return []ProcessRuleWarning{{Code: code, Suggestion: suggestion}}
		}
	}
	windows := runtime.GOOS == "windows"
	slashed := rule
	if windows {
		slashed = strings.ReplaceAll(rule, `\`, "/")
	}
	if rule == "-1" || !strings.Contains(slashed, "/") {
		lower := strings.ToLower(rule)
		switch {
		case runtime.GOOS == "darwin" && strings.HasSuffix(lower, ".app"):
			return warn("bundleName", existingDir(filepath.Join("/Applications", rule), "/"))
		case windows && windowsLauncher(lower):
			return warn("launcher", "")
		}
		return nil
	}
	if !localRule(rule) {
		return nil
	}
	sep := string(filepath.Separator)
	folder := strings.HasSuffix(slashed, "/")
	suffix := "" // a suggestion for a folder rule stays a folder rule
	if folder {
		suffix = sep
	}
	path := filepath.FromSlash(strings.TrimSuffix(slashed, "/"))
	info, err := ruleLstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		if real := realCase(path); real != "" && !windows {
			return warn("case", real+suffix)
		}
		// The rule also names the app's other versions; the checks below are of the one on disk.
		if !exact {
			path, info, err = otherVersion(path, folder)
		}
	}
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil // unreadable here, as from the App Sandbox
		}
		return warn("missing", "")
	}
	if !windows {
		// macOS disks ignore case, but rules compare it.
		if real := realCase(path); real != "" && real != path {
			return warn("case", real+suffix)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			// The process is the link's target; macOS and Linux report that path.
			if target, err := filepath.EvalSymlinks(path); err == nil {
				return warn("symlink", target+suffix)
			}
			return nil
		}
	}
	switch {
	case folder && !info.IsDir():
		return warn("notFolder", path)
	case folder:
		return nil
	case info.IsDir():
		// Without the separator the rule is an exact path, which no process has.
		return warn("folder", path+sep)
	case windows && windowsLauncher(strings.ToLower(path)):
		return warn("launcher", "")
	case windows:
		if root := squirrelRoot(path); root != "" {
			return warn("launcher", root+sep)
		}
	case script(path):
		return warn("script", "")
	}
	return nil
}

// ruleLstat is swapped by tests to see which paths a check opens.
var ruleLstat = os.Lstat

// otherVersion finds what a path that is not on disk names for routing: the same path through
// the folders of another version, which routing compares without what names a build
// (router.ProcessPathKey), and, when there is none, through one folder that replaced the rule's
// own, with the same name but for its numbers (router.PathPartShape). Routing follows a renamed
// folder only when no build of what the rule names is left, so that is looked for first, and a
// search that could not finish is not followed by the other. The error is fs.ErrNotExist when
// there is none, and another when a folder on the way could not be read, so the answer is
// unknown.
func otherVersion(path string, folder bool) (string, fs.FileInfo, error) {
	sep := string(filepath.Separator)
	volume := filepath.VolumeName(path)
	parts := strings.FieldsFunc(path[len(volume):], func(r rune) bool { return r == filepath.Separator })
	if len(parts) == 0 {
		return "", nil, fs.ErrNotExist
	}
	const most = 32 // folders listed for one rule, as routing lists (rulePathListings)
	listings := most
	var unreadable error
	shape := func(name string) string {
		if runtime.GOOS == "windows" {
			name = strings.ToLower(name)
		}
		return router.PathPartShape(name)
	}
	// replaced tells that a folder on the way already stands for one of the rule's.
	var find func(dir string, parts []string, replaced bool) (string, fs.FileInfo)
	find = func(dir string, parts []string, replaced bool) (string, fs.FileInfo) {
		last := len(parts) == 1
		follow := func(candidate string, replaced bool) (string, fs.FileInfo) {
			info, err := ruleLstat(candidate)
			switch {
			case err != nil && !errors.Is(err, fs.ErrNotExist):
				unreadable = err
			case err == nil && last:
				return candidate, info
			case err == nil:
				return find(candidate, parts[1:], replaced)
			}
			return "", nil
		}
		named := filepath.Join(dir, parts[0])
		if found, info := follow(named, replaced); info != nil {
			return found, info
		}
		if listings == 0 {
			return "", nil
		}
		listings--
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				unreadable = err
			}
			return "", nil
		}
		// A version number ends a folder's name, not a file's.
		end := sep
		if last && !folder {
			end = ""
		}
		key := router.ProcessPathKey(named + end)
		var others []string
		for _, entry := range entries {
			candidate := filepath.Join(dir, entry.Name())
			if entry.Name() == parts[0] {
				continue
			}
			if router.ProcessPathKey(candidate+end) != key {
				if named := shape(parts[0]); !replaced && named != "" && named == shape(entry.Name()) {
					others = append(others, candidate)
				}
				continue
			}
			if found, info := follow(candidate, replaced); info != nil {
				return found, info
			}
		}
		for _, candidate := range others {
			if found, info := follow(candidate, true); info != nil {
				return found, info
			}
		}
		return "", nil
	}
	for _, replaced := range []bool{true, false} {
		if found, info := find(volume+sep, parts, replaced); info != nil {
			return found, info, nil
		}
		if unreadable != nil {
			return "", nil, unreadable
		}
		if listings == 0 {
			break
		}
		listings = most
	}
	return "", nil, fs.ErrNotExist
}

// movedProgram returns the running program that a rule's missing program seems to have become:
// the only one with its file name in the same folders but one, such as a folder that an update
// names by a build's hash. Empty for a folder rule or a rule that is not a path.
func movedProgram(rule string, processes []xnet.Process) string {
	kind := ""
	if inner, ok := strings.CutPrefix(rule, "children:"); ok {
		kind, rule = "children:", inner
	}
	windows := runtime.GOOS == "windows"
	compared := func(path string) []string {
		if windows {
			path = strings.ToLower(strings.ReplaceAll(path, `\`, "/"))
		}
		return strings.Split(path, "/")
	}
	for _, typed := range []string{"flatpak:", "appimage:", "service:", "exact:"} {
		if strings.HasPrefix(rule, typed) {
			return ""
		}
	}
	want := compared(rule)
	if len(want) < 3 || want[len(want)-1] == "" {
		return ""
	}
	moved := ""
	for _, process := range processes {
		got := compared(process.Path)
		if len(got) != len(want) || got[len(got)-1] != want[len(want)-1] {
			continue
		}
		renamed := 0
		for i := range got {
			if got[i] != want[i] {
				renamed++
			}
		}
		if renamed != 1 {
			continue
		}
		if moved != "" && !slices.Equal(compared(moved), got) {
			return "" // two programs could be it
		}
		moved = process.Path
	}
	if moved == "" {
		return ""
	}
	return kind + filepath.FromSlash(moved)
}

// flatpakInstallations lists the Flatpak installations: the user's and the system's.
var flatpakInstallations = func() []string {
	var dirs []string
	if dir := os.Getenv("FLATPAK_USER_DIR"); dir != "" {
		dirs = append(dirs, dir)
	} else if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".local", "share", "flatpak"))
	}
	if dir := os.Getenv("FLATPAK_SYSTEM_DIR"); dir != "" {
		return append(dirs, dir)
	}
	return append(dirs, "/var/lib/flatpak")
}

// flatpakInstalled reports an app installed in a Flatpak installation, or one this check cannot
// see.
func flatpakInstalled(id string) bool {
	if !xnet.ValidFlatpakApp(id) {
		return false
	}
	for _, dir := range flatpakInstallations() {
		if _, err := os.Stat(filepath.Join(dir, "app", id)); !errors.Is(err, fs.ErrNotExist) {
			return true
		}
	}
	return false
}

func windowsLauncher(name string) bool {
	for _, extension := range []string{".lnk", ".url", ".bat", ".cmd", ".ps1", ".vbs"} {
		if strings.HasSuffix(name, extension) {
			return true
		}
	}
	return false
}

// squirrelRoot returns the install folder when path is a Squirrel app's root stub (Discord,
// Slack): <root>\Update.exe and <root>\app-<version>\ beside <root>\<name>.exe.
func squirrelRoot(path string) string {
	root := filepath.Dir(path)
	if !strings.HasSuffix(strings.ToLower(path), ".exe") || strings.EqualFold(filepath.Base(path), "Update.exe") {
		return ""
	}
	if info, err := os.Stat(filepath.Join(root, "Update.exe")); err != nil || info.IsDir() {
		return ""
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(strings.ToLower(entry.Name()), "app-") {
			return root
		}
	}
	return ""
}

// script reports a file that starts with "#!": the running process is its interpreter.
func script(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	head := make([]byte, 2)
	_, err = io.ReadFull(file, head)
	return err == nil && string(head) == "#!"
}

func existingDir(path, suffix string) string {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return path + suffix
	}
	return ""
}

// realCase spells an absolute path as the disk does, or returns "" when no entry matches in
// any letter case or a folder cannot be read.
func realCase(path string) string {
	if !filepath.IsAbs(path) {
		return ""
	}
	volume := filepath.VolumeName(path)
	real := volume + string(filepath.Separator)
	for _, part := range strings.Split(strings.Trim(path[len(volume):], string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		entries, err := os.ReadDir(real)
		if err != nil {
			return ""
		}
		found := ""
		for _, entry := range entries {
			if entry.Name() == part {
				found = part
				break
			}
			if found == "" && strings.EqualFold(entry.Name(), part) {
				found = entry.Name()
			}
		}
		if found == "" {
			return ""
		}
		real = filepath.Join(real, found)
	}
	return real
}
