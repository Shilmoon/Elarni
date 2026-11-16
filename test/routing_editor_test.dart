import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:elarni/routing_editor.dart';

Map<String, Object?> profile() => {
  'Name': 'Fixture',
  'RemoteDNSType': 'DoU',
  'RemoteDNSIP': '1.1.1.1',
  'DomesticDNSType': 'DoU',
  'DomesticDNSIP': '8.8.8.8',
};

Future<void> openEditor(
  WidgetTester tester,
  Map<String, Object?> value,
  void Function(String?) saved,
) async {
  await tester.binding.setSurfaceSize(const Size(1100, 800));
  addTearDown(() => tester.binding.setSurfaceSize(null));
  await tester.pumpWidget(
    MaterialApp(
      home: Scaffold(
        body: Builder(
          builder: (context) => TextButton(
            onPressed: () async => saved(
              await showDialog<String>(
                context: context,
                builder: (_) => RoutingEditor(json: jsonEncode(value)),
              ),
            ),
            child: const Text('Open editor'),
          ),
        ),
      ),
    ),
  );
  await tester.tap(find.text('Open editor'));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets(
    'saving normalizes aliases and duplicates while preserving routing metadata',
    (tester) async {
      final value = profile()..remove('RemoteDNSIP');
      value.addAll({
        'RemoteDNSIp': '9.9.9.9',
        'FakeDns': 'true',
        'UseChunkFiles': true,
        'GeoipUrl': 'https://example.com/geoip.dat',
        'DnsHosts': {'example.com': '192.0.2.1'},
        'DirectIp': ['192.0.2.0/24', '192.0.2.0/24', '2001:db8::/32'],
      });
      String? saved;
      await openEditor(tester, value, (result) => saved = result);
      await tester.tap(find.text('Validate and save'));
      await tester.pumpAndSettle();
      final result = jsonDecode(saved!) as Map;
      expect(result['RemoteDNSIP'], '9.9.9.9');
      expect(result['FakeDNS'], 'true');
      expect(result.containsKey('RemoteDNSIp'), isFalse);
      expect(result.containsKey('FakeDns'), isFalse);
      expect(result.containsKey('DomesticDNSDomains'), isFalse);
      expect(result['DirectIp'], ['192.0.2.0/24', '2001:db8::/32']);
      expect(result['UseChunkFiles'], isTrue);
      expect(result['GeoipUrl'], 'https://example.com/geoip.dat');
      expect(result['DnsHosts'], {'example.com': '192.0.2.1'});
    },
  );

  testWidgets(
    'the Russian sites preset adds its domains to Direct after the user\'s own, once',
    (tester) async {
      String? saved;
      await openEditor(
        tester,
        profile()..['DirectSites'] = ['example.com', 'domain:ru'],
        (result) => saved = result,
      );
      for (var i = 0; i < 2; i++) {
        await tester.tap(find.byKey(const Key('routing-preset:Direct')));
        await tester.pumpAndSettle();
        expect(
          find.text(".ru, .рф, .su and these services' other domains"),
          findsOneWidget,
        );
        await tester.tap(find.text('Russian sites direct'));
        await tester.pumpAndSettle();
      }
      final expected = ['example.com', ...russianSitesPreset];
      expect(russianSitesPreset.take(3), [
        'domain:ru',
        'domain:xn--p1ai',
        'domain:su',
      ]);
      final field = tester.widget<TextField>(
        find.byKey(const ValueKey('routing-field:DirectSites')),
      );
      expect(field.controller!.text.split('\n'), expected);
      await tester.tap(find.text('Validate and save'));
      await tester.pumpAndSettle();
      expect((jsonDecode(saved!) as Map)['DirectSites'], expected);
    },
  );

  // The compiler (native/elarni-domain) rejects every target below.
  for (final target in [
    '192.0.2.1/33',
    '2001:db8::/129',
    '192.0.2.1/-1',
    '192.0.2.1/24/1',
    '192.0.2.1/-0',
    '192.0.2.1/0x20',
    '192.0.2.01',
    'fe80::1%1',
    '!',
    '!not-an-ip',
    '!192.0.2.1/33',
    'geosite:cn',
    'GEOIP:cn',
    'geoip:x${'\u00e9' * 253}',
  ]) {
    final name = target.length > 32
        ? 'of ${utf8.encode(target).length} bytes'
        : target;
    testWidgets('invalid IP target $name keeps the editor open', (
      tester,
    ) async {
      String? saved;
      await openEditor(
        tester,
        profile()..['DirectIp'] = [target],
        (value) => saved = value,
      );
      await tester.tap(find.text('Validate and save'));
      await tester.pumpAndSettle();
      expect(saved, isNull);
      expect(
        find.textContaining('DirectIp needs IP addresses'),
        findsOneWidget,
      );
    });
  }

  testWidgets('boundary IP targets and explicit DNS domains survive a save', (
    tester,
  ) async {
    String? saved;
    final value = profile()
      ..addAll({
        // The compiler accepts every target below.
        'DirectIp': [
          '0.0.0.0/0',
          '192.0.2.1/32',
          '::/0',
          '2001:db8::1/128',
          '::ffff:192.0.2.1',
          '192.0.2.0/+24',
          '!192.0.2.2',
          '!!2001:db8::/32',
          '!geoip:cn',
          'geoip:!cn',
          'ext:lists/geoip.dat:cn',
          'ext-ip:geoip.dat:private',
          '!ext-ip:geoip.dat:private',
          'geoip:${'\u00e9' * 253}',
        ],
        'DomesticDNSDomains': ['full:example.cn', 'full:example.cn'],
      });
    await openEditor(tester, value, (result) => saved = result);
    await tester.tap(find.text('Validate and save'));
    await tester.pumpAndSettle();
    final result = jsonDecode(saved!) as Map;
    expect(result['DirectIp'], value['DirectIp']);
    expect(result['DomesticDNSDomains'], ['full:example.cn']);
  });

  for (final url in [
    'http://resolver.example/dns-query',
    'https://user:pass@resolver.example/dns-query',
    'https://resolver.example/dns-query#fragment',
  ]) {
    testWidgets('unsafe DoH URL $url is rejected', (tester) async {
      String? saved;
      await openEditor(
        tester,
        profile()..addAll({'RemoteDNSType': 'DoH', 'RemoteDNSDomain': url}),
        (result) => saved = result,
      );
      await tester.tap(find.text('Validate and save'));
      await tester.pumpAndSettle();
      expect(saved, isNull);
      expect(
        find.textContaining('Remote DNS needs an HTTPS URL'),
        findsOneWidget,
      );
    });
  }

  // The compiler reads a legacy RemoteDns or DomesticDns when the current key is
  // empty, so a cleared bootstrap IP would come back.
  testWidgets('a cleared legacy bootstrap IP stays cleared', (tester) async {
    String? saved;
    await openEditor(
      tester,
      profile()
        ..remove('RemoteDNSIP')
        ..remove('DomesticDNSIP')
        ..addAll({
          'RemoteDNSType': 'DoH',
          'RemoteDNSDomain': 'https://resolver.example/dns-query',
          'RemoteDns': '9.9.9.9',
          'DomesticDns': '8.8.8.8',
        }),
      (result) => saved = result,
    );
    await tester.tap(find.text('DNS'));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const ValueKey('routing-field:RemoteDNSIP')),
      '',
    );
    await tester.tap(find.text('Validate and save'));
    await tester.pumpAndSettle();
    final result = jsonDecode(saved!) as Map;
    expect(result['RemoteDNSIP'], '');
    expect(result['DomesticDNSIP'], '8.8.8.8');
    expect(result.containsKey('RemoteDns'), isFalse);
    expect(result.containsKey('DomesticDns'), isFalse);
  });

  // The native validator accepts DoH without a bootstrap IP (the proxy resolves
  // the resolver's name) and refuses scoped addresses on every platform.
  testWidgets('DoH saves without a bootstrap IP', (tester) async {
    String? saved;
    await openEditor(
      tester,
      profile()..addAll({
        'RemoteDNSType': 'DoH',
        'RemoteDNSDomain': 'https://resolver.example/dns-query',
        'RemoteDNSIP': '',
      }),
      (result) => saved = result,
    );
    await tester.tap(find.text('Validate and save'));
    await tester.pumpAndSettle();
    expect((jsonDecode(saved!) as Map)['RemoteDNSIP'], '');
  });

  for (final (type, ip) in [
    ('DoU', ''),
    ('DoU', 'fe80::1%1'),
    ('DoH', 'fe80::1%1'),
  ]) {
    testWidgets('$type resolver IP "$ip" keeps the editor open', (
      tester,
    ) async {
      String? saved;
      await openEditor(
        tester,
        profile()..addAll({
          'RemoteDNSType': type,
          'RemoteDNSDomain': 'https://resolver.example/dns-query',
          'RemoteDNSIP': ip,
        }),
        (result) => saved = result,
      );
      await tester.tap(find.text('Validate and save'));
      await tester.pumpAndSettle();
      expect(saved, isNull);
      expect(find.textContaining('Remote DNS needs'), findsOneWidget);
    });
  }

  // An expanded section stored its open state under its PageStorageKey, which the text fields inside
  // also resolved to: their scroll position read the bool as an offset and each field became a grey
  // error box (Direct DNS on Windows, 2026-11-08). Collapsed sections must open with working fields.
  testWidgets('sections opened or reopened keep their text fields', (
    tester,
  ) async {
    await openEditor(
      tester,
      profile()..addAll({
        'RemoteDNSType': 'DoH',
        'RemoteDNSDomain': 'https://8.8.8.8/dns-query',
        'DomesticDNSType': 'DoH',
        'DomesticDNSDomain': 'https://77.88.8.8/dns-query',
        'ProxySites': ['geosite:youtube'],
        'BlockSites': ['geosite:category-ads'],
      }),
      (_) {},
    );
    Future<void> toggle(String title) async {
      await tester.ensureVisible(find.text(title));
      await tester.tap(find.text(title));
      await tester.pumpAndSettle();
    }

    for (final title in ['Proxy rules', 'Block rules']) {
      await toggle(title);
    }
    expect(find.text('geosite:youtube'), findsOneWidget);
    expect(find.text('geosite:category-ads'), findsOneWidget);
    await tester.tap(find.text('DNS'));
    await tester.pumpAndSettle();
    await toggle('Direct DNS');
    await toggle('Choose DNS by domain');
    await toggle('Remote DNS'); // collapse and reopen one that started open
    await toggle('Remote DNS');
    expect(find.text('https://77.88.8.8/dns-query'), findsOneWidget);
    expect(find.text('https://8.8.8.8/dns-query'), findsOneWidget);
    expect(find.byType(ErrorWidget), findsNothing);
  });

  Future<void> section(WidgetTester tester, String tab) async {
    await tester.tap(find.widgetWithText(Tab, tab));
    await tester.pumpAndSettle();
  }

  Future<void> tapShown(WidgetTester tester, Finder finder) async {
    await tester.ensureVisible(finder);
    await tester.pumpAndSettle();
    await tester.tap(finder);
    await tester.pumpAndSettle();
  }

  Future<void> choose(WidgetTester tester, String select, String label) async {
    await tapShown(tester, find.byKey(ValueKey(select)));
    await tester.tap(find.widgetWithText(MenuItemButton, label));
    await tester.pumpAndSettle();
  }

  Future<void> type(WidgetTester tester, String key, String text) async {
    final field = find.byKey(ValueKey(key));
    await tester.ensureVisible(field);
    await tester.pumpAndSettle();
    await tester.enterText(field, text);
    await tester.pumpAndSettle();
  }

  testWidgets(
    'geofile URLs, managed domain lists and DNS hosts round-trip through a save',
    (tester) async {
      String? saved;
      await openEditor(
        tester,
        profile()..addAll({
          'GeoipUrl': 'https://old.example/geoip.dat',
          'ManagedSources': [
            {
              'id': 'keep',
              'url': 'https://lists.example/a.txt',
              'destination': 'direct',
              'matchMode': 'subdomains',
            },
            {
              'id': 'drop',
              'url': 'https://lists.example/old.txt',
              'destination': 'block',
              'matchMode': 'exact',
            },
          ],
          'DnsHosts': {'a.example': '192.0.2.1'},
        }),
        (result) => saved = result,
      );
      await section(tester, 'Advanced');
      // The decoder's alias shows in the field until the URL is changed.
      expect(find.text('https://old.example/geoip.dat'), findsOneWidget);
      await type(
        tester,
        'routing-field:Geoipurl',
        'https://new.example/geoip.dat',
      );
      await type(
        tester,
        'routing-field:Geositeurl',
        ' https://new.example/geosite.dat ',
      );
      await choose(tester, 'routing-source-destination:0', 'Proxy');
      await tapShown(
        tester,
        find.byKey(const ValueKey('routing-source-remove:1')),
      );
      expect(find.text('https://lists.example/old.txt'), findsNothing);
      await tapShown(tester, find.byKey(const Key('routing-source-add')));
      await type(tester, 'routing-source-url:1', 'https://lists.example/b.txt');
      await choose(tester, 'routing-source-matchMode:1', 'Exact domains');
      await section(tester, 'DNS');
      // A profile with hosts opens with them shown.
      await type(tester, 'routing-host-address:0', '192.0.2.10');
      await tapShown(tester, find.byKey(const Key('routing-host-add')));
      await type(tester, 'routing-host:1', ' B.example ');
      await type(tester, 'routing-host-address:1', '2001:db8::2');
      await tester.tap(find.text('Validate and save'));
      await tester.pumpAndSettle();
      final result = jsonDecode(saved!) as Map;
      expect(result['Geoipurl'], 'https://new.example/geoip.dat');
      expect(result.containsKey('GeoipUrl'), isFalse);
      expect(result['Geositeurl'], 'https://new.example/geosite.dat');
      final sources = (result['ManagedSources'] as List).cast<Map>();
      expect(sources, hasLength(2));
      expect(sources.first, {
        'id': 'keep',
        'url': 'https://lists.example/a.txt',
        'destination': 'proxy',
        'matchMode': 'subdomains',
      });
      // A new list as Android adds one, with the URL and match chosen here.
      expect(sources.last['id'], matches(RegExp(r'^[0-9a-f]{64}$')));
      expect({...sources.last}..remove('id'), {
        'url': 'https://lists.example/b.txt',
        'destination': 'block',
        'matchMode': 'exact',
      });
      expect(result['DnsHosts'], {
        'a.example': '192.0.2.10',
        'B.example': '2001:db8::2',
      });
    },
  );

  testWidgets('removing every list and host saves them empty', (tester) async {
    String? saved;
    await openEditor(
      tester,
      profile()..addAll({
        'ManagedSources': [
          {
            'id': 'drop',
            'url': 'https://lists.example/a.txt',
            'destination': 'dns',
            'matchMode': 'exact',
          },
        ],
        'DnsHosts': {'a.example': '192.0.2.1'},
      }),
      (result) => saved = result,
    );
    await section(tester, 'Advanced');
    await tapShown(
      tester,
      find.byKey(const ValueKey('routing-source-remove:0')),
    );
    await section(tester, 'DNS');
    await tapShown(tester, find.byKey(const ValueKey('routing-host-remove:0')));
    expect(find.byKey(const ValueKey('routing-host:0')), findsNothing);
    await tester.tap(find.text('Validate and save'));
    await tester.pumpAndSettle();
    final result = jsonDecode(saved!) as Map;
    expect(result['ManagedSources'], isEmpty);
    expect(result['DnsHosts'], isEmpty);
    expect(tester.takeException(), isNull);
  });

  for (final (name, host, address) in [
    ('a repeated hostname in other case', 'A.EXAMPLE', '192.0.2.2'),
    ('a blank hostname', ' ', '192.0.2.2'),
    ('a blank address', 'b.example', ''),
    ('an address that is not an IP', 'b.example', 'gateway.example'),
    ('a network instead of an address', 'b.example', '192.0.2.0/24'),
  ]) {
    testWidgets('$name keeps the DNS hosts editor open', (tester) async {
      String? saved;
      await openEditor(
        tester,
        profile()..['DnsHosts'] = {'a.example': '192.0.2.1'},
        (result) => saved = result,
      );
      await section(tester, 'DNS');
      await tapShown(tester, find.byKey(const Key('routing-host-add')));
      await type(tester, 'routing-host:1', host);
      await type(tester, 'routing-host-address:1', address);
      await tester.tap(find.text('Validate and save'));
      await tester.pumpAndSettle();
      expect(saved, isNull);
      expect(
        find.text(
          'Enter a unique hostname and an IP address for each DNS host.',
        ),
        findsOneWidget,
      );
      expect(find.byType(RoutingEditor), findsOneWidget);
    });
  }

  testWidgets(
    'an untouched profile saves byte for byte, and gains no empty fields',
    (tester) async {
      // A profile already in the form a save writes, with the new fields set
      // and keys this editor does not know.
      final value = <String, Object?>{
        'Name': 'Fixture',
        'RemoteDNSType': 'DoU',
        'RemoteDNSDomain': '',
        'RemoteDNSIP': '1.1.1.1',
        'DomesticDNSType': 'DoU',
        'DomesticDNSDomain': '',
        'DomesticDNSIP': '8.8.8.8',
        'DirectSites': <String>[],
        'DirectIp': <String>[],
        'ProxySites': <String>[],
        'ProxyIp': <String>[],
        'BlockSites': <String>[],
        'BlockIp': <String>[],
        'FakeDNS': 'false',
        'UseChunkFiles': 'true',
        'Geoipurl': 'https://example.com/geoip.dat',
        'GeositeUrl': 'https://example.com/geosite.dat',
        'ManagedSources': [
          {
            'matchMode': 'exact',
            'id': 'ads',
            'url': 'https://lists.example/ads.txt',
            'destination': 'block',
          },
        ],
        'DnsHosts': {'router.lan': '192.168.1.1', 'Example.com': '2001:db8::1'},
        'LastUpdated': {'geoip': 1700000000},
      };
      String? saved;
      await openEditor(tester, value, (result) => saved = result);
      // Every section built once, as when a user looks through them.
      await section(tester, 'DNS');
      await section(tester, 'Advanced');
      await tester.tap(find.text('Validate and save'));
      await tester.pumpAndSettle();
      expect(saved, jsonEncode(value));

      saved = null;
      await openEditor(tester, profile(), (result) => saved = result);
      await section(tester, 'DNS');
      await section(tester, 'Advanced');
      await tester.tap(find.text('Validate and save'));
      await tester.pumpAndSettle();
      final result = jsonDecode(saved!) as Map;
      for (final key in [
        'Geoipurl',
        'Geositeurl',
        'GeoipUrl',
        'GeositeUrl',
        'ManagedSources',
        'DnsHosts',
      ]) {
        expect(result.containsKey(key), isFalse, reason: key);
      }
    },
  );
}
