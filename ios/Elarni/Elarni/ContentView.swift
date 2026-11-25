import SwiftUI
import UniformTypeIdentifiers

struct ContentView: View {
  @Environment(IOSAppModel.self) private var model
  @Environment(\.scenePhase) private var scenePhase
  @State private var camera = false
  @State private var scannedText: String?
  @State private var file = false
  @State private var readingClipboard = false
  @Environment(\.verticalSizeClass) private var verticalSizeClass
  var body: some View {
    presentedContent.appErrors()
  }
  // An iPhone on its side is short: the tabs move to a rail at the leading edge, as on Android,
  // and leave the height to the content.
  private var rail: Bool { verticalSizeClass == .compact }
  @ViewBuilder private var tabs: some View {
    @Bindable var model = model
    HStack(spacing: 0) {
      if rail { TabRail(selection: $model.selectedTab) }
      tabView
    }
  }
  private var tabView: some View {
    @Bindable var model = model
    return TabView(selection: $model.selectedTab) {
      NavigationStack { HomeView(importActions: ImportActions(paste: importClipboard, scan: { camera = true }, file: { file = true })).toolbar { importToolbar; brandToolbar } }.toolbarVisibility(rail ? .hidden : .automatic, for: .tabBar).tabItem { Label("Home", systemImage: "house") }.tag(0)
      NavigationStack { SubscriptionsView().toolbar { importToolbar } }.toolbarVisibility(rail ? .hidden : .automatic, for: .tabBar).tabItem { Label("Subscriptions", systemImage: "tray.full") }.tag(2)
      NavigationStack { RoutingView().toolbar { importToolbar } }.toolbarVisibility(rail ? .hidden : .automatic, for: .tabBar).tabItem { Label("Routing", systemImage: "arrow.triangle.branch") }.tag(3)
      NavigationStack { SettingsView() }.toolbarVisibility(rail ? .hidden : .automatic, for: .tabBar).tabItem { Label("Settings", systemImage: "gearshape") }.tag(4)
    }
  }
  private var presentedContent: some View {
    tabs
    .tint(appTint(model.preferences.accent))
    .accentColor(appTint(model.preferences.accent))
    .foregroundStyle(AppPalette.text)
    .toolbarBackground(AppPalette.inset, for: .navigationBar, .tabBar)
    .preferredColorScheme(model.preferences.theme == "system" ? nil : model.preferences.theme == "dark" ? .dark : .light)
    .task { await model.start(); model.setForeground(scenePhase == .active) }
    .onChange(of: scenePhase) { _, phase in model.setForeground(phase == .active) }
    .confirmationDialog("Import this link?", isPresented: Binding(get: { model.incomingText != nil }, set: { if !$0 { model.incomingText = nil } }), titleVisibility: .visible, presenting: model.incomingText) { value in
      Button("Import") { importText(value) }
      Button("Cancel", role: .cancel) {}
    } message: { _ in Text("A link or file wants to add profiles to Elarni.") }
    .confirmationDialog("Disconnect Elarni?", isPresented: Binding(get: { model.linkStopRequested }, set: { model.linkStopRequested = $0 }), titleVisibility: .visible) {
      Button("Disconnect", role: .destructive) { model.linkStopRequested = false; Task { await model.disconnect() } }
      Button("Cancel", role: .cancel) { model.linkStopRequested = false }
    } message: { Text("A link asks Elarni to disconnect.") }
    .sheet(isPresented: $camera, onDismiss: {
      if let scannedText { self.scannedText = nil; importText(scannedText) }
    }) { QRScannerView { scannedText = $0; camera = false } }
    .fileImporter(isPresented: $file, allowedContentTypes: [.text, .json, .data]) { result in
      do { importText(try IOSImport.readText(result.get())) } catch { model.show(error) }
    }
  }
  @ToolbarContentBuilder private var importToolbar: some ToolbarContent {
    ToolbarItem(placement: .topBarTrailing) {
      Menu {
        Button("Import from clipboard", systemImage: "doc.on.clipboard", action: importClipboard)
        Button("Import file", systemImage: "doc") { file = true }
        Button("Scan QR code", systemImage: "qrcode.viewfinder") { camera = true }
      } label: { HStack(spacing: 5) { Image(systemName: "square.and.arrow.down"); Text("Import") }.font(.subheadline.weight(.medium)) }
      .accessibilityIdentifier("import").disabled(!model.loaded || model.busy || readingClipboard)
    }
  }
  private func importText(_ text: String) { model.launch { try await model.importInput(text) } }
  private func importClipboard() {
    guard !readingClipboard else { return }
    readingClipboard = true
    Task {
      defer { readingClipboard = false }
      // UIPasteboard is Sendable in UIKit. Its synchronous read can wait for
      // system paste permission; leave the main actor free during that wait.
      let text = await Task.detached(priority: .userInitiated) { UIPasteboard.general.string }.value
      guard let text, !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
        model.errorMessage = String(localized: "The clipboard is empty. Copy a link or configuration first.")
        return
      }
      importText(text)
    }
  }
  @ToolbarContentBuilder private var brandToolbar: some ToolbarContent {
    if #available(iOS 26.0, *) {
      ToolbarItem(placement: .topBarLeading) { ElarniBrand() }
        .sharedBackgroundVisibility(.hidden)
    } else {
      ToolbarItem(placement: .topBarLeading) { ElarniBrand() }
    }
  }
}

