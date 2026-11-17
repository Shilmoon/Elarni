import 'dart:convert';
import 'dart:math';

import 'package:flutter/cupertino.dart' show CupertinoIcons;
import 'package:flutter/material.dart';
import 'elarni_widgets.dart';
import 'strings.dart';

/// Russian national zones and the other domains these services use (v2fly
/// domain-list-community); banks, Gosuslugi and marketplaces have no desktop apps to match.
const russianSitesPreset = <String>[
  'domain:ru',
  'domain:xn--p1ai',
  'domain:su',
  'domain:ozon.com',
  'domain:ozonusercontent.com',
  'domain:ozon.kz',
  'domain:wbstatic.net',
  'domain:wbcontent.net',
  'domain:wb.kz',
  'domain:paywb.com',
  'domain:yandex.net',
  'domain:yandex.com',
  'domain:yastatic.net',
  'domain:yastat.net',
  'domain:yandexcloud.net',
  'domain:yandex.cloud',
  'domain:ya.cc',
  'domain:yadi.sk',
  'domain:naydex.net',
  'domain:vk.com',
  'domain:vk.me',
  'domain:userapi.com',
  'domain:vkuser.net',
  'domain:vk-cdn.net',
  'domain:vkuseraudio.net',
  'domain:vkuservideo.net',
  'domain:vkuserlive.net',
  'domain:vkcache.com',
  'domain:mycdn.me',
  'domain:mvk.com',
  'domain:sberbank.com',
  'domain:tbank-online.com',
  'domain:tinkoff-group.com',
  'domain:tinkoffmobile.com',
  'domain:vtb.com',
  'domain:vtb-direct.com',
  'domain:alfa.me',
  'domain:alfabank.st',
];

class RoutingEditor extends StatefulWidget {
  const RoutingEditor({
    super.key,
    required this.json,
    this.onSave,
    this.initialSection = 0,
  });
  final String json;
  final Future<bool> Function(String)? onSave;
  final int initialSection;

  @override
  State<RoutingEditor> createState() => _RoutingEditorState();
}

class _RoutingEditorState extends State<RoutingEditor> {
  late final Map<String, dynamic> _profile;
  final _fields = <String, TextEditingController>{};
  late bool _explicitDNS;
  String? _error;
  bool _saving = false;
  late int _section;
  // Which sections are open; it also restores them when a tab is rebuilt. The tiles carry no
  // PageStorageKey: the text fields inside would read the tile's stored bool as their scroll offset.
  final _expanded = <String>{'Direct', 'Remote'};
  String _dnsName(String role) =>
      role == 'Remote' ? l10n.remoteDns : l10n.domesticDns;

  static const _lists = <String>[
    'DomesticDNSDomains',
    'DirectSites',
    'DirectIp',
    'ProxySites',
    'ProxyIp',
    'BlockSites',
    'BlockIp',
  ];

  /// The geofile URLs by their key, with the alias the decoder also reads.
  static const _geofiles = {'Geoipurl': 'GeoipUrl', 'Geositeurl': 'GeositeUrl'};

  /// The most managed domain lists the decoder accepts.
  static const _maxSources = 125;
  static final _random = Random.secure();

  // The geofile URLs, managed domain lists and static DNS hosts are written
  // back only when edited, so an untouched profile saves as it came.
  final _geofileFields = <String, TextEditingController>{};
  final _geofileTexts = <String, String>{};
  final _sources =
      <({Map<String, dynamic> source, TextEditingController url})>[];
  final _hosts =
      <({TextEditingController host, TextEditingController address})>[];
  // Fields of removed rows, disposed with the editor: the frame that removes
  // a row still has its text field.
  final _removed = <TextEditingController>[];

