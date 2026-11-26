#include "elarni_camera.h"

#include <gst/app/gstappsink.h>
#include <gst/gst.h>

struct ElarniCamera {
  GstElement* pipeline = nullptr;
  GstElement* frames = nullptr;
};

namespace {
// A frame's bytes stay mapped in its sample until the runner has sent them.
struct Frame {
  GstSample* sample;
  GstMapInfo map;
};
void release_frame(gpointer data) {
  auto* frame = static_cast<Frame*>(data);
  gst_buffer_unmap(gst_sample_get_buffer(frame->sample), &frame->map);
  gst_sample_unref(frame->sample);
  delete frame;
}
}  // namespace

extern "C" {

void elarni_camera_close(ElarniCamera* camera) {
  if (!camera) return;
  if (camera->pipeline) gst_element_set_state(camera->pipeline, GST_STATE_NULL);
  if (camera->frames) gst_object_unref(camera->frames);
  if (camera->pipeline) gst_object_unref(camera->pipeline);
  delete camera;
}

ElarniCamera* elarni_camera_open(void) {
  if (!gst_init_check(nullptr, nullptr, nullptr)) return nullptr;
  auto* camera = new ElarniCamera{};
  g_autoptr(GError) error = nullptr;
  camera->pipeline = gst_parse_launch(
      "decodebin name=decode ! videoconvert ! videoscale ! video/x-raw,width=640,height=480 "
      "! jpegenc ! appsink name=frames max-buffers=1 drop=true sync=false", &error);
  if (error || !camera->pipeline) { elarni_camera_close(camera); return nullptr; }
  // autovideosrc falls back to synthetic color bars when no camera opens.
  // Select an actual device, including cameras whose path is not /dev/video0.
  g_autoptr(GstDeviceMonitor) monitor = gst_device_monitor_new();
  g_autoptr(GstCaps) caps = gst_caps_from_string("video/x-raw;image/jpeg");
  gst_device_monitor_add_filter(monitor, "Video/Source", caps);
  GList* devices = gst_device_monitor_get_devices(monitor);
  GstElement* source = nullptr;
  for (GList* item = devices; item && !source; item = item->next) {
    auto* candidate = gst_device_create_element(GST_DEVICE(item->data), "camera");
    if (!candidate) continue;
    if (gst_element_set_state(candidate, GST_STATE_READY) != GST_STATE_CHANGE_FAILURE) source = candidate;
    else { gst_element_set_state(candidate, GST_STATE_NULL); gst_object_unref(candidate); }
  }
  g_list_free_full(devices, gst_object_unref);
  if (!source) { elarni_camera_close(camera); return nullptr; }
  if (!gst_bin_add(GST_BIN(camera->pipeline), source)) {
    gst_element_set_state(source, GST_STATE_NULL); gst_object_unref(source);
    elarni_camera_close(camera); return nullptr;
  }
  g_autoptr(GstElement) decoder = gst_bin_get_by_name(GST_BIN(camera->pipeline), "decode");
  if (!decoder || !gst_element_link(source, decoder)) { elarni_camera_close(camera); return nullptr; }
  camera->frames = gst_bin_get_by_name(GST_BIN(camera->pipeline), "frames");
  if (!camera->frames || gst_element_set_state(camera->pipeline, GST_STATE_PLAYING) == GST_STATE_CHANGE_FAILURE) {
    elarni_camera_close(camera); return nullptr;
  }
  return camera;
}

GBytes* elarni_camera_frame(ElarniCamera* camera, gboolean* failed) {
  *failed = FALSE;
  GstBus* bus = gst_element_get_bus(camera->pipeline);
  GstMessage* error = gst_bus_pop_filtered(bus, static_cast<GstMessageType>(GST_MESSAGE_ERROR | GST_MESSAGE_EOS));
  gst_object_unref(bus);
  if (error) { gst_message_unref(error); *failed = TRUE; return nullptr; }
  GstSample* sample = gst_app_sink_try_pull_sample(GST_APP_SINK(camera->frames), 0);
  if (!sample) return nullptr;
  auto* frame = new Frame{sample, {}};
  GstBuffer* buffer = gst_sample_get_buffer(sample);
  if (!buffer || !gst_buffer_map(buffer, &frame->map, GST_MAP_READ)) {
    gst_sample_unref(sample); delete frame; *failed = TRUE; return nullptr;
  }
  return g_bytes_new_with_free_func(frame->map.data, frame->map.size, release_frame, frame);
}

}  // extern "C"
