//go:build cgo && (darwin || android)

package shadowquic

/*
#cgo android LDFLAGS: -ldl
#include <stdlib.h>
#include "linked.h"
*/
import "C"

import (
	"runtime"
	"unsafe"

	"github.com/xtls/xray-core/common/errors"
)

// linked is a client in this process.
type linked struct {
	handle C.uint64_t
	port   uint16
}

// LoadLibrary links the client from a shared library. Android apps carry it as
// libelarni_shadowquic.so, which the first ShadowQUIC outbound loads by itself.
func LoadLibrary(path string) bool {
	name := C.CString(path)
	defer C.free(unsafe.Pointer(name))
	return bool(C.elarni_shadowquic_load(name))
}

func startLinked(description []byte) (nativeClient, error) {
	if !bool(C.elarni_shadowquic_linked()) && (runtime.GOOS != "android" || !LoadLibrary("libelarni_shadowquic.so")) {
		return nil, errNotLinked
	}
	var port C.uint16_t
	var message [256]C.char
	handle := C.elarni_shadowquic_call_start((*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(description))), C.size_t(len(description)),
		&port, &message[0], C.size_t(len(message)))
	if handle == 0 {
		return nil, errors.New("the ShadowQUIC client did not start: ", C.GoString(&message[0]))
	}
	return &linked{handle: handle, port: uint16(port)}, nil
}

func (l *linked) Port() uint16 { return l.port }

func (l *linked) Running() bool { return bool(C.elarni_shadowquic_call_running(l.handle)) }

func (l *linked) Stop() { C.elarni_shadowquic_call_stop(l.handle) }

//export elarniShadowquicLog
func elarniShadowquicLog(line *C.char, length C.size_t) {
	logLine(C.GoStringN(line, C.int(length)))
}