  @override
  void initState() {
    super.initState();
    _section = widget.initialSection;
    _profile = Map<String, dynamic>.from(jsonDecode(widget.json) as Map);
    for (final MapEntry(key: key, value: alias) in _geofiles.entries) {
      final text = '${_profile[key] ?? _profile[alias] ?? ''}';
      _geofileTexts[key] = text;
      _geofileFields[key] = TextEditingController(text: text);
    }
    if (_profile['ManagedSources'] case final List sources) {
      for (final source in sources) {
        if (source is Map) {
          _sources.add((
            source: Map<String, dynamic>.from(source),
            url: TextEditingController(text: '${source['url'] ?? ''}'),
          ));
        }
      }
    }
    if (_profile['DnsHosts'] case final Map hosts) {
      for (final MapEntry(:key, :value) in hosts.entries) {
        _hosts.add((
          host: TextEditingController(text: '$key'),
          address: TextEditingController(text: '$value'),
        ));
      }
    }
    if (_hosts.isNotEmpty) _expanded.add('hosts');
    _profile['FakeDNS'] ??= _profile['FakeDns'] ?? 'false';
    // As the native decoder does: geofiles are reduced unless the profile opts out.
    _profile['UseChunkFiles'] ??= 'true';
    _explicitDNS = _profile['DomesticDNSDomains'] is List;
    for (final key in <String>[
      'Name',
      'RemoteDNSDomain',
      'RemoteDNSIP',
      'DomesticDNSDomain',
      'DomesticDNSIP',
      ..._lists,
    ]) {
      var value = _profile[key];
      if (key == 'RemoteDNSIP' && (value == null || value == '')) {
        value = _profile['RemoteDNSIp'] ?? _profile['RemoteDns'];
      }
      if (key == 'DomesticDNSIP' && (value == null || value == '')) {
        value = _profile['DomesticDNSIp'] ?? _profile['DomesticDns'];
      }
      _fields[key] = TextEditingController(
        text: value is List ? value.join('\n') : value?.toString() ?? '',
      );
    }
  }

  @override
  void dispose() {
    for (final field in [
      ..._fields.values,
      ..._geofileFields.values,
      for (final source in _sources) source.url,
      for (final host in _hosts) ...[host.host, host.address],
      ..._removed,
    ]) {
      field.dispose();
    }
    super.dispose();
  }

  bool _enabled(String key) => _profile[key] == true || _profile[key] == 'true';

  // Mirrors the per-target checks of native decode_routing: routing.rs
  // validation and the IP rule check in compiler.rs (native/elarni-domain).
  static bool _validIpTarget(String text) {
    // Rust char::is_whitespace; RegExp \s differs at U+0085 and U+FEFF.
    if (RegExp(
          r'[\t-\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]',
        ).hasMatch(text) ||
        utf8.encode(text).length > 512) {
      return false;
    }
    final target = text.replaceFirst(RegExp('^!+'), '');
    if (RegExp('^(geoip|ext|ext-ip):').hasMatch(target)) return true;
    final slash = target.indexOf('/');
    final address = slash < 0 ? target : target.substring(0, slash);
    final List<int> bytes;
    try {
      // Unlike InternetAddress.tryParse, these match Rust's IpAddr grammar on
      // every platform: no scope IDs and no leading zeros.
      bytes = address.contains(':')
          ? Uri.parseIPv6Address(address)
          : Uri.parseIPv4Address(address);
    } on Object {
      // Some malformed input such as 1:2:3:4:5:6:7::8 throws IndexError.
      return false;
    }
    if (slash < 0) return true;
    final prefix = target.substring(slash + 1);
    // Rust u8 parsing: an optional + and decimal digits.
    final bits = RegExp(r'^\+?[0-9]+$').hasMatch(prefix)
        ? int.tryParse(prefix)
        : null;
    return bits != null && bits <= bytes.length * 8;
  }

  Widget _text(String key, String label, {String? hint, bool enabled = true}) =>
      Padding(
        padding: const EdgeInsets.symmetric(vertical: 8),
        child: TextField(
          key: ValueKey('routing-field:$key'),
          controller: _fields[key],
          enabled: enabled,
          minLines: _lists.contains(key) ? 2 : 1,
          maxLines: _lists.contains(key) ? 5 : 1,
          decoration: InputDecoration(
            labelText: label,
            helperText: hint,
            helperMaxLines: 3,
          ),
        ),
      );

  Widget _choice(
    String key,
    String label,
    List<String> choices, {
    Map<String, String> labels = const {},
  }) => ElarniSelectTile(
    contentPadding: EdgeInsets.zero,
    title: Text(label),
    trailing: ElarniSelect<String>(
      value: choices.contains(_profile[key]) ? _profile[key] as String : null,
      hint: Text(l10n.select),
      items: choices
          .map(
            (value) => DropdownMenuItem(
              value: value,
              child: Text(labels[value] ?? value),
            ),
          )
          .toList(),
      onChanged: (value) => setState(() => _profile[key] = value),
    ),
  );

