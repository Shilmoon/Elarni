import Foundation

/// An undecodable "base64:" title is absent, as on Android and desktop.
func decodedProfileTitle(_ value: String) -> String? {
  let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
  guard trimmed.lowercased().hasPrefix("base64:") else { return trimmed }
  return decodedBase64Text(String(trimmed.dropFirst("base64:".count)))
}

/// Every app decodes provider "base64:" texts in the Rust domain library, in either alphabet, padded or not.
func decodedBase64Text(_ encoded: String) -> String? {
  (try? RoutingDomainDecoder.request("decodeBase64Text", payload: ["text": encoded]))?["text"] as? String
}

/// The SNI a probe's handshake sends: bulk tests run at most two probes per SNI,
/// as TSPU counts handshakes per SNI. TLS without a name sends the address;
/// ShadowQUIC keeps its name in its own settings.
func probeKey(outbound data: Data) -> String? {
  guard let root = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return nil }
  for outbound in root["outbounds"] as? [[String: Any]] ?? [root] {
    let stream = outbound["streamSettings"] as? [String: Any]
    let settings = outbound["settings"] as? [String: Any]
    let server = ((settings?["vnext"] ?? settings?["servers"]) as? [[String: Any]])?.first
    let named = ["realitySettings", "tlsSettings"].lazy
      .compactMap { (stream?[$0] as? [String: Any])?["serverName"] as? String }.first { !$0.isEmpty }
    let address = ["tls", "reality"].contains(stream?["security"] as? String ?? "")
      ? settings?["address"] as? String ?? server?["address"] as? String : nil
    let own = outbound["protocol"] as? String == "shadowquic" ? settings?["serverName"] as? String : nil
    if let key = named ?? own.flatMap({ $0.isEmpty ? nil : $0 }) ?? address, !key.isEmpty { return key.lowercased() }
  }
  return nil
}

/// TSPU freezes an SNI after more than 3 near-simultaneous handshakes, so bulk
/// tests run at most two probes per key at a time, in the given order otherwise.
struct ProbeLanes<Item> {
  private var queues: [(key: String, items: [Item])] = []
  private var busy: [String: Int] = [:]

  init(_ items: [Item], key: (Item) -> String) {
    var index: [String: Int] = [:]
    for item in items {
      let name = key(item)
      if let position = index[name] { queues[position].items.append(item) }
      else { index[name] = queues.count; queues.append((name, [item])) }
    }
  }

  mutating func next() -> (key: String, item: Item)? {
    guard let position = queues.firstIndex(where: { busy[$0.key, default: 0] < 2 }) else { return nil }
    let key = queues[position].key
    let item = queues[position].items.removeFirst()
    if queues[position].items.isEmpty { queues.remove(at: position) }
    busy[key, default: 0] += 1
    return (key, item)
  }

  mutating func finish(_ key: String) { busy[key, default: 1] -= 1 }
}

func prioritizingSelection(_ ids: [String], selected: String?) -> [String] {
  guard let selected, let index = ids.firstIndex(of: selected), index != 0 else { return ids }
  var result = ids
  result.swapAt(0, index)
  return result
}

struct SubscriptionNodeMatchKey: Hashable {
  let name: String
  let protocolName: String
  let transport: String
  let security: String
}

func uniqueNodeIDs(
  _ values: [(key: SubscriptionNodeMatchKey, id: String)]
) -> [SubscriptionNodeMatchKey: String] {
  var grouped: [SubscriptionNodeMatchKey: [String]] = [:]
  for value in values { grouped[value.key, default: []].append(value.id) }
  return grouped.compactMapValues { $0.count == 1 ? $0[0] : nil }
}

/// Pairs a refresh's unmatched positions with stored node IDs by a key: a key that occurs as often on both
/// sides pairs, any other count is left to the later stages. Within a key, a name that occurs once on each side
/// pairs first: a provider that lists one server twice, with two SNIs, names the copies apart and may swap them.
/// The rest pair in feed order, the stored nodes in the order of the feed they came from (`order`), not the
/// order of the visible and hidden lists.
func pairedByStableIdentity(
  fresh: [(position: Int, key: String, name: String)],
  stale: [(id: String, key: String, name: String, order: Int)]
) -> [Int: String] {
  var olds: [String: [(id: String, name: String)]] = [:]
  let ordered = stale.enumerated().sorted { ($0.element.order, $0.offset) < ($1.element.order, $1.offset) }
  for (_, value) in ordered { olds[value.key, default: []].append((id: value.id, name: value.name)) }
  var positions: [String: [(position: Int, name: String)]] = [:]
  for value in fresh { positions[value.key, default: []].append((position: value.position, name: value.name)) }
  var matches: [Int: String] = [:]
  for (key, items) in positions {
    guard let same = olds[key], same.count == items.count else { continue }
    let newNames = Dictionary(grouping: items, by: { $0.name }).mapValues(\.count)
    let oldNames = Dictionary(grouping: same, by: { $0.name }).mapValues(\.count)
    var byName: [String: Int] = [:]
    for (index, old) in same.enumerated() where oldNames[old.name] == 1 { byName[old.name] = index }
    var taken = Set<Int>()
    var rest: [Int] = []
    for item in items {
      if newNames[item.name] == 1, let index = byName[item.name] {
        matches[item.position] = same[index].id
        taken.insert(index)
      } else {
        rest.append(item.position)
      }
    }
    for (position, index) in zip(rest, same.indices.filter { !taken.contains($0) }) { matches[position] = same[index].id }
  }
  return matches
}