/// The tabs of an iPhone on its side, at the leading edge.
private struct TabRail: View {
  @Environment(IOSAppModel.self) private var model
  @Binding var selection: Int
  private let tabs: [(tag: Int, title: String, image: String)] = [
    (0, String(localized: "Home"), "house"), (2, String(localized: "Subscriptions"), "tray.full"),
    (3, String(localized: "Routing"), "arrow.triangle.branch"), (4, String(localized: "Settings"), "gearshape")]
  var body: some View {
    ScrollView {
      VStack(spacing: 4) {
        ForEach(tabs, id: \.tag) { tab in
          Button { selection = tab.tag } label: {
            VStack(spacing: 3) {
              Image(systemName: tab.image).font(.system(size: 19)).symbolVariant(selection == tab.tag ? .fill : .none)
              Text(tab.title).font(.caption2).lineLimit(1).minimumScaleFactor(0.7)
            }.frame(width: 76).frame(minHeight: 48).contentShape(Rectangle())
          }.buttonStyle(.plain)
            .foregroundStyle(selection == tab.tag ? appTint(model.preferences.accent) : AppPalette.secondary)
            .accessibilityAddTraits(selection == tab.tag ? .isSelected : [])
            .accessibilityIdentifier("rail.\(tab.tag)")
        }
      }.padding(.vertical, 8)
    }.scrollBounceBehavior(.basedOnSize)
      .background(AppPalette.inset.ignoresSafeArea())
      .accessibilityElement(children: .contain).accessibilityLabel("Tabs")
  }
}

/// Import's three sources, which an empty Home offers as its second step.
struct ImportActions {
  var paste: () -> Void = {}
  var scan: () -> Void = {}
  var file: () -> Void = {}
}