  Future<void> _save() async {
    if (_saving) return;
    for (final entry in _fields.entries) {
      final text = entry.value.text.trim();
      if (_lists.contains(entry.key)) {
        _profile[entry.key] = text
            .split(RegExp(r'[\r\n]+'))
            .map((item) => item.trim())
            .where((item) => item.isNotEmpty)
            .toSet()
            .toList();
      } else {
        _profile[entry.key] = entry.key == 'Name' && text.isEmpty ? null : text;
      }
    }
    if (!_explicitDNS) _profile.remove('DomesticDNSDomains');
    for (final key in ['DirectIp', 'ProxyIp', 'BlockIp']) {
      if (!(_profile[key] as List).cast<String>().every(_validIpTarget)) {
        setState(() => _error = l10n.routingIpTargetsInvalid(key));
        return;
      }
    }
    for (final alias in [
      'RemoteDNSIp',
      'RemoteDns',
      'DomesticDNSIp',
      'DomesticDns',
      'FakeDns',
    ]) {
      _profile.remove(alias);
    }
    for (final role in ['Remote', 'Domestic']) {
      final ip = _fields['${role}DNSIP']!.text.trim();
      final type = _profile['${role}DNSType'];
      // DoH needs no bootstrap IP; any IP is a plain address, as the compiler reads it.
      final validIp = ip.isEmpty
          ? type == 'DoH'
          : RegExp(r'^[0-9A-Fa-f:.]+$').hasMatch(ip) && _validIpTarget(ip);
      if (!validIp || !const ['DoH', 'DoU'].contains(type)) {
        setState(() => _error = l10n.routingDnsNeedsResolver(_dnsName(role)));
        return;
      }
      if (type == 'DoH') {
        final url = Uri.tryParse(_fields['${role}DNSDomain']!.text.trim());
        if (url == null ||
            url.scheme != 'https' ||
            url.host.isEmpty ||
            url.userInfo.isNotEmpty ||
            url.hasFragment) {
          setState(() => _error = l10n.routingDnsNeedsHttpsUrl(_dnsName(role)));
          return;
        }
      }
    }
    // As Android and iOS check them: a hostname once, ignoring case, and an
    // address the decoder reads as an IP.
    final hosts = <String, String>{};
    final hostnames = <String>{};
    for (final row in _hosts) {
      final host = row.host.text.trim(), address = row.address.text.trim();
      if (host.isEmpty ||
          !hostnames.add(host.toLowerCase()) ||
          !RegExp(r'^[0-9A-Fa-f:.]+$').hasMatch(address) ||
          !_validIpTarget(address)) {
        setState(() => _error = l10n.dnsHostsInvalid);
        return;
      }
      hosts[host] = address;
    }
    for (final MapEntry(key: key, value: alias) in _geofiles.entries) {
      final text = _geofileFields[key]!.text.trim();
      if (text == _geofileTexts[key]!.trim()) continue;
      _profile[key] = text;
      _profile.remove(alias);
    }
    // A profile without lists or hosts gets no empty ones.
    void edited(String key, Object value, {required bool empty}) {
      if ((!empty || _profile.containsKey(key)) &&
          jsonEncode(value) != jsonEncode(_profile[key]))
        _profile[key] = value;
    }

    final sources = [
      for (final row in _sources) {...row.source, 'url': row.url.text.trim()},
    ];
    edited('ManagedSources', sources, empty: sources.isEmpty);
    edited('DnsHosts', hosts, empty: hosts.isEmpty);
    final content = jsonEncode(_profile);
    final route = ModalRoute.of(context);
    setState(() => _saving = true);
    try {
      if (widget.onSave != null && !await widget.onSave!(content)) {
        if (mounted) setState(() => _error = l10n.routingChangeFailed);
        return;
      }
      if (mounted && route?.isCurrent == true) Navigator.pop(context, content);
    } finally {
      if (mounted) setState(() => _saving = false);
    }
  }

  // Keeps the user's lines first and adds each preset entry once.
  void _addSites(String key, List<String> sites) {
    final field = _fields[key]!;
    final current = field.text
        .split(RegExp(r'[\r\n]+'))
        .map((item) => item.trim())
        .where((item) => item.isNotEmpty)
        .toList();
    field.text = [
      ...current,
      ...sites.where((site) => !current.contains(site)),
    ].join('\n');
  }

