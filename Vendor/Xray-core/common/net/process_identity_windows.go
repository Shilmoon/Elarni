//go:build windows

package net

import (
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// FlatpakApp and AppImage name Linux apps.
func FlatpakApp(int) string { return "" }

func AppImage(int, string) string { return "" }

// The service snapshot is reused for this long; a svchost process it does not know refreshes it
// sooner, as a service that just started does.
const (
	servicesFresh = 2 * time.Second
	servicesRetry = 250 * time.Millisecond
)

var serviceHosts struct {
	sync.Mutex
	taken time.Time
	byPID map[uint32][]string
}

// ProcessServices returns the names of the services a process runs. svchost runs most of them,
// one per process on most PCs, several per process on some.
func ProcessServices(pid int, name string) []string {
	if pid <= 0 {
		return nil
	}
	hosts := &serviceHosts
	hosts.Lock()
	defer hosts.Unlock()
	age := time.Since(hosts.taken)
	_, known := hosts.byPID[uint32(pid)]
	if hosts.taken.IsZero() || age > servicesFresh || !known && svchostName(name) && age > servicesRetry {
		if byPID, err := listServices(); err == nil {
			hosts.byPID = byPID
		}
		hosts.taken = time.Now()
	}
	return append([]string(nil), hosts.byPID[uint32(pid)]...)
}

// listServices maps the processes of running services to their names. Listing needs no
// administrator.
func listServices() (map[uint32][]string, error) {
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_ENUMERATE_SERVICE)
	if err != nil {
		return nil, err
	}
	defer windows.CloseServiceHandle(manager)
	var buffer []byte
	for {
		var first *byte
		if len(buffer) > 0 {
			first = &buffer[0]
		}
		var needed, returned uint32
		err := windows.EnumServicesStatusEx(manager, windows.SC_ENUM_PROCESS_INFO, windows.SERVICE_WIN32,
			windows.SERVICE_ACTIVE, first, uint32(len(buffer)), &needed, &returned, nil, nil)
		if err == nil {
			byPID := map[uint32][]string{}
			if returned > 0 {
				for _, service := range unsafe.Slice((*windows.ENUM_SERVICE_STATUS_PROCESS)(unsafe.Pointer(&buffer[0])), int(returned)) {
					if pid := service.ServiceStatusProcess.ProcessId; pid != 0 {
						byPID[pid] = append(byPID[pid], windows.UTF16PtrToString(service.ServiceName))
					}
				}
			}
			return byPID, nil
		}
		if err != windows.ERROR_MORE_DATA || needed <= uint32(len(buffer)) {
			return nil, err
		}
		buffer = make([]byte, needed)
	}
}

// AppRule returns the rule for the app a process belongs to when its path does not name it: the
// service a svchost process runs, when it runs one (a rule for one of several would take the
// others' connections too), and the Microsoft Store app that a program of Windows runs for:
// WWAHost.exe hosts some apps, backgroundTaskHost.exe their background tasks.
func AppRule(pid int, name, path string) string {
	if svchostName(name) {
		if services := ProcessServices(pid, name); len(services) == 1 {
			return "service:" + services[0]
		}
		return ""
	}
	root, err := windows.GetWindowsDirectory()
	if err != nil || len(path) <= len(root) || !strings.EqualFold(path[:len(root)], root) || path[len(root)] != '\\' {
		return ""
	}
	return packageRule(pid)
}

var (
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	procGetPackageFamilyName     = kernel32.NewProc("GetPackageFamilyName")
	procGetPackageFullName       = kernel32.NewProc("GetPackageFullName")
	procGetPackagePathByFullName = kernel32.NewProc("GetPackagePathByFullName")
)

// packageName asks Windows for a name of the package a process runs as, with
// GetPackageFamilyName or GetPackageFullName. Empty for a process without a package, which most
// are, and for one this process may not open.
func packageName(pid int, proc *windows.LazyProc) string {
	if pid <= 0 || proc.Find() != nil {
		return ""
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(process)
	var buffer [256]uint16
	length := uint32(len(buffer))
	if status, _, _ := proc.Call(uintptr(process), uintptr(unsafe.Pointer(&length)), uintptr(unsafe.Pointer(&buffer[0]))); status != 0 {
		return ""
	}
	return windows.UTF16ToString(buffer[:])
}

// PackageFamily returns the Microsoft Store package family a process runs as,
// "<name>_<publisher>", whatever file it runs from. Empty for a process without a package.
func PackageFamily(pid int) string {
	return packageName(pid, procGetPackageFamilyName)
}

// packageRule returns the folder rule of the package a process runs as, as the app list gives
// it: <volume>\WindowsApps\<family>\. Empty without a package, or for one installed elsewhere.
func packageRule(pid int) string {
	family := PackageFamily(pid)
	full := packageName(pid, procGetPackageFullName)
	if family == "" || full == "" || procGetPackagePathByFullName.Find() != nil {
		return ""
	}
	name, err := windows.UTF16PtrFromString(full)
	if err != nil {
		return ""
	}
	var buffer [windows.MAX_PATH + 1]uint16
	length := uint32(len(buffer))
	if status, _, _ := procGetPackagePathByFullName.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&length)), uintptr(unsafe.Pointer(&buffer[0]))); status != 0 {
		return ""
	}
	folder := windows.UTF16ToString(buffer[:])
	parent := strings.LastIndexByte(folder, '\\')
	if parent < 0 || !strings.HasSuffix(strings.ToLower(folder[:parent]), `\windowsapps`) {
		return ""
	}
	return folder[:parent+1] + family + `\`
}