struct HomeView: View {
  var importActions = ImportActions()
  @Environment(IOSAppModel.self) private var model
  @Environment(\.openURL) private var openURL
  @Environment(\.accessibilityReduceMotion) private var reduceMotion
  @Environment(\.dynamicTypeSize) private var dynamicTypeSize
  @State private var filter = "all"
  @State private var details: ServerNode?
  @State private var share: SharedText?
  @State private var list = ServerListCache()
  @Environment(\.verticalSizeClass) private var verticalSizeClass
  // Compact, so servers show on the first screen; Troubleshooting and recovery live in Settings.
  var body: some View {
    ScrollViewReader { scroll in
      Group {
        // An iPhone on its side shows the connection beside the servers, as Android does.
        if verticalSizeClass == .compact {
          GeometryReader { geometry in
            HStack(spacing: 0) {
              AppList(sectionSpacing: 8) { connectionSections(scroll: scroll) }.frame(width: min(360, geometry.size.width * 0.38))
              AppList(sectionSpacing: 8) { listSections }
            }
          }
        } else {
          AppList(sectionSpacing: 8) { connectionSections(scroll: scroll); listSections }
        }
      }.navigationTitle("").navigationBarTitleDisplayMode(.inline)
        .refreshable { await model.refreshAll() }
        .sheet(item: $details) { node in NavigationStack { ServerDetailView(nodeID: node.id) } }
        .sheet(item: $share) { ShareView(item: $0) }
    }
  }
  @ViewBuilder private func connectionSections(scroll: ScrollViewProxy) -> some View {
        connectionSection(scroll: scroll)
        if let notice = model.notice { Section { Text(notice); Button("Dismiss") { model.notice = nil } } }
        if model.settings.monitoringEnabled && model.vpn.connected { TrafficSection() }
#if ELARNI_RECORDING
        if model.settings.recordDiagnostics == true {
          Section { Button("Mark a problem now", systemImage: "flag") { model.launch { try await model.markRecording() } } }
        }
#endif
  }
  @ViewBuilder private var listSections: some View {
        serverSections
        if let source = model.selectedSubscription {
          Section("Subscription") {
            NavigationLink { SubscriptionDetailView(sourceID: source.id) } label: {
              Label(source.name, systemImage: "tray.full").font(.headline)
            }
            QuotaView(source: source)
          }
          if let message = source.metadata["announcement"] as? String { Section("Provider notice") { Text(message) } }
        }
  }
  private func connectionSection(scroll: ScrollViewProxy) -> some View {
    Section {
        VStack(spacing: 8) {
          ConnectionArtwork(connected: model.vpn.providerReady && !model.vpn.noTraffic, failed: model.vpn.failed,
                            warning: model.vpn.noTraffic)
          VStack(spacing: 6) {
            Text(model.vpn.stateLabel).font(.headline).multilineTextAlignment(.center).fixedSize(horizontal: false, vertical: true)
              .foregroundStyle(model.vpn.noTraffic ? AppPalette.warning : Color.primary)
            if model.vpn.noTraffic, let since = model.vpn.noTrafficSince {
              Text("The server passed no traffic at \(since.formatted(date: .omitted, time: .shortened)). Checking again each minute.")
                .font(.footnote).foregroundStyle(AppPalette.secondary).multilineTextAlignment(.center).fixedSize(horizontal: false, vertical: true)
            }
            if let selected = model.selected {
              Button { withAnimation(reduceMotion ? nil : .default) { scroll.scrollTo("servers", anchor: .top) } } label: {
                Text(selected.name).font(.title2.bold()).multilineTextAlignment(.center).fixedSize(horizontal: false, vertical: true).foregroundStyle(AppPalette.text)
                  .frame(minHeight: 44)
              }.buttonStyle(.plain).accessibilityIdentifier("selectedServer").accessibilityHint("Scroll to servers")
            } else {
              Text("No server selected").font(.title3.weight(.semibold)).multilineTextAlignment(.center).fixedSize(horizontal: false, vertical: true).foregroundStyle(AppPalette.secondary)
            }
            if let date = model.vpn.connectedAt, model.vpn.connected { Text(date, style: .timer).font(.callout.monospacedDigit()).foregroundStyle(AppPalette.secondary) }
          }
          Button {
            Task { if model.vpn.active || model.busy { await model.disconnect() } else { await model.connect() } }
          } label: {
            Text(model.vpn.active ? (model.vpn.connected ? String(localized: "Disconnect") : String(localized: "Stop")) : model.busy ? String(localized: "Stop") : String(localized: "Connect"))
              .font(.headline).frame(maxWidth: .infinity, minHeight: 44)
          }.buttonStyle(AppPrimaryButtonStyle()).frame(maxWidth: 300)
            .disabled(!model.loaded || (!model.vpn.active && !model.busy && model.selected == nil)).accessibilityIdentifier("connect")
          if model.vpn.connected, let speed = model.traffic.speed {
            let down = Self.perSecond(speed.down), up = Self.perSecond(speed.up)
            Text("↓ \(down)/s   ↑ \(up)/s").font(.callout.monospacedDigit()).foregroundStyle(AppPalette.secondary)
              .accessibilityLabel(Text("Download \(down)/s, upload \(up)/s")).accessibilityIdentifier("trafficSpeed")
          }
          if model.busy { ProgressView().accessibilityLabel("Operation in progress") }
          // As on Android: stops tests and downloads, never the connection.
          if model.busy && model.vpn.connected { Button("Cancel") { model.cancelOperations() }.buttonStyle(.plain).frame(minHeight: 44) }
        }.frame(maxWidth: .infinity)
          .listRowBackground(Color.clear)
    }
  }
  // The traffic formatter, from KB up so that an idle tunnel reads "0 KB" rather than "Zero KB".
  private static let speedFormatter: ByteCountFormatter = {
    let formatter = ByteCountFormatter()
    formatter.countStyle = .binary
    formatter.allowedUnits = [.useKB, .useMB, .useGB, .useTB]
    formatter.allowsNonnumericFormatting = false
    return formatter
  }()
  private static func perSecond(_ bytes: Double) -> String { speedFormatter.string(fromByteCount: Int64(min(bytes, 1e15).rounded())) }
  // Body passes are many; the grouping runs only when what it shows changes.
  private var serverList: ServerListCache {
    let order = model.preferences.serverOrder
    list.update(.init(catalog: model.catalogRevision, filter: filter, order: order,
                      favoritesFirst: model.preferences.favoritesFirst ?? true, pinned: model.preferences.pinned,
                      latencies: order == "latency" ? model.probes.compactMapValues(\.milliseconds) : [:]),
                subscriptions: model.subscriptions, probes: model.probes)
    return list
  }
  @ViewBuilder private var serverSections: some View {
      Section {
        VStack(alignment: .leading, spacing: 8) {
          // The filters take the place of a "Servers" title, which VoiceOver still reads.
          HStack(spacing: 8) {
            if dynamicTypeSize.isAccessibilitySize { serverFilter.pickerStyle(.menu); Spacer() }
            else { serverFilter.pickerStyle(.segmented) }
            Button { model.testServers(shown) } label: {
              Image(systemName: "waveform.path.ecg").frame(width: 44, height: 44)
            }.buttonStyle(.plain).accessibilityLabel("Test visible servers").disabled(model.busy || shown.isEmpty)
          }.accessibilityElement(children: .contain).accessibilityLabel("Servers")
          if !model.testingIDs.isEmpty { Button("Cancel tests", role: .cancel) { model.cancelTests() }.buttonStyle(.plain).frame(minHeight: 44) }
          if let last = lastSuccessful, last.id != model.selectedID {
            Button("Use last successful server") { model.launch { try await model.select(last) } }
              .buttonStyle(.plain).foregroundStyle(appTint(model.preferences.accent)).frame(minHeight: 44)
              .accessibilityHint(last.name).disabled(model.busy)
          }
          // Right under the filters, so the floating tab bar can't cover the hint.
          if model.nodes.isEmpty {
            GettingStarted(actions: importActions).disabled(!model.loaded || model.busy)
          } else if visibleCount == 0 {
            AppEmptyState(title: "No favorites", systemImage: "star", message: "Touch and hold a server to add it to Favorites.")
          }
        }.id("servers").listRowSeparator(.hidden)
      }
      switch filter {
      case "favorites":
        // One list without groups: a favorite is found by its name, not by where it came from.
        if visibleCount > 0 { Section { ForEach(serverList.visible) { serverRow($0, provider: true) } } }
      case "country":
        ForEach(serverList.countries) { country in Section { countryGroup(country, provider: true) } }
      default:
        ForEach(serverList.sources) { source in
          Section {
            DisclosureGroup(isExpanded: open(source.id)) {
              ForEach(source.countries) { countryGroup($0, provider: false) }
            } label: {
              let label = VStack(alignment: .leading, spacing: 2) {
                ViewThatFits(in: .horizontal) {
                  HStack(spacing: 8) { sourceTitle(source); sourceCount(source) }
                  VStack(alignment: .leading, spacing: 4) { sourceTitle(source); sourceCount(source) }
                }
                ProviderNoticeLines(notices: source.notices)
              }.frame(maxWidth: .infinity, minHeight: 44, alignment: .leading).contentShape(Rectangle())
              if let subscription = model.subscriptions.first(where: { $0.id == source.id }) { label.subscriptionMenu(subscription) } else { label }
            }
          }
        }
      }
      Section {
        HStack {
          Text(filter == "favorites" ? String(localized: "Favorites") : filter == "country" ? String(localized: "By country") : String(localized: "All servers"))
          Spacer()
          Text("\(visibleCount) servers")
        }.font(.footnote).foregroundStyle(AppPalette.secondary).listRowSeparator(.hidden)
      }
  }
  private var serverFilter: some View {
    Picker("Show", selection: $filter) { Text("All").tag("all"); Text("Favorites").tag("favorites"); Text("By country").tag("country") }
      .accessibilityIdentifier("serverFilter")
  }
  // Every group starts open; the closed ones are remembered, so a start shows the list as it was left.
  private var closed: [String] { model.preferences.closedGroups ?? [] }
  private func open(_ group: String) -> Binding<Bool> {
    Binding(get: { !closed.contains(group) }, set: { value in
      guard value == closed.contains(group) else { return }
      var next = model.preferences
      next.closedGroups = ServerListCache.toggled(group, closed: closed, subscriptions: model.subscriptions.map(\.id))
      do { try model.savePreferences(next) } catch { model.show(error) }
    })
  }
  /// The servers on screen: those of the open groups.
  private var shown: [ServerNode] {
    switch filter {
    case "favorites": serverList.visible
    case "country": serverList.countries.filter { !closed.contains($0.id) }.flatMap(\.nodes)
    default: serverList.sources.filter { !closed.contains($0.id) }.flatMap { $0.countries.filter { !closed.contains($0.id) }.flatMap(\.nodes) }
    }
  }
  private func sourceTitle(_ source: ServerListCache.Source) -> some View {
    Text(source.name).font(.headline).foregroundStyle(AppPalette.text).accessibilityIdentifier("source.\(source.name)")
  }
  private func sourceCount(_ source: ServerListCache.Source) -> some View {
    Text("· \(source.countries.reduce(0) { $0 + $1.nodes.count }) servers").font(.subheadline).foregroundStyle(AppPalette.secondary)
  }
  // Under a subscription a row leaves its name out; across subscriptions it tells them apart.
  private func countryGroup(_ group: ServerListCache.Country, provider: Bool) -> some View {
    DisclosureGroup(isExpanded: open(group.id)) {
      ForEach(group.nodes) { serverRow($0, provider: provider) }
    } label: {
      HStack(spacing: 12) {
        LocationFlag(location: group.location)
        ViewThatFits(in: .horizontal) {
          HStack(spacing: 8) { countryTitle(group); countryCount(group); countryPing(group) }
          VStack(alignment: .leading, spacing: 4) { countryTitle(group); HStack(spacing: 8) { countryCount(group); countryPing(group) } }
        }
      }.frame(minHeight: 44)
    }
      .listRowBackground(group.nodes.contains { $0.id == model.selectedID } ? appTint(model.preferences.accent).opacity(0.08) : AppPalette.surface)
  }
  private func serverRow(_ node: ServerNode, provider: Bool) -> some View {
    HStack(spacing: 4) {
      Button {
        if node.format == "telegram" { do { openURL(try model.telegramURL(node)) } catch { model.show(error) } }
        else { model.launch { try await model.select(node) } }
      } label: {
        ServerRow(node: node, selected: model.selectedID == node.id,
                  provider: provider ? model.subscriptions.first { $0.id == node.subscriptionID }?.name : nil, compact: true)
      }
      .buttonStyle(.plain)
      .accessibilityIdentifier("server.\(node.name)").disabled(model.busy)
      .contextMenu {
        Button("Details", systemImage: "info.circle") { details = node }
        Button(node.favorite ? String(localized: "Remove favorite") : String(localized: "Favorite"), systemImage: "star") { model.launch { try model.setFavorite(node) } }
        Button("Test server", systemImage: "waveform.path.ecg") { model.testServers([node]) }
        if node.format != "telegram" { Button("Check exit", systemImage: "globe") { model.checkExit(node) } }
        Button("Share", systemImage: "square.and.arrow.up") { shareNode(node) }
      }
      // The same details as a long press or a swipe, for those who never try one.
      Button { details = node } label: { Label("Details", systemImage: "info.circle").labelStyle(.iconOnly) }
        .buttonStyle(.borderless).frame(minWidth: 44, minHeight: 44)
        .accessibilityLabel("\(node.name) details").accessibilityIdentifier("details.\(node.name)")
    }
    .listRowBackground(model.selectedID == node.id ? appTint(model.preferences.accent).opacity(0.12) : AppPalette.surface)
    .swipeActions(edge: .trailing, allowsFullSwipe: false) { Button("Details", systemImage: "info.circle") { details = node }.tint(.accentColor) }
    .swipeActions(edge: .leading) { Button(node.favorite ? String(localized: "Unfavorite") : String(localized: "Favorite"), systemImage: "star") { model.launch { try model.setFavorite(node) } }.tint(.orange) }
  }
  private func countryTitle(_ group: ServerListCache.Country) -> some View {
    Text(group.location?.name ?? String(localized: "Other locations")).font(.headline).foregroundStyle(AppPalette.text)
      .accessibilityIdentifier("country.\(group.code)")
  }
  private func countryCount(_ group: ServerListCache.Country) -> some View {
    Text("· \(group.nodes.count) servers").font(.subheadline).foregroundStyle(AppPalette.secondary)
  }
  @ViewBuilder private func countryPing(_ group: ServerListCache.Country) -> some View {
    if let best = group.nodes.compactMap({ model.probes[$0.id]?.currentMilliseconds(for: $0) }).min() {
      LatencyBadge(milliseconds: best, label: String(localized: "Lowest latency: \(best) milliseconds"))
        .accessibilityIdentifier("country-ping.\(group.code)")
    }
  }
  private var lastSuccessful: ServerNode? {
    model.nodes.filter { $0.successCurrent && $0.format != "telegram" }.max { ($0.lastSuccess ?? .distantPast) < ($1.lastSuccess ?? .distantPast) }
      .flatMap { $0.lastSuccess == nil ? nil : $0 }
  }
  private var visibleCount: Int { serverList.visible.count }
  private func shareNode(_ node: ServerNode) {
    do { share = SharedText(title: node.name, text: try model.share(id: node.id, subscription: false)) } catch { model.show(error) }
  }
}

