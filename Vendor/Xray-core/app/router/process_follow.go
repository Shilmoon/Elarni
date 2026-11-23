package router

import (
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

// Elarni: what tells two versions of an app from two products is not always in their paths:
// jdk-21 and jdk-22, Python312 and Python313, "Photoshop 2025" and "Photoshop 2026" are the next
// version, "Anno 1800" and "Anno 2070" are two games. The disk tells: a rule that names nothing on
// this computer anymore names what replaced it, the same path through one folder whose name
// differs only in its numbers and hashes. While what it names is there, in any build, a rule
// names only that (what ProcessPathKey gives it).

// pathRule is a path rule as it was written.
type pathRule struct {
	path   string // forward slashes, lowercase on Windows, without the folder rule's last slash
	folder bool
	keys   []string // its key, by folder
	parts  []string // its path, by folder

	asked atomic.Int64 // when the disk was last asked, in Unix nanoseconds
	gone  atomic.Bool
}

// rulePathFresh is how long an answer of the disk about a rule's path stands.
const rulePathFresh = 15 * time.Second

// rulePathListings is how many folders are listed to look for one rule's path.
const rulePathListings = 32

// rulePathGone reports a rule's path, as normalPath spelled it, that is nowhere on this computer
// as rules compare paths: neither itself nor through the folders of another build of what it
// names (ProcessPathKey). An app whose folders name its builds loses a rule's own path at every
// update, which its key follows, so that alone tells nothing: a rule for one channel of an app
// would take the channel installed beside it. A path that could not be read, or that took too
// many folders to look for, is not gone. Tests replace it.
var rulePathGone = func(path string, folder bool) bool {
	root, rest, _ := strings.Cut(path, "/")
	if rest == "" {
		return false
	}
	listings, unknown := rulePathListings, false
	var found func(dir string, parts []string) bool
	found = func(dir string, parts []string) bool {
		last := len(parts) == 1
		at := func(candidate string) bool {
			_, err := os.Lstat(candidate)
			switch {
			case err == nil:
				return last || found(candidate+"/", parts[1:])
			case !os.IsNotExist(err):
				unknown = true
			}
			return false
		}
		named := dir + parts[0]
		if at(named) || unknown {
			return !unknown
		}
		if listings == 0 {
			unknown = true
			return false
		}
		listings--
		entries, err := os.ReadDir(dir)
		if err != nil {
			unknown = !os.IsNotExist(err)
			return false
		}
		// A version number ends a folder's name, not a file's.
		end := "/"
		if last && !folder {
			end = ""
		}
		key := normalPathKey(named+end, runtime.GOOS)
		for _, entry := range entries {
			candidate := dir + normalPath(entry.Name(), runtime.GOOS)
			if candidate == named || normalPathKey(candidate+end, runtime.GOOS) != key {
				continue
			}
			if at(candidate) || unknown {
				return !unknown
			}
		}
		return false
	}
	return !found(root+"/", strings.Split(rest, "/")) && !unknown
}

// newPathRule takes a rule's path as normalPath spelled it and its key; nil for a path that is
// not absolute, which the disk cannot be asked about, and for one on another computer, which
// routing does not wait for.
func newPathRule(path, key string) *pathRule {
	if strings.HasPrefix(path, "//") || !strings.HasPrefix(path, "/") && !(len(path) > 2 && path[1] == ':' && path[2] == '/') {
		return nil
	}
	folder := strings.HasSuffix(path, "/")
	path, key = strings.TrimSuffix(path, "/"), strings.TrimSuffix(key, "/")
	rule := &pathRule{path: path, folder: folder, keys: strings.Split(key, "/"), parts: strings.Split(path, "/")}
	if len(rule.keys) != len(rule.parts) {
		return nil
	}
	return rule
}

// follows reports a program, by its key's and its path's folders, that runs from the folders the
// rule names but one, whose name has the rule's shape, when what the rule names is gone
// (rulePathGone). A folder rule takes what is inside; same takes only the path of the rule's own
// depth.
func (r *pathRule) follows(keys, parts []string, same bool) bool {
	n := len(r.keys)
	if len(keys) != len(parts) || len(keys) < n || (len(keys) == n) == (r.folder && !same) {
		return false
	}
	at := -1
	for i := range n {
		if r.keys[i] != keys[i] {
			if at >= 0 {
				return false
			}
			at = i
		}
	}
	if at < 0 {
		return false
	}
	if shape := PathPartShape(r.parts[at]); shape == "" || shape != PathPartShape(parts[at]) {
		return false
	}
	now := time.Now().UnixNano()
	if asked := r.asked.Load(); asked == 0 || now-asked > int64(rulePathFresh) {
		r.gone.Store(rulePathGone(r.path, r.folder))
		r.asked.Store(now)
	}
	return r.gone.Load()
}

var (
	joinedNumbers = regexp.MustCompile(`#(?:[-+_]#)+`)
	preRelease    = regexp.MustCompile(`(?i)#[-._ ]?(?:alpha|beta|rc|pre|preview|dev|canary|nightly|insiders|snapshot)(?:[-._]?#|$)`)
)

// PathPartShape returns a folder's or a file's name with each number and build hash in it
// replaced by "#", and a pre-release's suffix dropped: "jdk-#" for jdk-21 and jdk-21.0.2,
// "app-#" for app-4.42.0-beta1. Empty for a name without any, which names no version.
func PathPartShape(name string) string {
	alnum := func(c byte) bool {
		return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
	}
	digit := func(i int) bool { return i < len(name) && name[i] >= '0' && name[i] <= '9' }
	var shape strings.Builder
	found := false
	for i := 0; i < len(name); {
		if i == 0 || !alnum(name[i-1]) {
			if end := i + buildHash(name[i:]); end > i && (end == len(name) || !alnum(name[end])) {
				shape.WriteByte('#')
				found = true
				i = end
				continue
			}
		}
		if !digit(i) {
			shape.WriteByte(name[i])
			i++
			continue
		}
		for digit(i) {
			i++
		}
		for i < len(name) && (name[i] == '.' || name[i] == '_') && digit(i+1) {
			for i++; digit(i); i++ {
			}
		}
		shape.WriteByte('#')
		found = true
	}
	if !found {
		return ""
	}
	result := joinedNumbers.ReplaceAllString(shape.String(), "#")
	result = preRelease.ReplaceAllString(result, "#")
	return joinedNumbers.ReplaceAllString(result, "#")
}

// buildHash returns the length of the hexadecimal build hash that text starts with, or 0: eight
// or more characters that change between digits and letters at least twice, which a word, a
// number, a year after a word ("decade2000") and a user's number ("a1234567") do not.
func buildHash(text string) int {
	end, changes := 0, 0
	for ; end < len(text); end++ {
		c := text[end]
		letter := c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
		if !letter && (c < '0' || c > '9') {
			break
		}
		if end > 0 {
			previous := text[end-1]
			if letter != (previous >= 'a' && previous <= 'f' || previous >= 'A' && previous <= 'F') {
				changes++
			}
		}
	}
	if end < 8 || changes < 2 {
		return 0
	}
	return end
}

// nixStorePath replaces the hash that starts a Nix store path's name: /nix/store/<hash>-<name>.
func nixStorePath(path string) string {
	const parent, hash = "/nix/store/", 32
	if !strings.HasPrefix(path, parent) || len(path) <= len(parent)+hash || path[len(parent)+hash] != '-' ||
		strings.Trim(path[len(parent):len(parent)+hash], "0123456789abcdefghijklmnopqrstuvwxyz") != "" {
		return path
	}
	return parent + "*" + path[len(parent)+hash:]
}

// macOS runs an app that is still quarantined (opened where it was downloaded or from its disk
// image) from a copy at .../AppTranslocation/<UUID>/d/<bundle>, with a new UUID at each start.
const translocation = "/AppTranslocation/"

// translocationFolder returns where the UUID folder of a translocated path starts and ends.
func translocationFolder(path string) (start, end int, ok bool) {
	at := strings.Index(path, translocation)
	if at < 0 {
		return 0, 0, false
	}
	start = at + len(translocation)
	length := strings.IndexByte(path[start:], '/')
	if length <= 0 || !strings.HasPrefix(path[start+length:], "/d/") {
		return 0, 0, false
	}
	return start, start + length, true
}

// translocationPath replaces that UUID, so a rule saved from one start matches the next.
func translocationPath(path string) string {
	start, end, ok := translocationFolder(path)
	if !ok {
		return path
	}
	return path[:start] + "*" + path[end:]
}

// translocatedPath returns what a translocated program runs inside that folder, from its bundle
// on: "Example.app/Contents/MacOS/Example". Empty for any other path.
func translocatedPath(path string) string {
	_, end, ok := translocationFolder(path)
	if !ok {
		return ""
	}
	return path[end+len("/d/"):]
}

// bundleTail returns a rule's path from its app bundle on, as translocatedPath gives a program's:
// the bundle is all that a translocated copy keeps of where the app is. Empty for a rule that
// names no bundle.
func bundleTail(rule string) string {
	at := 0
	for _, part := range strings.SplitAfter(rule, "/") {
		if strings.HasSuffix(strings.TrimSuffix(part, "/"), ".app") && len(part) > len(".app/") {
			return rule[at:]
		}
		at += len(part)
	}
	return ""
}

// packageFamilyFolder returns the Microsoft Store package family that a folder rule's key names,
// "<name>_<publisher>" of .../windowsapps/<name>_<publisher>/, or "".
func packageFamilyFolder(key string) string {
	const parent = "/windowsapps/"
	at := strings.Index(key, parent)
	if at < 0 {
		return ""
	}
	family, folder := strings.CutSuffix(key[at+len(parent):], "/")
	if !folder {
		return ""
	}
	name, publisher, ok := strings.Cut(family, "_")
	if !ok || !packageName(name) || len(publisher) != 13 || strings.Trim(publisher, "abcdefghijklmnopqrstuvwxyz0123456789") != "" {
		return ""
	}
	return family
}

// SameProcessRule reports whether a saved rule and a rule the app list gives an app name the
// same thing: the same rule, also when it was written for another version. A folder and a
// program inside it are two rules. "children:" is left out: it adds programs to the same app.
func SameProcessRule(saved, app string) bool {
	saved, app = strings.TrimPrefix(saved, "children:"), strings.TrimPrefix(app, "children:")
	if saved == app {
		return true
	}
	for _, kind := range []string{"flatpak:", "appimage:"} {
		if strings.HasPrefix(saved, kind) || strings.HasPrefix(app, kind) {
			return false
		}
	}
	savedService, isService := strings.CutPrefix(saved, "service:")
	if appService, ok := strings.CutPrefix(app, "service:"); ok || isService {
		return ok && isService && strings.EqualFold(savedService, appService)
	}
	savedPath, savedExact := strings.CutPrefix(saved, "exact:")
	appPath, appExact := strings.CutPrefix(app, "exact:")
	savedPath, appPath = normalPath(savedPath, runtime.GOOS), normalPath(appPath, runtime.GOOS)
	named := strings.Contains(savedPath, "/")
	switch {
	case named != strings.Contains(appPath, "/"):
		return false
	case !named:
		if runtime.GOOS == "windows" {
			savedPath, appPath = strings.TrimSuffix(savedPath, ".exe"), strings.TrimSuffix(appPath, ".exe")
		}
		return savedPath == appPath
	case savedExact || appExact:
		return savedPath == appPath
	case strings.HasSuffix(savedPath, "/") != strings.HasSuffix(appPath, "/"):
		return false
	}
	savedKey, appKey := normalPathKey(savedPath, runtime.GOOS), normalPathKey(appPath, runtime.GOOS)
	if savedKey == appKey {
		return true
	}
	rule := newPathRule(savedPath, savedKey)
	if rule == nil {
		return false
	}
	return rule.follows(strings.Split(strings.TrimSuffix(appKey, "/"), "/"), strings.Split(strings.TrimSuffix(appPath, "/"), "/"), true)
}
