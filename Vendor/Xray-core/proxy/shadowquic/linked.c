//go:build cgo && (darwin || android)

#include "linked.h"

#include <dlfcn.h>
#include <pthread.h>

// Defined in linked_cgo.go.
extern void elarniShadowquicLog(char *line, size_t len);

static pthread_mutex_t lock = PTHREAD_MUTEX_INITIALIZER;
static elarni_shadowquic_start_fn linked_start;
static elarni_shadowquic_stop_fn linked_stop;
static elarni_shadowquic_running_fn linked_running;

static void forward_log(const char *line, size_t len) { elarniShadowquicLog((char *)line, len); }

void elarni_shadowquic_link(elarni_shadowquic_start_fn start, elarni_shadowquic_stop_fn stop,
                            elarni_shadowquic_running_fn running, elarni_shadowquic_set_log_fn set_log) {
  if (start == NULL || stop == NULL || running == NULL) {
    return;
  }
  pthread_mutex_lock(&lock);
  bool first = linked_start == NULL;
  linked_start = start;
  linked_stop = stop;
  linked_running = running;
  pthread_mutex_unlock(&lock);
  if (first && set_log != NULL) {
    set_log(forward_log);
  }
}

bool elarni_shadowquic_load(const char *library) {
  if (elarni_shadowquic_linked()) {
    return true;
  }
  void *handle = dlopen(library, RTLD_NOW | RTLD_GLOBAL);
  if (handle == NULL) {
    return false;
  }
  elarni_shadowquic_link((elarni_shadowquic_start_fn)dlsym(handle, "elarni_shadowquic_start"),
                         (elarni_shadowquic_stop_fn)dlsym(handle, "elarni_shadowquic_stop"),
                         (elarni_shadowquic_running_fn)dlsym(handle, "elarni_shadowquic_running"),
                         (elarni_shadowquic_set_log_fn)dlsym(handle, "elarni_shadowquic_set_log"));
  return elarni_shadowquic_linked();
}

bool elarni_shadowquic_linked(void) {
  pthread_mutex_lock(&lock);
  bool linked = linked_start != NULL;
  pthread_mutex_unlock(&lock);
  return linked;
}

uint64_t elarni_shadowquic_call_start(const uint8_t *config, size_t config_len, uint16_t *port, char *error,
                                      size_t error_len) {
  return linked_start(config, config_len, port, error, error_len);
}

void elarni_shadowquic_call_stop(uint64_t handle) { linked_stop(handle); }

bool elarni_shadowquic_call_running(uint64_t handle) { return linked_running(handle); }
