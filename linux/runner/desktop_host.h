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
// Whether the arguments, without the program name, are exactly the autostart entry's start
// hidden in the tray.
bool desktop_host_hidden_start(const char* const* arguments);
// The window stays hidden at start. When no status-notifier host shows the tray icon a
// short time later, the window is shown minimized instead, so the app stays reachable.
void desktop_host_start_hidden(DesktopHost* host);
void desktop_host_free(DesktopHost* host);