  Widget _rules() => Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      ElarniSelectTile(
        contentPadding: EdgeInsets.zero,
        title: Text(l10n.unmatchedTraffic),
        trailing: ElarniSelect<bool>(
          value: _enabled('GlobalProxy'),
          items: [
            DropdownMenuItem(value: true, child: Text(l10n.useProxy)),
            DropdownMenuItem(value: false, child: Text(l10n.connectDirectly)),
          ],
          onChanged: (value) =>
              setState(() => _profile['GlobalProxy'] = value.toString()),
        ),
      ),
      for (final action in ['Direct', 'Proxy', 'Block'])
        ExpansionTile(
          key: ValueKey('routing-rules:$action'),
          title: Text(switch (action) {
            'Direct' => l10n.directRules,
            'Proxy' => l10n.proxyRules,
            _ => l10n.blockRules,
          }),
          tilePadding: EdgeInsets.zero,
          initiallyExpanded: _expanded.contains(action),
          onExpansionChanged: (open) =>
              open ? _expanded.add(action) : _expanded.remove(action),
          children: [
            if (action == 'Direct')
              Align(
                alignment: Alignment.centerRight,
                child: MenuAnchor(
                  menuChildren: [
                    MenuItemButton(
                      onPressed: () =>
                          _addSites('DirectSites', russianSitesPreset),
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        mainAxisSize: MainAxisSize.min,
                        children: [
                          Text(l10n.russianSitesDirect),
                          Text(
                            l10n.russianSitesPresetHelp,
                            style: Theme.of(context).textTheme.bodySmall,
                          ),
                        ],
                      ),
                    ),
                  ],
                  builder: (context, controller, child) => TextButton.icon(
                    key: Key('routing-preset:$action'),
                    onPressed: () => controller.isOpen
                        ? controller.close()
                        : controller.open(),
                    icon: const Icon(Icons.add, size: 18),
                    label: Text(l10n.addPreset),
                  ),
                ),
              ),
            _text('${action}Sites', l10n.domains, hint: l10n.domainRulesHint),
            _text(
              '${action}Ip',
              l10n.ipAddressesAndNetworks,
              hint: l10n.ipRulesHint,
            ),
          ],
        ),
    ],
  );

  Widget _dns() => Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      ElarniCheckboxTile(
        contentPadding: EdgeInsets.zero,
        title: ElarniHelpLabel(
          l10n.keepDomainIdentity,
          help: l10n.keepDomainIdentityHelp,
        ),
        value: _enabled('FakeDNS'),
        onChanged: (value) => setState(() {
          _profile.remove('FakeDns');
          _profile['FakeDNS'] = value.toString();
        }),
      ),
      for (final role in ['Remote', 'Domestic'])
        ExpansionTile(
          key: ValueKey('routing-dns:$role'),
          title: Text(role == 'Remote' ? l10n.remoteDns : l10n.directDns),
          tilePadding: EdgeInsets.zero,
          initiallyExpanded: _expanded.contains(role),
          onExpansionChanged: (open) =>
              open ? _expanded.add(role) : _expanded.remove(role),
          children: [
            _choice(
              '${role}DNSType',
              l10n.resolverType,
              ['DoH', 'DoU'],
              labels: const {'DoH': 'DNS over HTTPS', 'DoU': 'UDP'},
            ),
            if (_profile['${role}DNSType'] == 'DoH')
              _text('${role}DNSDomain', l10n.httpsUrl),
            _text(
              '${role}DNSIP',
              _profile['${role}DNSType'] == 'DoH'
                  ? l10n.bootstrapIp
                  : l10n.resolverIp,
            ),
          ],
        ),
      ExpansionTile(
        key: const ValueKey('routing-dns:domains'),
        title: Text(l10n.chooseDnsByDomain),
        tilePadding: EdgeInsets.zero,
        initiallyExpanded: _expanded.contains('domains'),
        onExpansionChanged: (open) =>
            open ? _expanded.add('domains') : _expanded.remove('domains'),
        children: [
          ElarniCheckboxTile(
            contentPadding: EdgeInsets.zero,
            title: ElarniHelpLabel(
              l10n.useExplicitDomainRules,
              help: l10n.explicitDomainRulesHelp,
            ),
            value: _explicitDNS,
            onChanged: (value) => setState(() => _explicitDNS = value),
          ),
          _text(
            'DomesticDNSDomains',
            l10n.domainsUsingDirectDns,
            hint: l10n.directDnsDomainsHint,
            enabled: _explicitDNS,
          ),
        ],
      ),
      ExpansionTile(
        key: const ValueKey('routing-dns:hosts'),
        title: Text(l10n.staticDnsHosts),
        tilePadding: EdgeInsets.zero,
        initiallyExpanded: _expanded.contains('hosts'),
        onExpansionChanged: (open) =>
            open ? _expanded.add('hosts') : _expanded.remove('hosts'),
        children: [
          for (final (index, row) in _hosts.indexed)
            Padding(
              // Records have no identity; the row's field has.
              key: ObjectKey(row.host),
              padding: const EdgeInsets.symmetric(vertical: 8),
              child: Row(
                children: [
                  Expanded(
                    child: TextField(
                      key: ValueKey('routing-host:$index'),
                      controller: row.host,
                      decoration: InputDecoration(labelText: l10n.hostname),
                    ),
                  ),
                  const SizedBox(width: 12),
                  Expanded(
                    child: TextField(
                      key: ValueKey('routing-host-address:$index'),
                      controller: row.address,
                      decoration: InputDecoration(
                        labelText: l10n.ipv4OrIpv6Address,
                      ),
                    ),
                  ),
                  IconButton(
                    key: ValueKey('routing-host-remove:$index'),
                    tooltip: l10n.removeHost,
                    onPressed: () => setState(() {
                      _hosts.remove(row);
                      _removed.addAll([row.host, row.address]);
                    }),
                    icon: const Icon(CupertinoIcons.minus_circle),
                  ),
                ],
              ),
            ),
          Align(
            alignment: AlignmentDirectional.centerStart,
            child: TextButton.icon(
              key: const Key('routing-host-add'),
              onPressed: () => setState(
                () => _hosts.add((
                  host: TextEditingController(),
                  address: TextEditingController(),
                )),
              ),
              icon: const Icon(Icons.add, size: 18),
              label: Text(l10n.addDnsHost),
            ),
          ),
        ],
      ),
    ],
  );

  /// A new managed domain list as Android adds one.
  Map<String, dynamic> _newSource() => {
    'id': List.generate(
      32,
      (_) => _random.nextInt(256).toRadixString(16).padLeft(2, '0'),
    ).join(),
    'url': '',
    'destination': 'block',
    'matchMode': 'subdomains',
  };

  Widget _sourceChoice(
    int index,
    Map<String, dynamic> source,
    String key,
    String label,
    Map<String, String> choices,
  ) => ElarniSelectTile(
    contentPadding: EdgeInsets.zero,
    title: Text(label),
    trailing: ElarniSelect<String>(
      key: ValueKey('routing-source-$key:$index'),
      value: choices.containsKey(source[key]) ? source[key] as String : null,
      hint: Text(l10n.select),
      items: [
        for (final MapEntry(key: choice, value: text) in choices.entries)
          DropdownMenuItem(value: choice, child: Text(text)),
      ],
      onChanged: (value) => setState(() => source[key] = value),
    ),
  );

  Widget _managedSources() => Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      const Divider(height: 32),
      Text(
        l10n.managedDomainLists,
        style: Theme.of(context).textTheme.titleSmall,
      ),
      for (final (index, row) in _sources.indexed)
        Column(
          // Records have no identity; the row's field has.
          key: ObjectKey(row.url),
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Padding(
              padding: const EdgeInsets.symmetric(vertical: 8),
              child: TextField(
                key: ValueKey('routing-source-url:$index'),
                controller: row.url,
                decoration: InputDecoration(labelText: l10n.listUrl),
              ),
            ),
            _sourceChoice(
              index,
              row.source,
              'destination',
              l10n.useDomainsFor,
              {
                'block': l10n.ruleGroupBlock,
                'direct': l10n.ruleGroupDirect,
                'proxy': l10n.ruleGroupProxy,
                'dns': l10n.directDns,
              },
            ),
            _sourceChoice(index, row.source, 'matchMode', l10n.managedMatch, {
              'exact': l10n.exactDomains,
              'subdomains': l10n.includeSubdomains,
            }),
            Align(
              alignment: AlignmentDirectional.centerEnd,
              child: TextButton(
                key: ValueKey('routing-source-remove:$index'),
                onPressed: () => setState(() {
                  _sources.remove(row);
                  _removed.add(row.url);
                }),
                child: Text(
                  l10n.removeList,
                  style: TextStyle(color: Theme.of(context).colorScheme.error),
                ),
              ),
            ),
            const Divider(),
          ],
        ),
      Align(
        alignment: AlignmentDirectional.centerStart,
        child: TextButton.icon(
          key: const Key('routing-source-add'),
          onPressed: _sources.length >= _maxSources
              ? null
              : () => setState(
                  () => _sources.add((
                    source: _newSource(),
                    url: TextEditingController(),
                  )),
                ),
          icon: const Icon(Icons.add, size: 18),
          label: Text(l10n.addDomainList),
        ),
      ),
      Text(
        l10n.managedDomainListsHelp,
        style: Theme.of(context).textTheme.bodySmall?.copyWith(
          color: Theme.of(context).colorScheme.onSurfaceVariant,
        ),
      ),
    ],
  );

  Widget _advanced() => Column(
    crossAxisAlignment: CrossAxisAlignment.stretch,
    children: [
      _text('Name', l10n.profileName),
      _choice(
        'RouteOrder',
        l10n.ruleGroupOrder,
        [
          'block-direct-proxy',
          'block-proxy-direct',
          'direct-block-proxy',
          'direct-proxy-block',
          'proxy-block-direct',
          'proxy-direct-block',
        ],
        labels: {
          for (final order in const [
            'block-direct-proxy',
            'block-proxy-direct',
            'direct-block-proxy',
            'direct-proxy-block',
            'proxy-block-direct',
            'proxy-direct-block',
          ])
            order: order
                .split('-')
                .map(
                  (group) => switch (group) {
                    'block' => l10n.ruleGroupBlock,
                    'direct' => l10n.ruleGroupDirect,
                    _ => l10n.ruleGroupProxy,
                  },
                )
                .join(' → '),
        },
      ),
      _choice(
        'DomainStrategy',
        l10n.resolveDomainsForRouting,
        ['AsIs', 'IPIfNonMatch', 'IPOnDemand'],
        labels: {
          'AsIs': l10n.keepDomainNames,
          'IPIfNonMatch': l10n.resolveIfNoRuleMatches,
          'IPOnDemand': l10n.resolveWhenNeeded,
        },
      ),
      ElarniCheckboxTile(
        contentPadding: EdgeInsets.zero,
        title: ElarniHelpLabel(
          l10n.reduceRoutingDataSize,
          help: l10n.reduceRoutingDataHelp,
        ),
        value: _enabled('UseChunkFiles'),
        onChanged: (value) => setState(() => _profile['UseChunkFiles'] = value),
      ),
      for (final (key, label) in [
        ('Geoipurl', l10n.geoipUrl),
        ('Geositeurl', l10n.geositeUrl),
      ])
        Padding(
          padding: const EdgeInsets.symmetric(vertical: 8),
          child: TextField(
            key: ValueKey('routing-field:$key'),
            controller: _geofileFields[key],
            decoration: InputDecoration(labelText: label),
          ),
        ),
      _managedSources(),
    ],
  );

  @override
  Widget build(BuildContext context) => DefaultTabController(
    length: 3,
    initialIndex: widget.initialSection,
    child: AlertDialog(
      title: Text(_profile['Name']?.toString() ?? l10n.routingAndDns),
      content: SizedBox(
        width: 620,
        child: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            TabBar(
              isScrollable: true,
              tabAlignment: TabAlignment.start,
              onTap: (index) => setState(() => _section = index),
              tabs: [
                Tab(text: l10n.rulesTab),
                Tab(text: 'DNS'),
                Tab(text: l10n.settingsAdvanced),
              ],
            ),
            if (_error != null)
              Padding(
                padding: const EdgeInsets.only(top: 12),
                child: Semantics(
                  liveRegion: true,
                  child: Text(
                    _error!,
                    style: TextStyle(
                      color: Theme.of(context).colorScheme.error,
                    ),
                  ),
                ),
              ),
            const SizedBox(height: 16),
            Flexible(
              child: SingleChildScrollView(
                key: ValueKey('routing-section:$_section'),
                child: switch (_section) {
                  0 => _rules(),
                  1 => _dns(),
                  _ => _advanced(),
                },
              ),
            ),
          ],
        ),
      ),
      actions: [
        TextButton(
          // Cancel cannot hide a save that is still being applied.
          onPressed: _saving ? null : () => Navigator.pop(context),
          child: Text(l10n.cancel),
        ),
        FilledButton(
          onPressed: _saving ? null : _save,
          child: Text(l10n.validateAndSave),
        ),
      ],
    ),
  );
}
