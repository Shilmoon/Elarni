import Foundation
import Security
import UserNotifications

enum IOSPlatform {
  static var appGroup: String {
    Bundle.main.object(forInfoDictionaryKey: "ElarniAppGroup") as? String ?? "group.com.elarni.ios"
  }

  static var containerURL: URL? {
    FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroup)
  }

  static func keychainAccessGroup() throws -> String {
    guard let value = Bundle.main.object(forInfoDictionaryKey: "ElarniKeychainGroup") as? String,
          !value.isEmpty, !value.contains("$"), value.hasSuffix(".com.elarni.ios.shared") else {
      throw IOSPlatformError.signingRequired
    }
    return value
  }
}

/// Tells the user when the VPN stops without their action. While the tunnel runs, the
/// extension keeps a delayed alert pending and pushes it back, so an extension that iOS
/// ends (memory limit, crash, update) still alerts unless a restarted tunnel clears it.
enum IOSConnectionAlerts {
  static let identifier = "elarni.connection.lost"
  // The pending alert must outlive several refreshes; a missed one is not a disconnect.
  static let refreshSeconds: TimeInterval = 60
  private static let armSeconds: TimeInterval = 180

  static func arm() {
    schedule(String(localized: "Elarni disconnected"), String(localized: "The VPN stopped unexpectedly. Open Elarni to reconnect."), after: armSeconds)
  }

  static func disarm() {
    UNUserNotificationCenter.current().removePendingNotificationRequests(withIdentifiers: [identifier])
  }

  /// A new tunnel retires shown alerts; arming replaces any pending one.
  static func connected() {
    UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: [identifier])
    arm()
  }

  /// For the app, before it starts a tunnel the user asked for.
  static func clear() {
    disarm()
    UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: [identifier])
    IOSOnDemandRestart.forget()
  }

  /// Short delay so an On Demand restart can clear the alert before it shows.
  static func stopped(_ cause: String) {
    schedule(String(localized: "Elarni disconnected"), String(localized: "iOS stopped the VPN because \(cause). Open Elarni to reconnect."), after: 15)
  }

  static func failed() {
    schedule(String(localized: "Elarni connection lost"), String(localized: "Internet traffic is blocked until the VPN reconnects. Open Elarni to reconnect."), after: 1)
  }

  // One identifier: each alert replaces the pending one instead of stacking.
  private static func schedule(_ title: String, _ body: String, after seconds: TimeInterval) {
    let content = UNMutableNotificationContent()
    content.title = title
    content.body = body
    content.sound = .default
    let trigger = UNTimeIntervalNotificationTrigger(timeInterval: seconds, repeats: false)
    UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: identifier, content: content, trigger: trigger)) { _ in }
  }
}

/// iOS starts a VPN with On Demand again when its own VPN switch (Settings, Control Center) stops it,
/// and only the app can turn On Demand off. The app notes whether On Demand is armed, the extension
/// notes such a stop, and the restarted extension tells the user how to turn the VPN off.
enum IOSOnDemandRestart {
  static let identifier = "elarni.ondemand.restart"
  static let category = "elarni.ondemand"
  static let turnOff = "elarni.ondemand.turnoff"
  /// On Demand restarts a stopped VPN with the next connection an app opens; a later start is the user's own.
  static let window: TimeInterval = 60
  // Settable so unit tests can use their own folder; a build without the App Group has none.
  static var directory: URL? = IOSPlatform.containerURL

  static func armed(in directory: URL? = IOSOnDemandRestart.directory) -> Bool {
    guard let directory else { return false }
    return FileManager.default.fileExists(atPath: directory.appendingPathComponent("on-demand-armed").path)
  }

  /// For the app, with each change of the VPN configuration's On Demand state.
  static func setArmed(_ armed: Bool, in directory: URL? = IOSOnDemandRestart.directory) {
    guard let file = directory?.appendingPathComponent("on-demand-armed") else { return }
    if armed { try? Data().write(to: file, options: .atomic) } else { try? FileManager.default.removeItem(at: file) }
  }

  /// For the extension, when the user stops the tunnel. The app turns On Demand off before its own stop.
  static func userStopped(now: Date = Date(), in directory: URL? = IOSOnDemandRestart.directory) {
    guard armed(in: directory), let file = directory?.appendingPathComponent("on-demand-user-stop") else { return }
    try? Data(String(now.timeIntervalSince1970).utf8).write(to: file, options: .atomic)
  }

  /// For the extension, when a tunnel has started: whether On Demand undid the user's stop.
  static func restarted(now: Date = Date(), in directory: URL? = IOSOnDemandRestart.directory) -> Bool {
    guard let file = directory?.appendingPathComponent("on-demand-user-stop"),
          let text = try? String(contentsOf: file, encoding: .utf8), let stopped = Double(text) else { return false }
    try? FileManager.default.removeItem(at: file)
    return armed(in: directory) && (0..<window).contains(now.timeIntervalSince1970 - stopped)
  }

  /// For the app, before it starts a tunnel the user asked for and after its own stop.
  static func forget(in directory: URL? = IOSOnDemandRestart.directory) {
    if let file = directory?.appendingPathComponent("on-demand-user-stop") { try? FileManager.default.removeItem(at: file) }
    UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: [identifier])
  }

  /// The app handles Turn Off; the extension registers it too, for an app not opened since its update.
  static func registerActions() {
    // Not from a locked iPhone: whoever holds it could otherwise take the VPN down.
    let action = UNNotificationAction(identifier: turnOff, title: String(localized: "Turn Off"), options: [.authenticationRequired])
    UNUserNotificationCenter.current().setNotificationCategories([
      UNNotificationCategory(identifier: category, actions: [action], intentIdentifiers: [])])
  }

  static func notify() {
    registerActions()
    let content = UNMutableNotificationContent()
    content.title = String(localized: "Elarni reconnected")
    content.body = String(localized: "iOS’s VPN switch cannot stop Elarni while On Demand is on. Touch and hold this notification and tap Turn Off, or stop the VPN in Elarni.")
    content.sound = .default
    content.categoryIdentifier = category
    UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: identifier, content: content, trigger: nil)) { _ in }
  }
}

enum IOSPlatformError: LocalizedError {
  case signingRequired
  var errorDescription: String? {
    String(localized: "Configure the app and VPN extension with the same Apple Developer team, App Group, and Keychain group in Xcode.")
  }
}
