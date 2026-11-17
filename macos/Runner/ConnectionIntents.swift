import AppIntents

/// Shortcuts and Siri connect and stop through the app's connection links' own code, without the
/// confirmation a link asks before a stop.
@MainActor
enum ConnectionIntents {
  static func perform(_ action: String) async throws {
    let channel = try await windowChannel()
    // As after a link, the window reloads what the action changed.
    defer { channel.channel.invokeMethod("subscriptionsChanged", arguments: nil) }
    do { try await channel.performConnectionAction(action, confirm: false) }
    // Connect finds no saved VPN profile and no selected server to make one from.
    catch RunnerTunnelError.notPrepared { throw ConnectionIntentError.noServer }
    catch RoutingStoreError.noSelectedSubscription { throw ConnectionIntentError.noServer }
    catch { throw ConnectionIntentError.failed }
  }

  /// The action opens the app; on a cold launch its window creates the channel shortly after.
  private static func windowChannel() async throws -> TunnelMethodChannel {
    for _ in 0..<100 {
      if let channel = TunnelMethodChannel.shared { return channel }
      try await Task.sleep(nanoseconds: 50_000_000)
    }
    throw ConnectionIntentError.unavailable
  }
}

enum ConnectionIntentError: Error, CustomLocalizedStringResourceConvertible {
  case noServer, failed, unavailable

  var localizedStringResource: LocalizedStringResource {
    switch self {
    case .noServer: "Select a server in Elarni first."
    case .failed: "Elarni could not change the connection. Try it in the Elarni window to see why."
    case .unavailable: "Elarni did not finish opening. Try again."
    }
  }
}

struct ConnectIntent: AppIntent {
  static let title: LocalizedStringResource = "Connect Elarni"
  static let description = IntentDescription("Connects to the server selected in Elarni.")
  static let openAppWhenRun = true
  @MainActor func perform() async throws -> some IntentResult { try await ConnectionIntents.perform("connect"); return .result() }
}

struct DisconnectIntent: AppIntent {
  static let title: LocalizedStringResource = "Disconnect Elarni"
  static let description = IntentDescription("Disconnects Elarni and keeps On Demand from reconnecting it.")
  static let openAppWhenRun = true
  @MainActor func perform() async throws -> some IntentResult { try await ConnectionIntents.perform("disconnect"); return .result() }
}

struct ToggleConnectionIntent: AppIntent {
  static let title: LocalizedStringResource = "Toggle Elarni"
  static let description = IntentDescription("Disconnects Elarni when it runs, and connects it otherwise.")
  static let openAppWhenRun = true
  @MainActor func perform() async throws -> some IntentResult { try await ConnectionIntents.perform("toggle"); return .result() }
}

struct ElarniShortcuts: AppShortcutsProvider {
  static var appShortcuts: [AppShortcut] {
    AppShortcut(intent: ConnectIntent(), phrases: ["Connect \(.applicationName)"], shortTitle: "Connect", systemImageName: "lock.shield")
    AppShortcut(intent: DisconnectIntent(), phrases: ["Disconnect \(.applicationName)"], shortTitle: "Disconnect", systemImageName: "lock.open")
    AppShortcut(intent: ToggleConnectionIntent(), phrases: ["Toggle \(.applicationName)"], shortTitle: "Toggle", systemImageName: "arrow.triangle.2.circlepath")
  }
}
