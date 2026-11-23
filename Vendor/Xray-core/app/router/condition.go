package router

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/xtls/xray-core/common/errors"
	"github.com/xtls/xray-core/common/flightrecorder"
	"github.com/xtls/xray-core/common/geodata"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/features/routing/dns"
	routingsession "github.com/xtls/xray-core/features/routing/session"
)

type Condition interface {
	Apply(ctx routing.Context) bool
}

type ConditionChan []Condition

func NewConditionChan() *ConditionChan {
	var condChan ConditionChan = make([]Condition, 0, 8)
	return &condChan
}

func (v *ConditionChan) Add(cond Condition) *ConditionChan {
	*v = append(*v, cond)
	return v
}

// Apply applies all conditions registered in this chan.
func (v *ConditionChan) Apply(ctx routing.Context) bool {
	for _, cond := range *v {
		if !cond.Apply(ctx) {
			return false
		}
	}
	return true
}

func (v *ConditionChan) Len() int {
	return len(*v)
}

type DomainMatcher struct{ geodata.DomainMatcher }

func NewDomainMatcher(rules []*geodata.DomainRule) (*DomainMatcher, error) {
	m, err := geodata.DomainReg.BuildDomainMatcher(rules)
	if err != nil {
		return nil, err
	}
	return &DomainMatcher{DomainMatcher: m}, nil
}

func (m *DomainMatcher) ApplyDomain(domain string) bool {
	return m.DomainMatcher.MatchAny(strings.ToLower(domain))
}

// Apply implements Condition.
func (m *DomainMatcher) Apply(ctx routing.Context) bool {
	domain := ctx.GetTargetDomain()
	if len(domain) == 0 {
		return false
	}
	return m.DomainMatcher.MatchAny(strings.ToLower(domain))
}

type MatcherAsType byte

const (
	MatcherAsType_Local MatcherAsType = iota
	MatcherAsType_Source
	MatcherAsType_Target
	MatcherAsType_VlessRoute // for port
)

type IPMatcher struct {
	matcher geodata.IPMatcher
	asType  MatcherAsType
}

func NewIPMatcher(rules []*geodata.IPRule, asType MatcherAsType) (*IPMatcher, error) {
	m, err := geodata.IPReg.BuildIPMatcher(rules)
	if err != nil {
		return nil, err
	}
	return &IPMatcher{matcher: m, asType: asType}, nil
}

// Apply implements Condition.
func (m *IPMatcher) Apply(ctx routing.Context) bool {
	var ips []net.IP

	switch m.asType {
	case MatcherAsType_Local:
		ips = ctx.GetLocalIPs()
	case MatcherAsType_Source:
		ips = ctx.GetSourceIPs()
	case MatcherAsType_Target:
		ips = ctx.GetTargetIPs()
	default:
		panic("unk asType")
	}

	return m.matcher.AnyMatch(ips)
}

type PortMatcher struct {
	port   net.MemoryPortList
	asType MatcherAsType
}

// NewPortMatcher create a new port matcher that can match source or local or destination port
func NewPortMatcher(list *net.PortList, asType MatcherAsType) *PortMatcher {
	return &PortMatcher{
		port:   net.PortListFromProto(list),
		asType: asType,
	}
}

// Apply implements Condition.
func (v *PortMatcher) Apply(ctx routing.Context) bool {
	switch v.asType {
	case MatcherAsType_Local:
		return v.port.Contains(ctx.GetLocalPort())
	case MatcherAsType_Source:
		return v.port.Contains(ctx.GetSourcePort())
	case MatcherAsType_Target:
		return v.port.Contains(ctx.GetTargetPort())
	case MatcherAsType_VlessRoute:
		return v.port.Contains(ctx.GetVlessRoute())
	default:
		panic("unk asType")
	}
}

type NetworkMatcher struct {
	list [8]bool
}

func NewNetworkMatcher(network []net.Network) NetworkMatcher {
	var matcher NetworkMatcher
	for _, n := range network {
		matcher.list[int(n)] = true
	}
	return matcher
}

// Apply implements Condition.
func (v NetworkMatcher) Apply(ctx routing.Context) bool {
	return v.list[int(ctx.GetNetwork())]
}

type UserMatcher struct {
	user    []string
	pattern []*regexp.Regexp
}

