#pragma once

#include <flutter_linux/flutter_linux.h>
#include <gtk/gtk.h>

// Owned by the application, on GTK's main thread.
struct DesktopHost;
DesktopHost* desktop_host_new(GtkApplication* app, GtkWindow* window, FlView* view);
void desktop_host_links(DesktopHost* host, char** arguments);
// Whether a close should hide the window: a status-notifier host shows the tray
// icon that brings it back, and the close is not the tray's Quit or Ctrl+Q.
bool desktop_host_hides_on_close(DesktopHost* host);
// Tells Dart that a close hid the window to the tray.
void desktop_host_closed_to_tray(DesktopHost* host);
void desktop_host_free(DesktopHost* host);
