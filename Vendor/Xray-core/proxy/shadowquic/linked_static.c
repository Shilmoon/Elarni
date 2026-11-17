//go:build cgo && darwin && elarni_shadowquic_static

#include "linked.h"

// The client itself (native/elarni-shadowquic/src/ffi.rs), from libelarni_shadowquic.a.
extern uint64_t elarni_shadowquic_start(const uint8_t *config, size_t config_len, uint16_t *port, char *error,
                                        size_t error_len);
extern void elarni_shadowquic_stop(uint64_t handle);
extern bool elarni_shadowquic_running(uint64_t handle);
extern void elarni_shadowquic_set_log(elarni_shadowquic_log_fn sink);

void elarni_shadowquic_link_static(void) {
  elarni_shadowquic_link(elarni_shadowquic_start, elarni_shadowquic_stop, elarni_shadowquic_running,
                         elarni_shadowquic_set_log);
}