func NewUserMatcher(users []string) *UserMatcher {
	usersCopy := make([]string, 0, len(users))
	patternsCopy := make([]*regexp.Regexp, 0, len(users))
	for _, user := range users {
		if len(user) > 0 {
			if len(user) > 7 && strings.HasPrefix(user, "regexp:") {
				if re, err := regexp.Compile(user[7:]); err == nil {
					patternsCopy = append(patternsCopy, re)
				}
				// Items of users slice with an invalid regexp syntax are ignored.
				continue
			}
			usersCopy = append(usersCopy, user)
		}
	}
	return &UserMatcher{
		user:    usersCopy,
		pattern: patternsCopy,
	}
}

// Apply implements Condition.
func (v *UserMatcher) Apply(ctx routing.Context) bool {
	user := ctx.GetUser()
	if len(user) == 0 {
		return false
	}
	for _, u := range v.user {
		if u == user {
			return true
		}
	}
	for _, re := range v.pattern {
		if re.MatchString(user) {
			return true
		}
	}
	return false
}

type InboundTagMatcher struct {
	tags []string
}

func NewInboundTagMatcher(tags []string) *InboundTagMatcher {
	tagsCopy := make([]string, 0, len(tags))
	for _, tag := range tags {
		if len(tag) > 0 {
			tagsCopy = append(tagsCopy, tag)
		}
	}
	return &InboundTagMatcher{
		tags: tagsCopy,
	}
}

// Apply implements Condition.
func (v *InboundTagMatcher) Apply(ctx routing.Context) bool {
	tag := ctx.GetInboundTag()
	if len(tag) == 0 {
		return false
	}
	for _, t := range v.tags {
		if t == tag {
			return true
		}
	}
	return false
}

type ProtocolMatcher struct {
	protocols []string
}

func NewProtocolMatcher(protocols []string) *ProtocolMatcher {
	pCopy := make([]string, 0, len(protocols))

	for _, p := range protocols {
		if len(p) > 0 {
			pCopy = append(pCopy, p)
		}
	}

	return &ProtocolMatcher{
		protocols: pCopy,
	}
}

// Apply implements Condition.
func (m *ProtocolMatcher) Apply(ctx routing.Context) bool {
	protocol := ctx.GetProtocol()
	if len(protocol) == 0 {
		return false
	}
	for _, p := range m.protocols {
		if strings.HasPrefix(protocol, p) {
			return true
		}
	}
	return false
}

type AttributeMatcher struct {
	configuredKeys map[string]*regexp.Regexp
}

// Match implements attributes matching.
func (m *AttributeMatcher) Match(attrs map[string]string) bool {
	// header keys are case insensitive most likely. So we do a convert
	httpHeaders := make(map[string]string)
	for key, value := range attrs {
		httpHeaders[strings.ToLower(key)] = value
	}
	for key, regex := range m.configuredKeys {
		if a, ok := httpHeaders[key]; !ok || !regex.MatchString(a) {
			return false
		}
	}
	return true
}

// Apply implements Condition.
func (m *AttributeMatcher) Apply(ctx routing.Context) bool {
	attributes := ctx.GetAttributes()
	if attributes == nil {
		return false
	}
	return m.Match(attributes)
}

type ProcessNameMatcher struct {
	ProcessNames  []string
	AbsPaths      []string
	Folders       []string
	MatchXraySelf bool
	// Elarni: "-1" also names the owner of a local flow that the lookup could not find
	// (Android's finder already returns that name). Such a flow may belong to a listed
	// app, so "Only listed" keeps it in the tunnel.
	MatchUnknown bool
	// Elarni: apps that a path does not name. "flatpak:<app id>" is a Flatpak sandbox,
	// "appimage:<file>" an AppImage, or every AppImage in "appimage:<folder>/" (Linux), and
	// "service:<name>" a Windows service by its name, the process that runs it.
	FlatpakApps []string
	AppImages   []string
	Services    []string
	// Elarni: "children:<rule>" also matches the programs that a program matching <rule> started,
	// and theirs: Claude starts Claude Code, Discord its updater.
	Ancestors *ProcessNameMatcher
	// Elarni: "exact:<path>" names a path or a folder as it is written, version numbers and all.
	ExactPaths   []string
	ExactFolders []string
	// Elarni: the Microsoft Store package families that folder rules name (Windows). Their
	// processes match wherever they run from: a hosted app runs System32's WWAHost.exe, a
	// background task backgroundTaskHost.exe.
	Packages []string
	// Elarni: the path rules as written, which follow a folder that replaced their own
	// (process_follow.go), and the app bundles they name, for an app macOS runs from elsewhere.
	followers []*pathRule
	bundles   []string
}