struct ServerRow: View {
  @Environment(IOSAppModel.self) private var model
  @Environment(\.dynamicTypeSize) private var dynamicTypeSize
  let node: ServerNode
  var selected = false
  var provider: String?
  // Home packs more servers on screen; a subscription's server list keeps the roomier rows.
  var compact = false
  var body: some View {
    Group {
      if dynamicTypeSize.isAccessibilitySize {
        VStack(alignment: .leading, spacing: 8) {
          HStack(spacing: 12) { LocationFlag(location: ServerLocation.fromName(node.name)); serverName }
          serverMetadata
          exitLine
          HStack { serverStatus; Spacer(minLength: 0) }
        }.frame(maxWidth: .infinity, alignment: .leading)
      } else {
        HStack(spacing: 12) {
          LocationFlag(location: ServerLocation.fromName(node.name))
          VStack(alignment: .leading, spacing: compact ? 2 : 4) { serverName; serverMetadata; exitLine }
          Spacer(minLength: 8)
          serverStatus
        }
      }
    }.padding(.vertical, compact ? 0 : 5).frame(minHeight: 44).contentShape(.rect)
  }
  private var serverName: some View {
    Text(ServerLocation.displayName(node.name)).foregroundStyle(AppPalette.text).multilineTextAlignment(.leading).fixedSize(horizontal: false, vertical: true)
  }
  // The provider shares the protocol line, so each server takes two lines.
  private var serverMetadata: some View {
    HStack(spacing: 6) {
      Text(node.protocolName.uppercased()).font(.caption2.weight(.semibold)).fixedSize(horizontal: true, vertical: false)
        .padding(.horizontal, 6).padding(.vertical, 3).background(AppPalette.field, in: RoundedRectangle(cornerRadius: 4))
      Text(([node.transport, node.security].filter { !$0.isEmpty && $0 != "none" } + [provider ?? ""].filter { !$0.isEmpty }).joined(separator: " · "))
        .font(.caption).foregroundStyle(AppPalette.secondary)
        .lineLimit(dynamicTypeSize.isAccessibilitySize ? nil : 1).fixedSize(horizontal: false, vertical: dynamicTypeSize.isAccessibilitySize)
    }
  }
  @ViewBuilder private var exitLine: some View {
    if let exit = model.exits[node.id], exit.revision == node.revision {
      Text("Exit: \(exit.countryName) · \(exit.ip)").font(.caption).foregroundStyle(AppPalette.secondary)
      if let mismatch = exit.mismatch(name: node.name) {
        Text("\(ServerLocation.displayName(node.name))'s name says \(mismatch.named.name), but its exit is in \(mismatch.exit.name).")
          .font(.caption).foregroundStyle(AppPalette.warning).fixedSize(horizontal: false, vertical: true)
      }
    } else if model.exitFailures.contains(node.id) {
      Text("Exit check failed").font(.caption).foregroundStyle(AppPalette.secondary)
    }
  }
  @ViewBuilder private var serverStatus: some View {
    if model.testingIDs.contains(node.id) { ProgressView().accessibilityLabel("Testing") }
    else if let result = model.probes[node.id], result.current(for: node) {
      if let milliseconds = result.currentMilliseconds(for: node) {
        LatencyBadge(milliseconds: milliseconds).accessibilityIdentifier("server-ping.\(node.id)")
      } else { Text(result.statusLabel).font(.caption).foregroundStyle(AppPalette.secondary) }
    }
    if node.favorite { Image(systemName: "star.fill").font(.caption).foregroundStyle(AppPalette.secondary).accessibilityLabel("Favorite") }
    if selected { Image(systemName: "checkmark.circle.fill").foregroundStyle(appTint(model.preferences.accent)).accessibilityLabel("Selected") }
  }
}

