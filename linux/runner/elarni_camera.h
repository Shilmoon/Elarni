#pragma once

#include <glib.h>

// The QR scanner's camera, built as lib/libelarni_camera.so. Only this module links GStreamer: the
// runner opens it at the first camera call, so a system without GStreamer still starts the app.
G_BEGIN_DECLS

typedef struct ElarniCamera ElarniCamera;
// Starts the first video capture device that opens, as 640x480 JPEG frames; NULL when none opens.
ElarniCamera* elarni_camera_open(void);
// The newest frame as JPEG bytes, or NULL while none has come. Sets *failed when the camera stopped
// with an error or at the end of its stream; the caller then closes it.
GBytes* elarni_camera_frame(ElarniCamera* camera, gboolean* failed);
void elarni_camera_close(ElarniCamera* camera);

G_END_DECLS
