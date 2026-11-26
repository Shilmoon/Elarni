import Foundation
import NetworkExtension

protocol XrayCore: AnyObject, Sendable {
  func prepare(
    plan: TunnelPlan,
    endpoint: inout SecretValue,
    servers: [String: SecretValue],
    inboundCredentials: InboundCredentials?,
    attempt: ProviderBoundaryAttempt
  ) async throws -> PreparedXray
  func start(_ prepared: PreparedXray, attempt: ProviderBoundaryAttempt) async throws
  func stop() async throws
  func isRunning() async -> Bool
  func metrics() async -> [String: UInt64]
  // A request through the running core's routing, the way app traffic goes.
  func passesTraffic(url: String, timeout: Int) async -> Bool
  /// A transfer check through the running core: "ok", "stalled" or "inconclusive" (see `ProviderTransferCheck`).
  func transfer(url: String, timeout: Int, idleMs: Int?) async -> String
  /// Tells the running core its network moved, so it checks pooled connections and dials new ones on the
  /// current path. False when the core cannot take the signal.
  func socketPathChanged() async -> Bool
  /// Lists the listed apps' connections that were open before the tunnel started and still run
  /// outside it, for the connections view; macOS lets no program reset another's connections.
  func moveOpenedConnections() async
  #if DEBUG
  func injectExit()
  #endif
}

struct PreparedXray: Sendable {
  let config: String
  let addresses: [String]
  let memoryLimitBytes: Int
  /// Digest of everything that produced `config`; equal digests mean an equal configuration.
  var source: Data? = nil
}

protocol PacketBridge: AnyObject, Sendable {
  func start(
    socksPort: UInt16,
    mtu: UInt16,
    tcpMaxConnections: Int,
    udpMaxConnections: Int,
    inboundCredentials: InboundCredentials?,
    attempt: ProviderBoundaryAttempt
  ) async throws
  func submit(_ packet: Data) throws
  func receive() -> Data?
  /// Blocks until receive() has a packet (true), the timeout passes (false) or the bridge stopped (nil).
  func awaitOutput(timeout: Duration) -> Bool?
  func stop(deadline: Duration) async throws
  var counters: [String: UInt64] { get }
}

protocol PacketFlow: AnyObject {
  func readPacketObjects(completionHandler: @escaping @Sendable ([NEPacket]) -> Void)
  func writePacketObjects(_ packets: [NEPacket]) -> Bool
}

extension NEPacketTunnelFlow: PacketFlow {}

final class PacketFlowPump: @unchecked Sendable {
  private let flow: PacketFlow
  private let bridge: PacketBridge
  private let onFailure: (Error) -> Void
  // NetworkExtension may refuse writes while settings are re-applied (well under a second); refusals
  // that last this long, across at least two batches, mean the tunnel is gone.
  private let rejectionGrace: TimeInterval
  private let lock = NSLock()
  private var running = false
  private var generation: UInt64 = 0

  init(flow: PacketFlow, bridge: PacketBridge, rejectionGrace: TimeInterval = 5, onFailure: @escaping (Error) -> Void) {
    self.flow = flow
    self.bridge = bridge
    self.rejectionGrace = rejectionGrace
    self.onFailure = onFailure
  }

  func start() {
    let started = lock.withLock { () -> UInt64? in
      guard !self.running else { return nil }
      self.generation &+= 1
      self.running = true
      return self.generation
    }
    guard let started else { return }
    // The output thread sleeps in the bridge until a packet is ready: an idle tunnel wakes once a
    // minute instead of polling. A stopped pump's thread ends at its next wake, a stopped bridge's at once.
    let output = Thread { [weak self] in
      var rejectedSince: TimeInterval?
      while let self, self.isCurrent(started), let ready = self.bridge.awaitOutput(timeout: .seconds(60)) {
        // A thread's own pool drains only when the thread ends; without one per wake every written NEPacket stays alive.
        if ready { autoreleasepool { self.drainOutput(generation: started, rejectedSince: &rejectedSince) } }
      }
    }
    output.name = "com.elarni.packet-output"
    output.qualityOfService = .userInitiated
    output.start()
    readNextBatch(generation: started)
  }

