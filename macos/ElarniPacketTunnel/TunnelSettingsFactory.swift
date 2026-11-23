import Foundation
import NetworkExtension

enum TunnelSettingsFactory {
  /// LAN discovery (multicast, the limited broadcast) reaches no device through the tunnel, so it
  /// stays on the physical network unless the kill switch keeps everything inside.
  static let discoveryRoutes = ["224.0.0.0/4", "255.255.255.255/32", "ff00::/8"]
  /// The tunnel's own address ranges, as `make` sets them: a connection from them runs through it.
  static let tunnelPrefixes = ["192.0.2.0/30", "2001:db8::/126"]

  /// What a custom route list always carries: the tunnel's DNS addresses (198.18.0.1 is inside the
  /// first range) and the ranges FakeDNS answers from. Without them no name resolves, or every name
  /// resolves to an address that leaves by the physical network.
  static let requiredRoutes = ["198.18.0.0/15", "fc00::/18", "2001:2::1/128"]

  /// The included routes with the required ones a list does not cover; the IPv6 ones join a list
  /// that routes IPv6.
  static func routed(_ included: [String]) -> [String] {
    let routesIPv6 = included.contains { $0.contains(":") }
    return included + requiredRoutes.filter { needed in
      (routesIPv6 || !needed.contains(":")) && !included.contains { covers($0, needed) }
    }
  }

  private static func covers(_ outer: String, _ inner: String) -> Bool {
    func parse(_ cidr: String) -> (bytes: [UInt8], bits: Int)? {
      let parts = cidr.split(separator: "/")
      guard parts.count == 2, let bits = Int(parts[1]) else { return nil }
      let ipv6 = parts[0].contains(":")
      var bytes = [UInt8](repeating: 0, count: ipv6 ? 16 : 4)
      guard inet_pton(ipv6 ? AF_INET6 : AF_INET, String(parts[0]), &bytes) == 1, (0...bytes.count * 8).contains(bits) else { return nil }
      return (bytes, bits)
    }
    guard let a = parse(outer), let b = parse(inner), a.bytes.count == b.bytes.count, a.bits <= b.bits else { return false }
    return (0..<a.bits).allSatisfy { bit in
      (a.bytes[bit / 8] ^ b.bytes[bit / 8]) & (0x80 >> UInt8(bit % 8)) == 0
    }
  }

  static func make(plan: TunnelPlan, excludedEndpointAddresses: [String], keepDiscovery: Bool = false) throws -> NEPacketTunnelNetworkSettings {
    guard !excludedEndpointAddresses.isEmpty || plan.outboundFormat == .xrayProfile else { throw SettingsError.missingEndpointExclusion }
    let settings = NEPacketTunnelNetworkSettings(tunnelRemoteAddress: "192.0.2.1")
    settings.mtu = NSNumber(value: plan.mtu)
    let runtime = plan.runtimeSettings
    let discovery = keepDiscovery ? [] : discoveryRoutes
    let included = routed(runtime.includedRoutes)

    let ipv4 = NEIPv4Settings(addresses: ["192.0.2.2"], subnetMasks: ["255.255.255.252"])
    ipv4.includedRoutes = included.compactMap(ipv4Route)
    let ipv4Exclusions = excludedEndpointAddresses + runtime.excludedRoutes +
      (runtime.hideVPNIcon ? ["0.0.0.0/8"] : []) + discovery
    ipv4.excludedRoutes = ipv4Exclusions.compactMap { address in
      if address.contains("/") { return ipv4Route(address) }
      guard address.contains(".") else { return nil }
      return NEIPv4Route(destinationAddress: address, subnetMask: "255.255.255.255")
    }
    // Preferred IP only picks the server address family; routing both families
    // keeps the other one from leaking around the tunnel.
    settings.ipv4Settings = ipv4

    let ipv6 = NEIPv6Settings(addresses: ["2001:db8::2"], networkPrefixLengths: [126])
    ipv6.includedRoutes = included.compactMap(ipv6Route)
    let ipv6Exclusions = excludedEndpointAddresses + runtime.excludedRoutes +
      (runtime.hideVPNIcon ? ["::/128"] : []) + discovery
    ipv6.excludedRoutes = ipv6Exclusions.compactMap { address in
      if address.contains("/") { return ipv6Route(address) }
      guard address.contains(":") else { return nil }
      return NEIPv6Route(destinationAddress: address, networkPrefixLength: 128)
    }
    settings.ipv6Settings = ipv6

    let dns = NEDNSSettings(servers: plan.dnsServers)
    dns.matchDomains = [""]
    settings.dnsSettings = dns
    return settings
  }

  static func endpointAddresses(_ candidates: [String], strategy: String, preferredIP: String) -> [String] {
    let ipv4 = candidates.filter { !$0.contains(":") }
    let ipv6 = candidates.filter { $0.contains(":") }
    if preferredIP == "ipv4" { return ipv4 }
    if preferredIP == "ipv6" { return ipv6 }
    switch strategy.lowercased() {
    case "forceipv4": return ipv4
    case "forceipv6": return ipv6
    case "useipv4": return ipv4.isEmpty ? candidates : ipv4
    case "useipv6": return ipv6.isEmpty ? candidates : ipv6
    case "useipv4v6", "forceipv4v6": return ipv4 + ipv6
    case "useipv6v4", "forceipv6v4": return ipv6 + ipv4
    default: return candidates
    }
  }

  private static func ipv4Route(_ cidr: String) -> NEIPv4Route? {
    let parts = cidr.split(separator: "/")
    guard parts.count == 2, parts[0].contains("."),
          let prefix = Int(parts[1]), (0...32).contains(prefix) else { return nil }
    let mask = (0..<4).map { byte -> String in
      let bits = min(8, max(0, prefix - byte * 8))
      return String(bits == 0 ? 0 : 256 - (1 << (8 - bits)))
    }.joined(separator: ".")
    return NEIPv4Route(destinationAddress: String(parts[0]), subnetMask: mask)
  }

  private static func ipv6Route(_ cidr: String) -> NEIPv6Route? {
    let parts = cidr.split(separator: "/")
    guard parts.count == 2, parts[0].contains(":"),
          let prefix = Int(parts[1]), (0...128).contains(prefix) else { return nil }
    return NEIPv6Route(destinationAddress: String(parts[0]), networkPrefixLength: NSNumber(value: prefix))
  }
}

enum SettingsError: Error { case missingEndpointExclusion }
