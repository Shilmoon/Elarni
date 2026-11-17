import Foundation
import NetworkExtension
import WidgetKit

/// The Home Screen and Lock Screen widget's state and texts, built into the app and the widget extension.
/// The app saves the selected server's name in the App Group; the widget reads the VPN state as the Control Center toggle does.
enum ConnectionWidgetStatus {
  static let kind = "ConnectionStatus"
  private static let serverKey = "ElarniSelectedServerName"

  enum State: Equatable { case connected, connecting, disconnected }

  struct Presentation: Equatable {
    let status: String
    let server: String
    let action: String
    /// What the button asks for: true connects, false disconnects.
    let connects: Bool
  }

  /// As the Control Center toggle: a VPN that runs or starts counts, a stopping one does not.
  static func state(_ statuses: [NEVPNStatus]) -> State {
    if statuses.contains(.connected) { return .connected }
    return statuses.contains { $0 == .connecting || $0 == .reasserting } ? .connecting : .disconnected
  }

  static func presentation(state: State, server: String?) -> Presentation {
    let name = server.flatMap { $0.isEmpty ? nil : $0 } ?? String(localized: "No server selected")
    switch state {
    case .connected: return Presentation(status: String(localized: "Connected"), server: name, action: String(localized: "Disconnect"), connects: false)
    case .connecting: return Presentation(status: String(localized: "Connecting…"), server: name, action: String(localized: "Disconnect"), connects: false)
    case .disconnected: return Presentation(status: String(localized: "Disconnected"), server: name, action: String(localized: "Connect"), connects: true)
    }
  }

  private static var defaults: UserDefaults? {
    UserDefaults(suiteName: Bundle.main.object(forInfoDictionaryKey: "ElarniAppGroup") as? String ?? "group.com.elarni.ios")
  }

  static var serverName: String? { defaults?.string(forKey: serverKey) }

  /// The app's side: saves a changed name, nil for none, and has the widget redraw.
  static func publish(serverName name: String?) {
    guard let defaults, defaults.string(forKey: serverKey) != name else { return }
    defaults.set(name, forKey: serverKey)
    WidgetCenter.shared.reloadTimelines(ofKind: kind)
  }
}
