//go:build cgo && darwin && elarni_shadowquic_static

package shadowquic

// The Apple apps and network extensions link this core as a static library next to the
// client's (libelarni_shadowquic.a), so the client is part of the process from the start.
// The core's own archive is made without the client; the flag lets it name the client's
// functions, and the program's link fails when the client is missing.

/*
#cgo LDFLAGS: -Wl,-undefined,dynamic_lookup
void elarni_shadowquic_link_static(void);
*/
import "C"

func init() { C.elarni_shadowquic_link_static() }