// maxProcessAncestors bounds the walk from a program to the programs that started it.
const maxProcessAncestors = 32

// Elarni: what the system knows of a process beyond its path; tests replace these.
var (
	processFlatpakApp = net.FlatpakApp
	processAppImage   = net.AppImage
	processServices   = net.ProcessServices
	parentProcess     = net.ParentProcess
	processPackage    = net.PackageFamily
)

func NewProcessNameMatcher(names []string) *ProcessNameMatcher {
	processNames := []string{}
	folders := []string{}
	absPaths := []string{}
	var flatpakApps, appImages, services, ancestors, exactPaths, exactFolders, packages, bundles []string
	var followers []*pathRule
	matchXraySelf := false
	for _, name := range names {
		if rule, ok := strings.CutPrefix(name, "children:"); ok {
			ancestors = append(ancestors, rule)
			continue
		}
		if name == "self/" {
			matchXraySelf = true
			continue
		}
		if id, ok := strings.CutPrefix(name, "flatpak:"); ok {
			flatpakApps = append(flatpakApps, id)
			continue
		}
		if image, ok := strings.CutPrefix(name, "appimage:"); ok {
			appImages = append(appImages, image)
			continue
		}
		if service, ok := strings.CutPrefix(name, "service:"); ok {
			services = append(services, strings.ToLower(service))
			continue
		}
		// replace xray/ with self executable path
		if name == "xray/" {
			xrayPath, err := os.Executable()
			if err != nil {
				errors.LogError(context.Background(), "Failed to get xray executable path: ", err)
				continue
			}
			name = xrayPath
		}
		exact := false
		if path, ok := strings.CutPrefix(name, "exact:"); ok {
			if path == "" {
				continue
			}
			name, exact = path, true
		}
		name := normalPath(name, runtime.GOOS)
		if exact && strings.Contains(name, "/") {
			if strings.HasSuffix(name, "/") {
				exactFolders = append(exactFolders, name)
			} else {
				exactPaths = append(exactPaths, name)
			}
			continue
		}
		if strings.Contains(name, "/") {
			key := normalPathKey(name, runtime.GOOS)
			if rule := newPathRule(name, key); rule != nil {
				followers = append(followers, rule)
			}
			if runtime.GOOS == "darwin" {
				if bundle := bundleTail(name); bundle != "" {
					bundles = append(bundles, bundle)
				}
			}
			// /usr/bin/
			if strings.HasSuffix(name, "/") {
				folders = append(folders, key)
				if family := packageFamilyFolder(key); family != "" && runtime.GOOS == "windows" {
					packages = append(packages, family)
				}
				continue
			}
			// /usr/bin/curl
			absPaths = append(absPaths, key)
			continue
		}
		// curl.exe or curl
		processNames = append(processNames, strings.TrimSuffix(name, ".exe"))
	}
	var ancestorMatcher *ProcessNameMatcher
	if len(ancestors) > 0 {
		ancestorMatcher = NewProcessNameMatcher(ancestors)
		// "children:" wraps one rule; nested, it adds nothing.
		ancestorMatcher.Ancestors = nil
	}
	return &ProcessNameMatcher{
		Ancestors:     ancestorMatcher,
		ProcessNames:  processNames,
		AbsPaths:      absPaths,
		Folders:       folders,
		MatchXraySelf: matchXraySelf,
		MatchUnknown:  slices.Contains(processNames, "-1"),
		FlatpakApps:   flatpakApps,
		AppImages:     appImages,
		Services:      services,
		ExactPaths:    exactPaths,
		ExactFolders:  exactFolders,
		Packages:      packages,
		followers:     followers,
		bundles:       bundles,
	}
}