  func stop() {
    lock.withLock { self.running = false }
  }

  private func isCurrent(_ generation: UInt64) -> Bool {
    lock.withLock { self.running && self.generation == generation }
  }

  // The pump's lock covers only the generation check: the bridge locks itself, and holding this one
  // across submit or a write would make each direction wait for the other. A submit that races a stop
  // throws notRunning, which fail() ignores for a stopped pump.
  private func readNextBatch(generation: UInt64) {
    guard isCurrent(generation) else { return }
    flow.readPacketObjects { [weak self] packets in
      guard let self, self.isCurrent(generation) else { return }
      do {
        for packet in packets { try self.bridge.submit(packet.data) }
      } catch {
        self.fail(error, generation: generation)
        return
      }
      self.readNextBatch(generation: generation)
    }
  }

  // Writes up to 256 packets; the bridge reports more at once on the next wait. A refused batch is
  // dropped like a congested link drops it; only refusals for rejectionGrace fail the pump.
  private func drainOutput(generation: UInt64, rejectedSince: inout TimeInterval?) {
    guard isCurrent(generation) else { return }
    var output = [NEPacket]()
    while output.count < 256, let data = bridge.receive() {
      let family = data.first.map { $0 >> 4 } == 6 ? AF_INET6 : AF_INET
      output.append(NEPacket(data: data, protocolFamily: sa_family_t(family)))
    }
    guard !output.isEmpty else { return }
    if flow.writePacketObjects(output) {
      rejectedSince = nil
      return
    }
    let now = ProcessInfo.processInfo.systemUptime
    DiagnosticLog.record("packet_write_rejected", ["packets": Int64(output.count)])
    guard let since = rejectedSince else {
      rejectedSince = now
      return
    }
    if now - since >= rejectionGrace { fail(PacketFlowError.writeRejected, generation: generation) }
  }

  private func fail(_ error: Error, generation: UInt64) {
    let failed = lock.withLock { () -> Bool in
      guard self.running && self.generation == generation else { return false }
      self.running = false
      return true
    }
    if failed { onFailure(error) }
  }
}

enum PacketFlowError: Error { case writeRejected }

// Whether the connected server passes traffic: checked once after Connect, then
// each interval while traffic leaves. An idle tunnel is not checked, even after a
// failed check: nothing needs the server, and each check keeps the radio awake.
// The counters only gate the check; their downlink proves nothing, since SOCKS
// replies and direct routes move it while the server is gone.
struct ProviderLiveness {
  // The failover defaults, as on desktop and Android.
  static let targets = ["https://www.gstatic.com/generate_204", "https://cp.cloudflare.com/generate_204"]
  let interval: TimeInterval
  private(set) var state: String?
  /// Failed checks since the last one that passed.
  private(set) var failures = 0
  private var checked: (at: TimeInterval, uplink: UInt64)?

  init(interval: TimeInterval = 60) { self.interval = interval }

  func due(at now: TimeInterval) -> Bool { checked.map { now - $0.at >= interval } ?? true }

  // An idle interval starts the next interval instead.
  mutating func needsProbe(uplink: UInt64, at now: TimeInterval) -> Bool {
    if let checked, uplink <= checked.uplink {
      self.checked = (now, uplink)
      return false
    }
    return true
  }

  mutating func record(passed: Bool, uplink: UInt64, at now: TimeInterval) {
    state = passed ? "ok" : "noTraffic"
    failures = passed ? 0 : failures + 1
    checked = (now, uplink)
  }
}

