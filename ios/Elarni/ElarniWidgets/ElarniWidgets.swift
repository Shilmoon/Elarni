import AppIntents
import NetworkExtension
import SwiftUI
import WidgetKit

@main
struct ElarniWidgets: WidgetBundle {
  var body: some Widget {
    ConnectionControl()
    ConnectionStatusWidget()
  }
}

/// Control Center's Elarni toggle: on while the VPN runs or starts.
struct ConnectionControl: ControlWidget {
  var body: some ControlWidgetConfiguration {
    StaticControlConfiguration(kind: ConnectionControlIntent.kind, provider: ConnectionState()) { active in
      ControlWidgetToggle("Elarni", isOn: active, action: ConnectionControlIntent()) { on in
        Label(on ? String(localized: "Connected") : String(localized: "Disconnected"), systemImage: on ? "lock.shield.fill" : "lock.shield")
      }
    }
    .displayName("Elarni VPN")
    .description("Connects to the server selected in Elarni, or disconnects.")
  }
}

struct ConnectionState: ControlValueProvider {
  var previewValue: Bool { false }
  // The app reloads the control when the VPN changes; the extension reads the app's VPN configuration.
  func currentValue() async throws -> Bool {
    try await NETunnelProviderManager.loadAllFromPreferences().contains { [.connected, .connecting, .reasserting].contains($0.connection.status) }
  }
}

/// Home Screen and Lock Screen: the VPN state and the selected server. The small one also connects and disconnects.
struct ConnectionStatusWidget: Widget {
  var body: some WidgetConfiguration {
    StaticConfiguration(kind: ConnectionWidgetStatus.kind, provider: ConnectionStatusTimeline()) { entry in
      ConnectionStatusView(entry: entry).containerBackground(.background, for: .widget)
    }
    .configurationDisplayName("Elarni")
    .description("Shows whether Elarni is connected and to which server.")
    .supportedFamilies([.systemSmall, .accessoryRectangular])
  }
}

struct ConnectionStatusEntry: TimelineEntry {
  let date: Date
  let state: ConnectionWidgetStatus.State
  let server: String?
}

struct ConnectionStatusTimeline: TimelineProvider {
  func placeholder(in context: Context) -> ConnectionStatusEntry {
    ConnectionStatusEntry(date: Date(), state: .disconnected, server: nil)
  }
  func getSnapshot(in context: Context, completion: @escaping (ConnectionStatusEntry) -> Void) { current(completion) }
  // The app reloads the widget when the VPN or the selected server changes; the refresh covers what changed while it was closed.
  func getTimeline(in context: Context, completion: @escaping (Timeline<ConnectionStatusEntry>) -> Void) {
    current { completion(Timeline(entries: [$0], policy: .after(Date().addingTimeInterval(15 * 60)))) }
  }
  // As the Control Center toggle: the extension reads the app's VPN configuration.
  private func current(_ completion: @escaping (ConnectionStatusEntry) -> Void) {
    NETunnelProviderManager.loadAllFromPreferences { managers, _ in
      completion(ConnectionStatusEntry(date: Date(), state: ConnectionWidgetStatus.state((managers ?? []).map(\.connection.status)),
                                       server: ConnectionWidgetStatus.serverName))
    }
  }
}

struct ConnectionStatusView: View {
  @Environment(\.widgetFamily) private var family
  let entry: ConnectionStatusEntry
  private var shown: ConnectionWidgetStatus.Presentation { ConnectionWidgetStatus.presentation(state: entry.state, server: entry.server) }
  var body: some View {
    // Lock Screen: no button; a tap opens the app.
    if family == .accessoryRectangular {
      VStack(alignment: .leading, spacing: 2) {
        Text(shown.status).font(.headline).widgetAccentable()
        Text(shown.server).font(.caption).lineLimit(2)
      }.frame(maxWidth: .infinity, alignment: .leading)
    } else {
      VStack(alignment: .leading, spacing: 6) {
        HStack(spacing: 6) {
          Circle().fill(dot).frame(width: 8, height: 8).accessibilityHidden(true)
          Text(shown.status).font(.caption.weight(.semibold)).foregroundStyle(.secondary)
        }
        Text(shown.server).font(.headline).lineLimit(2)
        Spacer(minLength: 0)
        // The Control Center toggle's intent, which runs in the app as its own buttons do.
        Button(intent: intent) { Text(shown.action).font(.subheadline.weight(.semibold)).frame(maxWidth: .infinity) }
          .buttonStyle(.borderedProminent)
      }.frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .leading)
    }
  }
  private var dot: Color {
    switch entry.state {
    case .connected: .green
    case .connecting: .orange
    case .disconnected: .gray
    }
  }
  private var intent: ConnectionControlIntent {
    // The parameter stores its value by reference, so the intent needs no var.
    let intent = ConnectionControlIntent()
    intent.value = shown.connects
    return intent
  }
}