// ProcessPathKey returns a path as rules and process paths compare it.
// Elarni: an update moves many apps to a new path, and a rule saved for one version must match the
// next, whatever installed the app. So paths compare without what names a build:
//   - numbers joined by dots, anywhere: app-1.0.9265 (Squirrel), Application\131.0.2903.86
//     (Edge WebView2), jdk-21.0.2, rufus-4.6.exe;
//   - a number of three or more digits that ends a folder's name after a dot, dash or underscore:
//     Battle.net.15173;
//   - a build's hash that ends a folder's name or is that name: Roblox's version-<hash>, Visual
//     Studio Code's <commit>;
//   - a Microsoft Store package's folder, compared as its family (Windows), a Snap's revision
//     (Linux), a Nix store path's hash, and the folder macOS runs a quarantined app from.
//
// The other version sits beside the one the rule names, in the same folder. Windows also ignores
// the letter case. Bare names are compared as they are. A number that is part of a name ("Anno
// 1800", "Photoshop 2025", jdk-21, a user's or a process's number) stays: two such folders can be
// two products. A rule follows such a folder only once its own is gone (process_follow.go).
func ProcessPathKey(path string) string {
	return pathKey(filepath.ToSlash(path), runtime.GOOS)
}

func pathKey(path, goos string) string {
	return normalPathKey(normalPath(path, goos), goos)
}