struct ServerDetailView: View {
  @Environment(IOSAppModel.self) private var model
  @Environment(\.dismiss) private var dismiss
  let nodeID: String
  @State private var share: SharedText?
  @State private var config: LoadedConfig?
  @State private var renaming = false
  @State private var deleting = false
  @State private var name = ""
  private var local: Bool { model.subscriptions.first { $0.nodes.contains { $0.id == nodeID } }?.local ?? true }
  var body: some View {
    AppList {
      if let node = model.nodes.first(where: { $0.id == nodeID }) {
        Section { LabeledContent("Protocol", value: node.protocolName); LabeledContent("Transport", value: node.transport); LabeledContent("Security", value: node.security) }
        Section {
          Picker("IP preference", selection: Binding(get: { node.ipPreference }, set: { value in model.launch { try await model.setIP(node, value: value) } })) { IPChoices(inherit: true) }
          Toggle("Favorite", isOn: Binding(get: { node.favorite }, set: { _ in model.launch { try model.setFavorite(node) } }))
          if let date = node.lastSuccess { LabeledContent(node.successCurrent ? String(localized: "Last successful test") : String(localized: "Success with older settings")) { Text(date, style: .relative) } }
        }
        if let result = model.probes[node.id] {
          Section("Last test") {
            LabeledContent(result.scope, value: result.milliseconds.map { String(localized: "\($0) ms") } ?? result.statusLabel)
            if let failure = result.failure { Text(failure).foregroundStyle(AppPalette.secondary) }
            LabeledContent(result.current(for: node) ? String(localized: "Tested") : String(localized: "Outdated")) { Text(result.date, style: .relative) }
          }
        }
        if let exit = model.exits[node.id], exit.revision == node.revision {
          Section("Exit") {
            LabeledContent("Reaches the internet in", value: "\(exit.countryName) · \(exit.ip)")
            if let mismatch = exit.mismatch(name: node.name) {
              Text("\(ServerLocation.displayName(node.name))'s name says \(mismatch.named.name), but its exit is in \(mismatch.exit.name).").foregroundStyle(AppPalette.warning)
            }
          }
        }
        ForEach(node.groups.indices, id: \.self) { index in
          let group = node.groups[index]
          let name = group["name"] as? String ?? String(localized: "Group")
          Picker(name, selection: Binding(get: { group["selected"] as? String ?? "" }, set: { value in model.launch { try await model.selectGroup(node, group: name, value: value) } })) {
            ForEach(group["options"] as? [String] ?? [], id: \.self) { Text($0).tag($0) }
          }
        }
        Section {
          Button("Use server") { model.launch { try await model.select(node); dismiss() } }
          Button("Test server") { model.testServers([node]) }
          if node.format != "telegram" { Button("Check exit") { model.checkExit(node) } }
          Button("Share configuration") { do { share = SharedText(title: node.name, text: try model.share(id: node.id, subscription: false)) } catch { model.show(error) } }
          // Refused where sharing is: a provider that hides its servers, or one not refreshed since it could say so.
          if node.format == "xrayProfile" {
            Button("Configuration") { do { config = LoadedConfig(text: try model.nodeConfig(node)) } catch { model.show(error) } }
              .accessibilityIdentifier("server-config")
          }
          Button("Rename") { name = node.name; renaming = true }
          // The selected server and a subscription's last one stay, as the store requires.
          if model.selectedID != node.id, (model.subscriptions.first { $0.id == node.subscriptionID }?.nodes.count ?? 0) > 1 {
            Button(local ? String(localized: "Delete server") : String(localized: "Hide server"), role: .destructive) { deleting = true }
          }
        }.disabled(model.busy)
      }
    }
    .alert("Rename server", isPresented: $renaming) {
      TextField("Name", text: $name)
      Button("Save") { if let node = model.nodes.first(where: { $0.id == nodeID }) { model.launch { try model.renameNode(node, name: name) } } }
      Button("Cancel", role: .cancel) {}
    } message: { Text("Leave the name empty to use the provider’s name.") }
    // A provider's server is hidden, so that its refresh keeps it out.
    .confirmationDialog(local ? String(localized: "Delete this server?") : String(localized: "Hide this server?"), isPresented: $deleting, titleVisibility: .visible) {
      Button(local ? String(localized: "Delete server") : String(localized: "Hide server"), role: .destructive) { if let node = model.nodes.first(where: { $0.id == nodeID }) { model.launch { try model.deleteNode(node); dismiss() } } }
    } message: {
      let name = model.nodes.first { $0.id == nodeID }?.name ?? String(localized: "This server")
      Text(local ? String(localized: "Delete \(name)?") : String(localized: "\(name) stays hidden after refreshes. Show it again from Hidden servers."))
    }
    .navigationTitle(model.nodes.first(where: { $0.id == nodeID })?.name ?? String(localized: "Server")).navigationBarTitleDisplayMode(.inline)
    .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } } }
    .sheet(item: $share) { ShareView(item: $0) }
    .sheet(item: $config) { ServerConfigView(nodeID: nodeID, original: $0.text) }.appErrors()
  }
}

