#ifndef ELARNI_SHADOWQUIC_LINKED_H
#define ELARNI_SHADOWQUIC_LINKED_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

// The interface of the ShadowQUIC client (native/elarni-shadowquic/src/ffi.rs).
typedef uint64_t (*elarni_shadowquic_start_fn)(const uint8_t *config, size_t config_len, uint16_t *port,
                                               char *error, size_t error_len);
typedef void (*elarni_shadowquic_stop_fn)(uint64_t handle);
typedef bool (*elarni_shadowquic_running_fn)(uint64_t handle);
typedef void (*elarni_shadowquic_log_fn)(const char *line, size_t len);
typedef void (*elarni_shadowquic_set_log_fn)(elarni_shadowquic_log_fn sink);

// Gives the core the client that the program links. A program that links both calls
// this once, before it starts a core.
void elarni_shadowquic_link(elarni_shadowquic_start_fn start, elarni_shadowquic_stop_fn stop,
                            elarni_shadowquic_running_fn running, elarni_shadowquic_set_log_fn set_log);

// Loads the client from a shared library and links it. Returns whether it is linked.
bool elarni_shadowquic_load(const char *library);

bool elarni_shadowquic_linked(void);
uint64_t elarni_shadowquic_call_start(const uint8_t *config, size_t config_len, uint16_t *port, char *error,
                                      size_t error_len);
void elarni_shadowquic_call_stop(uint64_t handle);
bool elarni_shadowquic_call_running(uint64_t handle);

#endif
