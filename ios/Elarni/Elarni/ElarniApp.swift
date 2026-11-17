import SwiftUI
import UserNotifications

@main
struct ElarniApp: App {
  @State private var model = IOSAppModel.shared
  init() {
    // Set before launch ends: iOS starts the app in the background to run a notification's action.
    UNUserNotificationCenter.current().delegate = NotificationActions.shared
    IOSOnDemandRestart.registerActions()
  }
  var body: some Scene {
    WindowGroup { ContentView().environment(model).onOpenURL { model.acceptURL($0) } }
  }
}

/// Turn Off, on the notice that On Demand restarted the VPN, runs the app's Stop, which also turns On Demand off.
final class NotificationActions: NSObject, UNUserNotificationCenterDelegate {
  static let shared = NotificationActions()

  @MainActor static func perform(_ action: String) async {
    if action == IOSOnDemandRestart.turnOff { try? await ConnectionIntents.set(false) }
  }

  func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
    await Self.perform(response.actionIdentifier)
  }

  // That notice answers what the user just did, so it shows over the open app; the app reports the other alerts itself.
  func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async -> UNNotificationPresentationOptions {
    notification.request.identifier == IOSOnDemandRestart.identifier ? [.banner, .list, .sound] : []
  }
}