// Whether the connected server's network lets a transfer through: small probes pass on a server whose
// network cuts each connection off after about 16 KB, so this check fetches one body and passes at
// 96 KB. It runs 5 s after the core came up and 10 s later again when that one stalled, then every
// 15 min while traffic left or a stall was seen (pages that hang send nothing, and only a passing check
// clears the stall), and at once when the liveness check found no traffic. Two stalls in a
// row confirm; an inconclusive check (connect, TLS or status failure) neither counts nor ends a run.
// A check takes up to about 20 s, so it runs beside the monitor loop, one at a time: the loop keeps its
// 2 s pace and reads the verdict on a later pass.
struct ProviderTransferCheck {
  static let ok = "ok", stalled = "stalled", inconclusive = "inconclusive"
  /// Public bodies of at least 128 KB, from the operators of the liveness targets, as on desktop and
  /// Android; tests point them at a fixture.
  static let defaultTargets = ["https://ajax.googleapis.com/ajax/libs/jquery/3.7.1/jquery.js",
                               "https://cdnjs.cloudflare.com/ajax/libs/jquery/3.7.1/jquery.js"]
  static var targets = defaultTargets
  /// The core's 4 s limit without a byte unless a test shortens it.
  static var idleMs: Int?
  /// Seconds from the core's start to its first check, from a first stall to the next, and between later checks.
  let delay: TimeInterval, recheck: TimeInterval, interval: TimeInterval
  private(set) var stalls = 0
  var confirmed: Bool { stalls >= 2 }
  /// The last check that answered passed; recovery restarts a stalled server again only after one.
  private(set) var passed = false
  private var dueAt: TimeInterval?
  private var uplink: UInt64?
  private var now = false
  private var inFlight: Pending?

  /// The verdict of the check running beside the loop; the check sets it from its own task.
  final class Pending: @unchecked Sendable {
    private let lock = NSLock()
    private var verdict: String?

    func finish(_ value: String) { lock.withLock { verdict = value } }

    fileprivate func take() -> String? {
      lock.withLock {
        defer { verdict = nil }
        return verdict
      }
    }
  }

  init(delay: TimeInterval = 5, recheck: TimeInterval = 10, interval: TimeInterval = 900) {
    self.delay = delay
    self.recheck = recheck
    self.interval = interval
  }

  /// A core came up: its first check is `delay` away.
  mutating func started(at time: TimeInterval) {
    dueAt = time + delay
    uplink = nil
    stalls = 0
    passed = false
    now = false
    inFlight = nil
  }

  /// The liveness check found no traffic: the next round checks.
  mutating func noTraffic() { if dueAt != nil { now = true } }

  /// No second check starts while one runs.
  func due(at time: TimeInterval) -> Bool { inFlight == nil && (dueAt.map { now || time >= $0 } ?? false) }

  /// A check starts; its task finishes the returned verdict.
  mutating func begin() -> Pending {
    let pending = Pending()
    inFlight = pending
    return pending
  }

  /// The verdict of the check that ended since the last pass, once; nil while it runs.
  mutating func takeVerdict() -> String? {
    guard let verdict = inFlight?.take() else { return nil }
    inFlight = nil
    return verdict
  }

  // A 15 min check with no stall seen, finding no new traffic, waits another interval instead; the
  // first check and those after a stall do not wait for traffic.
  mutating func needsCheck(uplink: UInt64, at time: TimeInterval) -> Bool {
    if !now, stalls == 0, let last = self.uplink, uplink <= last {
      dueAt = time + interval
      self.uplink = uplink
      return false
    }
    return true
  }

  /// Records a check that ended at `time`, with the uplink counter after it; true when it confirmed a stall.
  mutating func record(verdict: String, uplink: UInt64, at time: TimeInterval) -> Bool {
    now = false
    if verdict == Self.ok { stalls = 0; passed = true }
    if verdict == Self.stalled { stalls += 1; passed = false }
    dueAt = time + (verdict == Self.stalled && stalls == 1 ? recheck : interval)
    self.uplink = uplink
    return verdict == Self.stalled && stalls == 2
  }

  /// One check over its targets' verdicts: one ok wins, and it stalled only when each target that answered stalled.
  static func combine(_ verdicts: [String]) -> String {
    verdicts.contains(ok) ? ok : verdicts.contains(stalled) ? stalled : inconclusive
  }
}

private extension NSLock {
  func withLock<T>(_ body: () throws -> T) rethrows -> T {
    lock()
    defer { unlock() }
    return try body()
  }
}