private struct LoadedConfig: Identifiable { let id = UUID(); let text: String }

/// A server's stored Xray JSON, as on desktop and Android. Save checks that it is still one Xray server.
struct ServerConfigView: View {
  @Environment(IOSAppModel.self) private var model
  @Environment(\.dismiss) private var dismiss
  let nodeID: String
  let original: String
  @State private var text: String
  init(nodeID: String, original: String) { self.nodeID = nodeID; self.original = original; _text = State(initialValue: original) }
  private var node: ServerNode? { model.nodes.first { $0.id == nodeID } }
  // A local group has no refresh to replace the edit.
  private var footer: String {
    let rule = String(localized: "The configuration must be one Xray server.")
    guard let source = model.subscriptions.first(where: { $0.id == node?.subscriptionID }), !source.local else { return rule }
    return rule + " " + String(localized: "\(source.name)’s next refresh replaces your edits.")
  }
  var body: some View {
    NavigationStack {
      VStack(alignment: .leading, spacing: 8) {
        // The ASCII keyboard also turns off smart quotes and dashes, which would break the JSON.
        TextEditor(text: $text).font(.body.monospaced()).textInputAutocapitalization(.never).autocorrectionDisabled().keyboardType(.asciiCapable)
          .accessibilityLabel("Configuration").accessibilityIdentifier("server-config-editor")
        Text(footer).font(.footnote).foregroundStyle(AppPalette.secondary).fixedSize(horizontal: false, vertical: true)
      }.padding()
        .navigationTitle("Configuration").navigationBarTitleDisplayMode(.inline)
        .toolbar {
          ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
          ToolbarItem(placement: .confirmationAction) {
            Button("Save") { if let node { model.launch { try await model.replaceNodeConfig(node, text: text); dismiss() } } }
              .disabled(text == original || model.busy || node == nil).accessibilityIdentifier("server-config-save")
          }
        }.interactiveDismissDisabled(model.busy).appErrors()
    }
  }
}