// normalPath spells a path with forward slashes as rules compare it: Windows ignores the case.
func normalPath(path, goos string) string {
	if goos == "windows" {
		return strings.ToLower(strings.ReplaceAll(path, `\`, "/"))
	}
	return path
}

// normalPathKey is the key of a path that normalPath spelled. It changes names within folders
// only, so a key has its path's folders one for one.
func normalPathKey(path, goos string) string {
	switch goos {
	case "windows":
		path = windowsPackageFamilyPath(path)
	case "linux":
		path = nixStorePath(snapPath(path))
	case "darwin":
		path = nixStorePath(translocationPath(path))
	}
	return versionlessPath(path)
}

// snapPath replaces the revision in /snap/<name>/<revision>/, which each refresh changes: a
// number, or x and a number for a locally installed Snap.
func snapPath(path string) string {
	const parent = "/snap/"
	if !strings.HasPrefix(path, parent) {
		return path
	}
	name := strings.IndexByte(path[len(parent):], '/')
	if name <= 0 {
		return path
	}
	start := len(parent) + name + 1
	end := strings.IndexByte(path[start:], '/')
	if end < 0 {
		return path
	}
	end += start
	revision := strings.TrimPrefix(path[start:end], "x")
	if revision == "" || strings.Trim(revision, "0123456789") != "" {
		return path
	}
	return path[:start] + "*" + path[end:]
}

// versionlessPath replaces what names a build in a path with forward slashes by "*".
func versionlessPath(path string) string {
	digit := func(i int) bool { return i < len(path) && path[i] >= '0' && path[i] <= '9' }
	var key strings.Builder
	copied := 0
	replace := func(start, end int) {
		if copied == 0 {
			key.Grow(len(path))
		}
		key.WriteString(path[copied:start])
		key.WriteByte('*')
		copied = end
	}
	for i := 0; i < len(path); {
		if i == 0 || strings.IndexByte("/.-_", path[i-1]) >= 0 {
			// A build's hash that ends a folder's name.
			if end := i + buildHash(path[i:]); end > i && end < len(path) && path[end] == '/' {
				replace(i, end)
				i = end
				continue
			}
		}
		if !digit(i) {
			i++
			continue
		}
		start := i
		for digit(i) {
			i++
		}
		version := false
		for i < len(path) && path[i] == '.' && digit(i+1) {
			version = true
			for i++; digit(i); i++ {
			}
		}
		if !version {
			// A build number that ends a folder's name. After a space, or as the whole name, a
			// number is part of the name.
			version = i-start >= 3 && i < len(path) && path[i] == '/' &&
				start > 0 && strings.IndexByte(".-_", path[start-1]) >= 0
		}
		if version {
			replace(start, i)
		}
	}
	if copied == 0 {
		return path
	}
	key.WriteString(path[copied:])
	return key.String()
}

// Elarni: Microsoft Store (MSIX) apps run from <volume>\WindowsApps\<name>_<version>_<arch>_<resource>_<publisher>\,
// a new folder with every update. Rules and process paths compare that folder as <name>_<publisher>, its
// package family, so a rule saved for one version matches the next. Only Windows can install there: the
// folder is writable by the system alone. Takes a lowercased path with forward slashes.
func windowsPackageFamilyPath(path string) string {
	const parent = "/windowsapps/"
	start := strings.Index(path, parent)
	if start < 0 {
		return path
	}
	start += len(parent)
	end := strings.IndexByte(path[start:], '/')
	if end < 0 {
		end = len(path)
	} else {
		end += start
	}
	parts := strings.Split(path[start:end], "_")
	if len(parts) != 5 || !packageName(parts[0]) || !packageVersion(parts[1]) ||
		!slices.Contains([]string{"x86", "x64", "arm", "arm64", "neutral", "x86a64"}, parts[2]) ||
		len(parts[4]) != 13 || strings.Trim(parts[4], "abcdefghijklmnopqrstuvwxyz0123456789") != "" {
		return path
	}
	return path[:start] + parts[0] + "_" + parts[4] + path[end:]
}

func packageName(name string) bool {
	return name != "" && strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789.-") == ""
}

func packageVersion(version string) bool {
	fields := strings.Split(version, ".")
	for _, field := range fields {
		if field == "" || strings.Trim(field, "0123456789") != "" {
			return false
		}
	}
	return len(fields) == 4
}

// lookupProcess finds the owner of a local socket; tests replace it.
var lookupProcess = net.FindProcess

func (m *ProcessNameMatcher) Apply(ctx routing.Context) (matched bool) {
	code := "no_source"
	if flightrecorder.Enabled() {
		started := time.Now()
		defer func() {
			if matched && code == "not_matched" {
				code = "matched"
			}
			flightrecorder.Record(flightrecorder.Event{Name: "process_match", Code: code, Values: map[string]int64{"network": int64(ctx.GetNetwork()), "elapsed_us": time.Since(started).Microseconds()}})
		}()
	}
	if len(ctx.GetSourceIPs()) == 0 {
		return false
	}

	srcPort := uint16(ctx.GetSourcePort())
	srcIP := ctx.GetSourceIPs()[0].String()

	var network string
	switch ctx.GetNetwork() {
	case net.Network_TCP:
		network = "tcp"
	case net.Network_UDP:
		network = "udp"
	default:
		code = "unsupported_network"
		return false
	}

	var dstIP string
	var dstPort uint16 = 0

	// Process lookup needs the OS socket tuple, before sniffing/FakeDNS or DNS
	// resolution changed the routing target.
	lookupContext := ctx
	if resolvable, ok := ctx.(*dns.ResolvableContext); ok {
		lookupContext = resolvable.Context
	}
	if original, ok := lookupContext.(*routingsession.Context); ok && original.Outbound != nil && original.Outbound.OriginalTarget.IsValid() && original.Outbound.OriginalTarget.Address.Family().IsIP() {
		dstIP = original.Outbound.OriginalTarget.Address.IP().String()
		dstPort = uint16(original.Outbound.OriginalTarget.Port)
	} else if ips := lookupContext.GetTargetIPs(); len(ips) > 0 {
		dstIP = ips[0].String()
		dstPort = uint16(lookupContext.GetTargetPort())
	}

	find := func() (int, string, string, error) {
		pid, name, path, err := lookupProcess(network, srcIP, srcPort, dstIP, dstPort)
		if err != nil && err != net.ErrNotLocal {
			// Elarni: once per connection rather than per process rule, and at info: the connection is still routed by the other rules.
			errors.LogInfo(context.Background(), "Unable to find the process of ", network, " ", srcIP, ":", srcPort, ": ", err)
		}
		return pid, name, path, err
	}
	var pid int
	var name, absPath string
	var err error
	if original, ok := lookupContext.(*routingsession.Context); ok {
		pid, name, absPath, err = original.LookupProcess(find)
	} else {
		pid, name, absPath, err = find()
	}
	if err != nil {
		if err == net.ErrNotLocal {
			code = "not_local"
			return false
		}
		code = "lookup_failed"
		if m.MatchUnknown {
			code = "unknown_owner"
		}
		return m.MatchUnknown
	}
	code = "not_matched"
	matched, via := m.matchProcess(pid, name, absPath)
	if via != "" {
		if original, ok := lookupContext.(*routingsession.Context); ok {
			original.NoteProcessAncestor(via)
		}
	}
	return matched
}

// MatchesProcess reports whether the rule names a process, as FindProcess describes it.
// Elarni: the per-app editor counts running programs with it, as routing would match them.
func (m *ProcessNameMatcher) MatchesProcess(pid int, name, absPath string) bool {
	matched, _ := m.matchProcess(pid, name, absPath)
	return matched
}

// matchProcess also returns the path of the program that started this one, when a children
// rule matched that program rather than this one.
func (m *ProcessNameMatcher) matchProcess(pid int, name, absPath string) (bool, string) {
	if m.matchesSelf(pid, name, absPath) {
		return true, ""
	}
	if m.Ancestors == nil || pid <= 0 {
		return false, ""
	}
	if m.Ancestors.matchesSelf(pid, name, absPath) {
		return true, ""
	}
	seen := map[int]bool{pid: true}
	for depth := 0; depth < maxProcessAncestors; depth++ {
		parent, ok := parentProcess(pid)
		if !ok || seen[parent.PID] {
			return false, ""
		}
		if m.Ancestors.matchesSelf(parent.PID, parent.Name, parent.Path) {
			return true, parent.Path
		}
		seen[parent.PID] = true
		pid = parent.PID
	}
	return false, ""
}

func (m *ProcessNameMatcher) matchesSelf(pid int, name, absPath string) bool {
	if m.MatchXraySelf && pid == os.Getpid() {
		return true
	}
	// Identity lookups take the process as the lookup reported it.
	reportedName, reportedPath := name, absPath
	if runtime.GOOS == "windows" {
		name = strings.TrimSuffix(strings.ToLower(name), ".exe")
	}
	written := normalPath(absPath, runtime.GOOS)
	absPath = normalPathKey(written, runtime.GOOS)
	if slices.Contains(m.ProcessNames, name) {
		return true
	}
	if slices.Contains(m.AbsPaths, absPath) || slices.Contains(m.ExactPaths, written) {
		return true
	}
	for _, f := range m.Folders {
		if strings.HasPrefix(absPath, f) {
			return true
		}
	}
	for _, f := range m.ExactFolders {
		if strings.HasPrefix(written, f) {
			return true
		}
	}
	if len(m.bundles) > 0 {
		if run := translocatedPath(written); run != "" {
			for _, bundle := range m.bundles {
				if run == bundle || strings.HasSuffix(bundle, "/") && strings.HasPrefix(run, bundle) {
					return true
				}
			}
		}
	}
	if len(m.followers) > 0 && written != "" {
		keys, parts := strings.Split(absPath, "/"), strings.Split(written, "/")
		for _, rule := range m.followers {
			if rule.follows(keys, parts, false) {
				return true
			}
		}
	}
	if pid <= 0 {
		return false
	}
	if len(m.Packages) > 0 {
		if family := processPackage(pid); family != "" && slices.Contains(m.Packages, strings.ToLower(family)) {
			return true
		}
	}
	if len(m.FlatpakApps) > 0 {
		if id := processFlatpakApp(pid); id != "" && slices.Contains(m.FlatpakApps, id) {
			return true
		}
	}
	if len(m.AppImages) > 0 {
		if image := processAppImage(pid, reportedPath); image != "" {
			for _, rule := range m.AppImages {
				if image == rule || strings.HasSuffix(rule, "/") && strings.HasPrefix(image, rule) {
					return true
				}
			}
		}
	}
	if len(m.Services) > 0 {
		for _, service := range processServices(pid, reportedName) {
			if slices.Contains(m.Services, strings.ToLower(service)) {
				return true
			}
		}
	}
	return false
}

// LocalOSMatcher matches the operating system Xray itself is running on. That never
// changes while Xray is running, so the result is resolved when the rule is built.
type LocalOSMatcher struct {
	matched bool
}

func NewLocalOSMatcher(names []string) *LocalOSMatcher {
	return &LocalOSMatcher{
		matched: slices.ContainsFunc(names, func(name string) bool {
			return strings.EqualFold(name, runtime.GOOS)
		}),
	}
}

// Apply implements Condition.
func (m *LocalOSMatcher) Apply(_ routing.Context) bool {
	return m.matched
}
