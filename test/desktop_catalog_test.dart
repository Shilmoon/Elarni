import 'dart:convert';

import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:elarni/desktop_automation.dart';
import 'package:elarni/desktop_catalog.dart';
import 'package:elarni/tunnel_channel.dart';

void main() {
  test('a new catalog requires a generated login for its local listeners', () {
    final first = DesktopCatalog.empty(), second = DesktopCatalog.empty();
    final catalog = DesktopCatalog(
      first,
      (method, payload) async => throw StateError(method),
      (operation, payload) => throw StateError(operation),
    )..validate();
    expect(catalog.settings.inboundAuthEnabled, isTrue);
    expect(catalog.settings.inboundCredentialsConfigured, isTrue);
    final login = catalogMap(first['inboundCredentials']);
    expect(login['username'], matches(RegExp(r'^[0-9a-f]{32}$')));
    expect(login['password'], matches(RegExp(r'^[0-9a-f]{32}$')));
    expect(second['inboundCredentials'], isNot(login));
    // Installs from before keep their listeners as they were.
    final upgraded = DesktopCatalog.empty()
      ..remove('inboundCredentials')
      ..['settings'] = const TunnelSettings(xrayTunEnabled: true).toMap();
    final kept = DesktopCatalog(
      upgraded,
      (method, payload) async => throw StateError(method),
      (operation, payload) => throw StateError(operation),
    )..validate();
    expect(kept.settings.inboundAuthEnabled, isFalse);
    expect(kept.data['inboundCredentials'], isNull);
  });

  // A server test compiles one server. Its catalog used to be a JSON copy of the
  // whole catalog, made for every server of a bulk test.
  test('a server test gets a catalog of that server alone, without a copy', () {
    Map<String, Object?> node(String id) => {
      'id': id,
      'name': id,
      'format': 'xrayProfile',
      'config': {'tag': id},
    };
    final data = {
      ...DesktopCatalog.empty(),
      'selectedNodeId': 'a-1',
      'subscriptions': [
        {
          'id': 'a',
          'nodes': [node('a-1'), node('a-2')],
          'hiddenNodes': [node('a-3')],
        },
        {
          'id': 'b',
          'nodes': [node('b-1')],
        },
      ],
      'profiles': [
        {'id': 'route-a', 'enabled': true, 'assetRevision': 'ra'},
        {'id': 'route-b', 'enabled': true, 'assetRevision': 'rb'},
      ],
      'assignments': {'a': 'route-a', 'b': 'route-b'},
    };
    final before = jsonEncode(data);
    final catalog = DesktopCatalog(
      data,
      (method, payload) async => throw StateError(method),
      (operation, payload) => throw StateError(operation),
    );
    final settings = catalog.settings.copyWith(socksPort: 23456).toMap();
    final first = catalog.only('a-2', {
      'selectedNodeId': 'a-2',
      'settings': settings,
      'inboundCredentials': {'username': 'one', 'password': 'one'},
    });
    final second = catalog.only('b-1', {'selectedNodeId': 'b-1'});
    expect(first.selected, 'a-2');
    expect(first.settings.socksPort, 23456);
    expect(catalogMap(first.data['inboundCredentials'])['username'], 'one');
    expect(first.sources.map((source) => source['id']), ['a']);
    expect(
      [
        for (final source in first.sources)
          for (final item in catalogList(source['nodes'])) item['id'],
      ],
      ['a-2'],
    );
    expect(first.sources.single['hiddenNodes'], isEmpty);
    expect(first.routes.map((route) => route['id']), ['route-a']);
    expect(first.assignments['a'], 'route-a');
    // The server and its routing profile are the catalog's own, not copies.
    expect(
      identical(first.node('a-2')['config'], catalog.node('a-2')['config']),
      isTrue,
    );
    expect(second.selected, 'b-1');
    expect(second.routes.map((route) => route['id']), ['route-b']);
    expect(
      second.settings.socksPort,
      catalog.settings.socksPort,
      reason: 'one test changed the settings of another',
    );
    expect(jsonEncode(data), before, reason: 'a test changed the catalog');
    expect(
      () => catalog.only('missing', const {}),
      throwsA(
        isA<PlatformException>().having(
          (error) => error.code,
          'code',
          'invalid_profile',
        ),
      ),
    );
  });

  group('a backup', () {
    CatalogMap node(String id, String address, {bool hideURL = false}) => {
      'id': id,
      'name': id,
      'format': 'xrayProfile',
      'config': {
        'outbounds': [
          {
            'protocol': 'vless',
            'settings': {'address': address, 'port': 443},
          },
        ],
      },
      if (hideURL) 'hideURL': true,
    };
    CatalogMap source(
      String id,
      String name,
      List<CatalogMap> nodes, {
      String? url,
      Object? hideURL,
    }) => {
      'id': id,
      'name': name,
      'url': ?url,
      'metadata': {'hideURL': ?hideURL},
      'nodes': nodes,
    };
    CatalogMap route(String id, {String? provider}) => {
      'id': id,
      'name': id,
      'profile': {'Name': id},
      'original': {'Name': id},
      'routeModel': <String, Object?>{},
      'enabled': true,
      'edited': false,
      'providerSubscriptionId': ?provider,
    };
    DesktopCatalog catalogOf(CatalogMap data) => DesktopCatalog(
      {...DesktopCatalog.empty(), ...data},
      (method, payload) async => throw StateError(method),
      (operation, payload) => switch (operation) {
        'catalogIdentity' => {'identity': jsonEncode(payload)},
        'decodeRouting' => {
          'import': {'profile': jsonDecode(payload['content']! as String)},
        },
        'mergeManagedRouting' => {'routeModel': <String, Object?>{}},
        _ => throw StateError(operation),
      },
    );
    Matcher refused(String code) => throwsA(
      isA<PlatformException>().having((error) => error.code, 'code', code),
    );

    // One subscription whose provider forbids backups, or that was never
    // updated, used to refuse the backup of everything else.
    test('leaves out and names the subscriptions it may not contain', () {
      final catalog = catalogOf({
        'selectedNodeId': 'forbidden-1',
        'subscriptions': [
          source('local', 'My servers', [
            node('local-1', 'local.example'),
          ], hideURL: false),
          source(
            'provider',
            'Allowed provider',
            [node('provider-1', 'allowed.example')],
            url: 'https://allowed.example/sub',
            hideURL: false,
          ),
          source(
            'forbidden',
            'No backups',
            [node('forbidden-1', 'forbidden.example')],
            url: 'https://forbidden.example/sub',
            hideURL: true,
          ),
          // A hidden import keeps its own local group, flagged per server.
          source('hidden', 'Hidden import', [
            node('hidden-1', 'hidden.example', hideURL: true),
          ]),
          // Never updated: whether its provider allows backups is unknown.
          source('fresh', 'Never updated', [
            node('fresh-1', 'fresh.example'),
          ], url: 'https://fresh.example/sub'),
        ],
        'profiles': [
          route('route-local'),
          route('route-provider', provider: 'provider'),
          route('route-forbidden', provider: 'forbidden'),
          route('route-fresh', provider: 'fresh'),
        ],
        'assignments': {
          'local': 'route-forbidden',
          'provider': 'route-provider',
          'forbidden': 'route-forbidden',
          'fresh': 'route-local',
        },
      })..validate();
      final (:document, :excluded) = catalog.backup();
      expect(excluded, [
        {'name': 'No backups', 'reason': 'sharing_disabled'},
        {'name': 'Hidden import', 'reason': 'sharing_disabled'},
        {'name': 'Never updated', 'reason': 'sharing_refresh_required'},
      ]);
      final subscriptions = catalogList(document['subscriptions']);
      expect(subscriptions.map((source) => source['id']), [
        'local',
        'provider',
      ]);
      // Nothing of a left-out subscription is in the file: no server, no
      // link, no routing profile of its provider.
      final text = jsonEncode(document);
      for (final private in [
        'forbidden.example',
        'hidden.example',
        'fresh.example',
        'route-forbidden',
        'route-fresh',
      ])
        expect(text, isNot(contains(private)), reason: private);
      // The selection was a left-out server, so the backup selects none.
      expect(document.containsKey('selectedNodeId'), isFalse);
      expect(catalogList(document['routes']).map((route) => route['id']), [
        'route-local',
        'route-provider',
      ]);
      expect(document['assignments'], {'provider': 'route-provider'});
      // The catalog itself is unchanged.
      expect(catalog.sources.length, 5);
      expect(catalog.selected, 'forbidden-1');

      final restored = catalogOf({})..restoreBackup(document);
      restored.validate();
      expect(restored.sources.map((source) => source['name']), [
        'My servers',
        'Allowed provider',
      ]);
      expect(restored.routes.length, 2);
      final provider = restored.sources.last['id'];
      expect(
        restored.routes
            .singleWhere((route) => route['name'] == 'route-provider')['id'],
        restored.assignments[provider],
      );
      expect(restored.assignments.length, 1);
    });

    test('keeps a selection the backup contains', () {
      final (:document, :excluded) = catalogOf({
        'selectedNodeId': 'local-1',
        'subscriptions': [
          source('local', 'My servers', [node('local-1', 'local.example')]),
          source('forbidden', 'No backups', [
            node('forbidden-1', 'forbidden.example'),
          ], hideURL: true),
        ],
      }).backup();
      expect(document['selectedNodeId'], 'local-1');
      expect(excluded.single['name'], 'No backups');
    });

    test('with nothing left to save still fails with the first refusal', () {
      expect(
        () => catalogOf({
          'subscriptions': [
            source(
              'forbidden',
              'No backups',
              [node('forbidden-1', 'forbidden.example')],
              url: 'https://forbidden.example/sub',
              hideURL: true,
            ),
            source('fresh', 'Never updated', [
              node('fresh-1', 'fresh.example'),
            ], url: 'https://fresh.example/sub'),
          ],
          // A left-out provider's routing profile is not left to save.
          'profiles': [route('route-forbidden', provider: 'forbidden')],
          'assignments': {'forbidden': 'route-forbidden'},
        }).backup(),
        refused('sharing_disabled'),
      );
      expect(
        () => catalogOf({
          'subscriptions': [
            source('fresh', 'Never updated', [
              node('fresh-1', 'fresh.example'),
            ], url: 'https://fresh.example/sub'),
          ],
        }).backup(),
        refused('sharing_refresh_required'),
      );
      // A routing profile of the user's own is worth saving alone.
      final (:document, :excluded) = catalogOf({
        'subscriptions': [
          source('fresh', 'Never updated', [
            node('fresh-1', 'fresh.example'),
          ], url: 'https://fresh.example/sub'),
        ],
        'profiles': [route('route-local')],
        'assignments': {'fresh': 'route-local'},
      }).backup();
      expect(document['subscriptions'], isEmpty);
      expect(catalogList(document['routes']).single['id'], 'route-local');
      expect(document['assignments'], isEmpty);
      expect(excluded.single['reason'], 'sharing_refresh_required');
      // An empty catalog has nothing left out and saves as before.
      expect(catalogOf({}).backup().excluded, isEmpty);
    });
  });

  test(
    'per-app server targets resolve to their server and survive a restore',
    () {
      final server = {
        'outbounds': [
          {
            'protocol': 'vless',
            'settings': {'address': 'nl.example.com', 'port': 443},
          },
        ],
      };
      final data = {
        ...DesktopCatalog.empty(),
        'subscriptions': [
          {
            'id': 'provider',
            'name': 'Provider',
            'nodes': [
              {
                'id': 'nl-4',
                'name': 'NL-4',
                'format': 'xrayProfile',
                'config': server,
              },
            ],
          },
        ],
      };
      final catalog = DesktopCatalog(
        data,
        (method, payload) async => throw StateError(method),
        (operation, payload) => switch (operation) {
          'catalogIdentity' => {'identity': jsonEncode(payload)},
          _ => throw StateError(operation),
        },
      );
      const settings = TunnelSettings(
        xrayTunEnabled: true,
        perAppMode: 'bypass',
        perAppProcesses: [
          'telegram.exe',
          'game.exe',
          'browser.exe',
          'steam.exe',
        ],
        perAppTargets: {
          'telegram.exe': 'node:nl-4',
          'game.exe': 'block',
          'browser.exe': 'proxy:socks5://127.0.0.1:1080',
        },
      );
      expect(catalog.perAppTargets(settings), [
        {'process': 'telegram.exe', 'target': 'server', 'server': server},
        {'process': 'game.exe', 'target': 'block'},
        {
          'process': 'browser.exe',
          'target': 'proxy',
          'proxy': 'socks5://127.0.0.1:1080',
        },
      ]);
      // Off keeps the targets but sends none, so a deleted server cannot block a connect.
      expect(
        catalog.perAppTargets(settings.copyWith(perAppMode: 'off')),
        isEmpty,
      );
      expect(
        () => catalog.perAppTargets(
          settings.copyWith(perAppTargets: {'telegram.exe': 'node:gone'}),
        ),
        throwsA(
          isA<PlatformException>().having(
            (error) => error.code,
            'code',
            'per_app_target_missing',
          ),
        ),
      );
      // A restore gives servers new IDs; the targets follow them.
      data['settings'] = settings.toMap();
      catalog.restoreBackup(catalog.backup().document);
      final restored = catalog.sources.single;
      final id = catalogList(restored['nodes']).single['id']! as String;
      expect(id, isNot('nl-4'));
      expect(catalog.settings.perAppTargets['telegram.exe'], 'node:$id');
      expect(catalog.settings.perAppTargets['game.exe'], 'block');
    },
  );

  // Bulk tests cap probes per SNI, the name TSPU counts handshakes by.
  test('each server names the SNI its probes handshake with', () {
    CatalogMap node(String id, CatalogMap outbound) => {
      'id': id,
      'name': id,
      'format': 'xrayProfile',
      'config': {
        'outbounds': [outbound],
      },
    };
    final catalog = DesktopCatalog(
      {
        ...DesktopCatalog.empty(),
        'subscriptions': [
          {
            'id': 'provider',
            'name': 'Provider',
            'nodes': [
              node('reality', {
                'protocol': 'vless',
                'settings': {'address': '192.0.2.1', 'port': 443},
                'streamSettings': {
                  'security': 'reality',
                  'realitySettings': {'serverName': 'Shared.Example'},
                },
              }),
              node('tls', {
                'protocol': 'trojan',
                'settings': {
                  'servers': [
                    {'address': '192.0.2.2', 'port': 443},
                  ],
                },
                'streamSettings': {
                  'security': 'tls',
                  'tlsSettings': {'serverName': 'shared.example'},
                },
              }),
              // TLS without a server name sends the address as its SNI.
              node('address', {
                'protocol': 'vmess',
                'settings': {
                  'vnext': [
                    {'address': 'Address.Example', 'port': 443},
                  ],
                },
                'streamSettings': {'security': 'tls'},
              }),
              // Without TLS there is no SNI to count.
              node('plain', {
                'protocol': 'vless',
                'settings': {'address': '192.0.2.3', 'port': 80},
              }),
              // ShadowQUIC has no stream settings; its name is in its own.
              node('shadowquic', {
                'protocol': 'shadowquic',
                'settings': {
                  'address': '192.0.2.4',
                  'port': 443,
                  'serverName': 'Shared.Example',
                },
              }),
            ],
          },
        ],
      },
      (method, payload) async => throw StateError(method),
      (operation, payload) => throw StateError(operation),
    );
    final nodes = catalogList(
      catalogList(catalog.snapshot()['subscriptions']).single['nodes'],
    );
    expect(nodes.map((node) => node['probeKey']), [
      'shared.example',
      'shared.example',
      'address.example',
      null,
      'shared.example',
    ]);
  });

  test('a provider rotating 1,024 server keys keeps their IDs quickly', () {
    final catalog = DesktopCatalog(
      DesktopCatalog.empty(),
      (method, payload) async => throw StateError(method),
      (operation, payload) => switch (operation) {
        'catalogIdentity' => {'identity': jsonEncode(payload)},
        'catalogStableIdentity' => {'identity': jsonEncode(payload)},
        'keepShortIds' => {'config': payload['config']},
        _ => throw StateError(operation),
      },
    );
    CatalogMap server(int i, String key) => {
      'format': 'xrayProfile',
      'name': 'Server $i',
      'config': {
        'outbounds': [
          {
            'protocol': 'vless',
            'settings': {'id': key},
            'streamSettings': {'network': 'tcp', 'security': 'reality'},
          },
        ],
      },
    };
    final previous = catalog.parseNodes({
      'profiles': [for (var i = 0; i < 1024; i++) server(i, 'old-$i')],
    }, []);
    final watch = Stopwatch()..start();
    final rotated = catalog.parseNodes({
      'profiles': [for (var i = 0; i < 1024; i++) server(i, 'new-$i')],
    }, previous);
    // Matching rebuilt every server's display key per server: seconds.
    expect(watch.elapsed, lessThan(const Duration(seconds: 1)));
    expect(
      rotated.map((node) => node['id']),
      previous.map((node) => node['id']),
    );
  });

  // 3x-ui picks a REALITY server's sid and sni per fetch and writes the
  // traffic left into the remark, so neither the exact identity nor the name
  // finds the server again (N10-06).
  group('a refresh matched by the stable identity', () {
    // As the domain library: the identity without the rotated REALITY values.
    Object? withoutRotated(Object? value) => switch (value) {
      final Map map => {
        for (final MapEntry(:key, value: item) in map.entries)
          key: withoutRotated(
            key == 'realitySettings' && item is Map
                ? ({...item}..removeWhere(
                    (field, _) =>
                        {'shortId', 'serverName', 'spiderX'}.contains(field),
                  ))
                : item,
          ),
      },
      final List list => [for (final item in list) withoutRotated(item)],
      _ => value,
    };
    late List<CatalogMap> keptShortIds;
    DesktopCatalog catalog() {
      keptShortIds = [];
      return DesktopCatalog(
        DesktopCatalog.empty(),
        (method, payload) async => throw StateError(method),
        (operation, payload) => switch (operation) {
          'catalogIdentity' => {'identity': jsonEncode(payload)},
          'catalogStableIdentity' => {
            'identity': jsonEncode(withoutRotated(payload)),
          },
          // The stored values stay, as for a server that connected lately.
          'keepShortIds' => {
            'config': (keptShortIds..add(payload)).last['previous'],
          },
          _ => throw StateError(operation),
        },
      );
    }

    CatalogMap server(
      String name, {
      String host = '192.0.2.11',
      String sid = 'a1b2',
      String sni = 'www.example.com',
      String format = 'xrayProfile',
    }) => {
      'format': format,
      'name': name,
      'config': {
        'outbounds': [
          {
            'protocol': 'vless',
            'settings': {'address': host, 'port': 443, 'id': 'user'},
            'streamSettings': {
              'network': 'tcp',
              'security': 'reality',
              'realitySettings': {
                'publicKey': 'key',
                'shortId': sid,
                'serverName': sni,
              },
            },
          },
        ],
      },
    };
    List<Object?> ids(List<CatalogMap> nodes) => [
      for (final node in nodes) node['id'],
    ];

    test('keeps each server with its user state when the remark changed with '
        'the sid, the sni or both', () {
      for (final (sid, sni) in [
        ('e5f6', 'www.example.com'),
        ('a1b2', 'cdn.example.org'),
        ('e5f6', 'cdn.example.org'),
      ]) {
        final subject = catalog();
        final before = subject.parseNodes({
          'profiles': [
            server('DE-1-9.52GB', host: '192.0.2.11'),
            server('DE-2-9.52GB', host: '192.0.2.12'),
            server('NL-1-9.52GB', host: '192.0.2.13'),
          ],
        }, []);
        before[0]
          ..['favorite'] = true
          ..['manualName'] = 'My Frankfurt'
          ..['ipPreference'] = 'ipv4';
        before[2]
          ..['lastSuccessAt'] = subject.now - 60
          ..['lastSuccessRevision'] = before[2]['configurationRevision'];
        final after = subject.parseNodes({
          'profiles': [
            server('DE-1-9.47GB', host: '192.0.2.11', sid: sid, sni: sni),
            server('DE-2-9.47GB', host: '192.0.2.12', sid: sid, sni: sni),
            server('NL-1-9.47GB', host: '192.0.2.13', sid: sid, sni: sni),
          ],
        }, before);
        expect(ids(after), ids(before), reason: '$sid $sni');
        expect(after[0]['favorite'], isTrue);
        expect(after[0]['manualName'], 'My Frankfurt');
        expect(after[0]['ipPreference'], 'ipv4');
        expect(after[0]['name'], 'DE-1-9.47GB');
        // The one that connected lately went through keepShortIds and keeps
        // its stored values and revision, so the running core stays.
        expect(keptShortIds, hasLength(1));
        expect(after[2]['config'], before[2]['config']);
        expect(
          after[2]['configurationRevision'],
          before[2]['configurationRevision'],
        );
        expect(after[2]['lastSuccessAt'], before[2]['lastSuccessAt']);
        // The others take the fetched values as a new revision.
        expect(
          after[1]['configurationRevision'],
          isNot(before[1]['configurationRevision']),
        );
      }
    });

    test('pairs one host listed N times in feed order', () {
      final subject = catalog();
      final before = subject.parseNodes({
        'profiles': [
          server('Host 1 · 9.52GB', sid: '01'),
          server('Host 2 · 9.52GB', sid: '02'),
          server('Host 3 · 9.52GB', sid: '03'),
        ],
      }, []);
      // The selected one is matched first by the exact identity, not here.
      subject.data['selectedNodeId'] = before[2]['id'];
      final after = subject.parseNodes({
        'profiles': [
          server('Host 1 · 9.47GB', sid: '11'),
          server('Host 2 · 9.47GB', sid: '12'),
          server('Host 3 · 9.47GB', sid: '13'),
        ],
      }, before);
      expect(ids(after), ids(before));
      // An exact match still comes first: the entry that now has the second
      // server's sid is the second server, and the rest pair in order.
      final exact = subject.parseNodes({
        'profiles': [
          server('Host 1 · 9.40GB', sid: '22'),
          server('Host 2 · 9.40GB', sid: '12'),
          server('Host 3 · 9.40GB', sid: '23'),
        ],
      }, after);
      expect(ids(exact), ids(before));
      final swapped = subject.parseNodes({
        'profiles': [
          server('Host 1 · 9.30GB', sid: '12'),
          server('Host 2 · 9.30GB', sid: '31'),
          server('Host 3 · 9.30GB', sid: '32'),
        ],
      }, exact);
      expect(ids(swapped), [before[1]['id'], before[0]['id'], before[2]['id']]);
    });

    test('leaves unequal counts and other formats to the name stages', () {
      final subject = catalog();
      final before = subject.parseNodes({
        'profiles': [
          server('Host 1 · 9.52GB', sid: '01'),
          server('Host 2 · 9.52GB', sid: '02'),
        ],
      }, []);
      // Two listed, one fetched: the one fetched is not guessed.
      final fewer = subject.parseNodes({
        'profiles': [server('Host 1 · 9.47GB', sid: '11')],
      }, before);
      expect(ids(fewer).single, isNot(anyOf(ids(before))));
      // A name the stages after this one know still decides.
      final named = subject.parseNodes({
        'profiles': [server('Host 2 · 9.52GB', sid: '11')],
      }, before);
      expect(ids(named), [before[1]['id']]);
      final more = subject.parseNodes({
        'profiles': [
          server('Host 1 · 9.47GB', sid: '11'),
          server('Host 2 · 9.47GB', sid: '12'),
          server('Host 3 · 9.47GB', sid: '13'),
        ],
      }, before);
      expect(ids(more).where(ids(before).contains), isEmpty);
      // The same server under another format is another server.
      final telegram = subject.parseNodes({
        'profiles': [
          {...server('Proxy · 9.52GB'), 'format': 'telegram'},
        ],
      }, []);
      final profile = subject.parseNodes({
        'profiles': [server('Proxy · 9.47GB', sid: '11')],
      }, telegram);
      expect(ids(profile).single, isNot(ids(telegram).single));
    });

    // The importer marks a provider's message line; a stored server carries
    // no such key.
    test('stores no notice mark on a server', () {
      final nodes = catalog().parseNodes({
        'profiles': [
          {...server('Host 1'), 'notice': true},
          server('Host 2', host: '192.0.2.12'),
        ],
      }, []);
      expect(nodes.where((node) => node.containsKey('notice')), isEmpty);
    });
  });

  // As on Android, a running tunnel moves to the provider's next server
  // instead of stopping silently when a refresh drops the selected one.
  // Providers write "📊 10GB left" or "Device limit reached" into the remark of
  // a line no server answers (N10-07); the importer marks such lines.
  group('provider message lines', () {
    test('count only in a feed whose own host is public', () {
      for (final url in [
        'https://sub.example/feed',
        'https://SUB.example:8443/feed?token=1',
        'http://192.0.3.1/feed',
        'http://10.0.0.1:2096/feed',
        'https://[2001:db9::1]/feed',
        // Names are not resolved, whatever they look like.
        'https://127.0.0.1.example/feed',
        'https://localhost.example/feed',
      ]) {
        expect(DesktopCatalog.noticesApply(url), isTrue, reason: url);
      }
      for (final url in [
        'http://127.0.0.1:8080/feed',
        'http://127.1.2.3/feed',
        'http://[::1]:8080/feed',
        'http://localhost/feed',
        'http://LocalHost:8080/feed',
        'http://0.0.0.0/feed',
        'http://[::]/feed',
        'http://192.0.2.1/feed',
        'http://198.51.100.7/feed',
        'http://203.0.113.255/feed',
        'http://[2001:db8::1]/feed',
        'http://[2001:DB8:ffff::1]/feed',
        'http://[::ffff:127.0.0.1]/feed',
        'http://[::ffff:192.0.2.1]/feed',
        'not a link',
        '',
      ]) {
        expect(DesktopCatalog.noticesApply(url), isFalse, reason: url);
      }
    });

    CatalogMap line(String name, {bool notice = false}) => {
      'format': 'xrayProfile',
      'name': name,
      'config': {
        'outbounds': [
          {
            'protocol': 'socks',
            'settings': {'address': '$name.example', 'port': 1080},
          },
        ],
      },
      if (notice) 'notice': true,
    };
    late List<CatalogMap> feed;
    DesktopCatalog catalog(String url) => DesktopCatalog(
      DesktopCatalog.empty(),
      (method, payload) async => switch (method) {
        'fetchSubscription' => {
          'body': 'feed',
          'headers': {},
          'url': payload['url'],
        },
        'importClientProfiles' => {'profiles': feed},
        _ => throw StateError(method),
      },
      (operation, payload) => switch (operation) {
        'catalogIdentity' ||
        'catalogStableIdentity' => {'identity': jsonEncode(payload)},
        _ => throw StateError(operation),
      },
    )..putSource({
        'id': 'feed',
        'name': 'Feed',
        'url': url,
        'format': 'source',
        'status': 'ready',
        'nodes': <CatalogMap>[],
      });
    SubscriptionGroup shown(DesktopCatalog catalog) => SubscriptionGroup.fromMap(
      catalogList(catalog.snapshot()['subscriptions']).single,
    );

    test('of a public feed are its notices: in feed order, each once, five '
        'at most, and no server', () async {
      feed = [
        line('DE-1'),
        line('📊 10GB left', notice: true),
        line('Device limit reached', notice: true),
        line('NL-1'),
        line('📊 10GB left', notice: true),
        line('✖ Package Ended', notice: true),
        line(' Renew at panel.example ', notice: true),
        line('Sixth', notice: true),
        line('Seventh', notice: true),
      ];
      final subscriptions = catalog('https://sub.example/feed');
      expect(await subscriptions.refresh('feed'), isNull);
      final group = shown(subscriptions);
      expect(group.notices, [
        '📊 10GB left',
        'Device limit reached',
        '✖ Package Ended',
        'Renew at panel.example',
        'Sixth',
      ]);
      expect(subscriptions.source('feed')['notices'], hasLength(5));
      expect([for (final node in group.nodes) node.name], ['DE-1', 'NL-1']);
      expect(subscriptions.selected, group.nodes.first.id);
    });

    test('of a feed from this device are servers, as before', () async {
      feed = [
        line('📊 10GB left', notice: true),
        line('DE-1'),
        line('Device limit reached', notice: true),
      ];
      final subscriptions = catalog('http://127.0.0.1:2096/feed');
      await subscriptions.refresh('feed');
      final group = shown(subscriptions);
      expect([for (final node in group.nodes) node.name], [
        '📊 10GB left',
        'DE-1',
        'Device limit reached',
      ]);
      expect(subscriptions.source('feed').containsKey('notices'), isFalse);
      expect(group.notices, isEmpty);
    });

    test('alone refresh a subscription to no server, and the next refresh '
        'replaces them', () async {
      feed = [
        line('Device limit reached', notice: true),
        line('✖ Package Ended', notice: true),
      ];
      final subscriptions = catalog('https://sub.example/feed');
      expect(await subscriptions.refresh('feed'), isNull);
      expect(subscriptions.source('feed')['status'], 'ready');
      expect(shown(subscriptions).nodes, isEmpty);
      expect(shown(subscriptions).notices, [
        'Device limit reached',
        '✖ Package Ended',
      ]);
      expect(subscriptions.selected, isNull);
      feed = [line('DE-1'), line('📊 9GB left', notice: true)];
      await subscriptions.refresh('feed');
      expect(shown(subscriptions).notices, ['📊 9GB left']);
      expect(subscriptions.selected, shown(subscriptions).nodes.single.id);
      feed = [line('DE-1')];
      await subscriptions.refresh('feed');
      expect(subscriptions.source('feed').containsKey('notices'), isFalse);
      expect(shown(subscriptions).notices, isEmpty);
    });

    test('pasted by hand are servers', () async {
      feed = [line('📊 10GB left', notice: true)];
      final subscriptions = catalog('https://sub.example/feed');
      final local = await subscriptions.importLocal(
        'socks://127.0.0.1:1080#📊 10GB left',
      );
      expect([for (final node in local.added) node['name']], ['📊 10GB left']);
      expect(subscriptions.source(local.id).containsKey('notices'), isFalse);
    });
  });

  test('a refresh that drops the selected server selects the next', () async {
    CatalogMap server(String name) => {
      'format': 'xrayProfile',
      'name': name,
      'config': {
        'outbounds': [
          {
            'protocol': 'vless',
            'settings': {'address': '$name.example', 'port': 443, 'id': name},
          },
        ],
      },
    };
    var feed = [server('a'), server('b')];
    final catalog = DesktopCatalog(
      DesktopCatalog.empty(),
      (method, payload) async => switch (method) {
        'fetchSubscription' => {
          'body': 'feed',
          'headers': {},
          'url': payload['url'],
        },
        'importClientProfiles' => {'profiles': feed},
        _ => throw StateError(method),
      },
      (operation, payload) => switch (operation) {
        'catalogIdentity' => {'identity': jsonEncode(payload)},
        'catalogStableIdentity' => {'identity': jsonEncode(payload)},
        _ => throw StateError(operation),
      },
    );
    catalog.putSource({
      'id': 'feed',
      'name': 'Feed',
      'url': 'https://feed.example/sub',
      'format': 'source',
      'status': 'ready',
      'nodes': <CatalogMap>[],
    });
    await catalog.refresh('feed');
    final nodes = catalogList(catalog.source('feed')['nodes']);
    final removed = nodes.first['id']! as String;
    catalog.data['selectedNodeId'] = removed;
    // The window says the server changed only when the refresh replaced it.
    await catalog.refresh('feed');
    expect(catalog.replacedSelection(removed), isFalse);
    feed = [server('b')];
    await catalog.refresh('feed');
    expect(catalog.selected, nodes.last['id']);
    expect(catalog.replacedSelection(removed), isTrue);
    expect(catalog.replacedSelection(null), isFalse);
    expect(catalog.replacedSelection(catalog.selected), isFalse);
    catalog.data['selectedNodeId'] = null;
    expect(catalog.replacedSelection(removed), isFalse);
  });

  // 3x-ui sends its Happ routing header to every client (3x-ui #5999).
  test(
    'routing addressed to Elarni wins over other clients\' routing',
    () async {
      var headers = <String, Object?>{};
      var directives = <String>[];
      final catalog = DesktopCatalog(
        DesktopCatalog.empty(),
        (method, payload) async => switch (method) {
          'fetchSubscription' => {
            'body': 'feed',
            'headers': headers,
            'url': payload['url'],
          },
          'importClientProfiles' => {
            'profiles': [
              {
                'format': 'xrayProfile',
                'name': 'a',
                'config': {
                  'outbounds': [
                    {
                      'protocol': 'vless',
                      'settings': {'address': 'a.example', 'port': 443},
                    },
                  ],
                },
              },
            ],
            'directives': directives,
          },
          _ => throw StateError(method),
        },
        (operation, payload) => switch (operation) {
          'catalogIdentity' => {'identity': jsonEncode(payload)},
          'decodeRouting' => {
            'import': {
              'action': 'add',
              'profile': {
                'Name': (payload['content']! as String).split('/').last,
              },
            },
          },
          'mergeManagedRouting' => {'routeModel': {}},
          _ => throw StateError(operation),
        },
      );
      catalog.putSource({
        'id': 'feed',
        'name': 'Feed',
        'url': 'https://feed.example/sub',
        'format': 'source',
        'status': 'ready',
        'nodes': <CatalogMap>[],
      });
      String? provided() =>
          catalog.routes
                  .where((route) => route['providerSubscriptionId'] == 'feed')
                  .singleOrNull?['name']
              as String?;
      headers = {'routing': 'elarni://routing/onadd/Elarni'};
      directives = ['happ://routing/onadd/Happ'];
      await catalog.refresh('feed');
      expect(provided(), 'Elarni');
      headers = {'routing': 'happ://routing/onadd/Happ'};
      directives = ['elarni://routing/onadd/Body'];
      await catalog.refresh('feed');
      expect(provided(), 'Body');
      // With nothing addressed to Elarni, Happ's routing still applies.
      directives = [];
      await catalog.refresh('feed');
      expect(provided(), 'Happ');
    },
  );

  // A provider's routing that Elarni refuses costs only that routing: the
  // servers still update, the subscription says so, and nothing of the
  // refused profile (such as an assignment to it) is left behind.
  test('a refused provider routing directive keeps the refresh', () async {
    var routing = '';
    var name = 'a';
    var directives = <String>[];
    final catalog = DesktopCatalog(
      DesktopCatalog.empty(),
      (method, payload) async => switch (method) {
        'fetchSubscription' => {
          'body': 'feed',
          'headers': {'routing': routing},
          'url': payload['url'],
        },
        'importClientProfiles' => {
          'profiles': [
            {
              'format': 'xrayProfile',
              'name': name,
              'config': {
                'outbounds': [
                  {
                    'protocol': 'vless',
                    'settings': {'address': '$name.example', 'port': 443},
                  },
                ],
              },
            },
          ],
          'directives': directives,
        },
        _ => throw StateError(method),
      },
      (operation, payload) => switch (operation) {
        'catalogIdentity' => {'identity': jsonEncode(payload)},
        'catalogStableIdentity' => {'identity': jsonEncode(payload)},
        // "Unreadable" does not decode; "Large" decodes but does not merge,
        // as a profile over the routing limits.
        'decodeRouting'
            when !(payload['content']! as String).endsWith('/Unreadable') =>
          {
            'import': {
              'action': 'add',
              'activate': true,
              'profile': {
                'Name': (payload['content']! as String).split('/').last,
              },
            },
          },
        'mergeManagedRouting'
            when catalogMap(payload['profile'])['Name'] != 'Large' =>
          {'routeModel': <String, Object?>{}},
        'decodeRouting' ||
        'mergeManagedRouting' => throw desktopError('invalid_routing'),
        _ => throw StateError(operation),
      },
    );
    catalog.putSource({
      'id': 'feed',
      'name': 'Feed',
      'url': 'https://feed.example/sub',
      'format': 'source',
      'status': 'ready',
      'nodes': <CatalogMap>[],
    });
    CatalogMap metadata() => catalogMap(catalog.source('feed')['metadata']);
    List<Object?> names() => [
      for (final node in catalogList(catalog.source('feed')['nodes']))
        node['name'],
    ];

    // A first download whose routing does not merge.
    routing = 'happ://routing/onadd/Large';
    await catalog.refresh('feed');
    expect(names(), ['a']);
    expect(metadata()['directiveNotice'], 'Unsupported provider directives');
    expect(catalog.routes, isEmpty);
    expect(catalog.assignments, isEmpty);
    catalog.validate();

    routing = 'happ://routing/onadd/Good';
    await catalog.refresh('feed');
    final good = catalog.routes.single['id'];
    expect(catalog.assignments, {'feed': good});
    expect(metadata()['directiveNotice'], isNull);

    // Refused later, the profile the provider sent before stays as it was.
    for (final (refused, server) in [
      ('happ://routing/onadd/Large', 'b'),
      ('happ://routing/Unreadable', 'c'),
    ]) {
      routing = refused;
      name = server;
      await catalog.refresh('feed');
      expect(names(), [server], reason: refused);
      expect(catalog.source('feed')['status'], 'ready');
      expect(
        metadata()['directiveNotice'],
        'Unsupported provider directives',
        reason: refused,
      );
      expect(catalog.routes.single['name'], 'Good');
      expect(catalog.assignments, {'feed': good});
      catalog.validate();
    }

    // A local import has no notice to show; its servers are added.
    directives = ['happ://routing/onadd/Large'];
    name = 'local';
    final imported = await catalog.importLocal('local');
    expect(imported.added.single['name'], 'local');
    expect(catalog.routes.single['name'], 'Good');
    catalog.validate();
  });

  // Recovery must keep working when a provider rotates its servers out.
  test('a refresh that drops failover servers keeps recovery valid', () async {
    CatalogMap server(String name) => {
      'format': 'xrayProfile',
      'name': name,
      'config': {
        'outbounds': [
          {
            'protocol': 'vless',
            'settings': {'address': '$name.example', 'port': 443, 'id': name},
          },
        ],
      },
    };
    var feed = [server('a'), server('b'), server('c')];
    final catalog = DesktopCatalog(
      DesktopCatalog.empty(),
      (method, payload) async => switch (method) {
        'fetchSubscription' => {
          'body': 'feed',
          'headers': {},
          'url': payload['url'],
        },
        'importClientProfiles' => {'profiles': feed},
        _ => throw StateError(method),
      },
      (operation, payload) => switch (operation) {
        'catalogIdentity' => {'identity': jsonEncode(payload)},
        'catalogStableIdentity' => {'identity': jsonEncode(payload)},
        _ => throw StateError(operation),
      },
    );
    catalog.putSource({
      'id': 'feed',
      'name': 'Feed',
      'url': 'https://feed.example/sub',
      'format': 'source',
      'status': 'ready',
      'nodes': <CatalogMap>[],
    });
    await catalog.refresh('feed');
    final ids = {
      for (final node in catalogList(catalog.source('feed')['nodes']))
        node['name']: node['id'],
    };
    catalog.data['selectedNodeId'] = ids['a'];
    catalog.data['automation'] = {
      'schemaVersion': 1,
      'enabled': true,
      'policy': 'orderedFallback',
      'subscriptionID': 'feed',
      'nodeIDs': [ids['a'], ids['b'], ids['c']],
      'targets': [
        {
          'id': 'check',
          'url': 'https://check.example/generate_204',
          'expectedStatus': 204,
          'expectedBody': '',
        },
      ],
    };
    List<Object?> automated() =>
        catalogMap(catalog.data['automation'])['nodeIDs'] as List;

    feed = [server('a'), server('c')];
    await catalog.refresh('feed');
    expect(automated(), [ids['a'], ids['c']]);
    DesktopAutomation.validate(catalogMap(catalog.data['automation']), catalog);

    // The selected server went too: the next one recovery checks takes over.
    feed = [server('d'), server('c')];
    await catalog.refresh('feed');
    expect(catalog.selected, ids['c']);
    expect(automated(), [ids['c']]);
    DesktopAutomation.validate(catalogMap(catalog.data['automation']), catalog);

    // None of its servers left: recovery turns off instead of failing.
    feed = [server('d')];
    await catalog.refresh('feed');
    expect(catalogMap(catalog.data['automation'])['enabled'], false);
    DesktopAutomation.validate(catalogMap(catalog.data['automation']), catalog);
  });

  test('deleting the failover subscription turns recovery off', () {
    final catalog = DesktopCatalog(
      DesktopCatalog.empty(),
      (method, payload) async => throw StateError(method),
      (operation, payload) => throw StateError(operation),
    );
    CatalogMap source(String id) => {
      'id': id,
      'name': id,
      'format': 'source',
      'status': 'ready',
      'nodes': [
        {'id': '$id-node', 'name': id, 'format': 'xrayProfile', 'config': {}},
      ],
    };
    catalog.putSource(source('primary'));
    catalog.putSource(source('backup'));
    catalog.data['selectedNodeId'] = 'primary-node';
    catalog.data['automation'] = {
      'schemaVersion': 1,
      'enabled': true,
      'policy': 'orderedFallback',
      'subscriptionID': 'primary',
      'nodeIDs': ['primary-node', 'backup-node'],
      'targets': [
        {
          'id': 'check',
          'url': 'https://check.example/generate_204',
          'expectedStatus': 204,
          'expectedBody': '',
        },
      ],
    };
    // Failover spans subscriptions; losing the backup's keeps the rest.
    catalog.data['subscriptions'] = [catalog.source('primary')];
    catalog.reconcileSelection();
    expect(catalogMap(catalog.data['automation'])['nodeIDs'], ['primary-node']);
    DesktopAutomation.validate(catalogMap(catalog.data['automation']), catalog);

    catalog.data['subscriptions'] = [source('other')];
    catalog.reconcileSelection();
    expect(catalogMap(catalog.data['automation'])['enabled'], false);
    DesktopAutomation.validate(catalogMap(catalog.data['automation']), catalog);
  });

  // Only the default format's response names the provider's other formats.
  test('a refresh in another format keeps the format links', () async {
    const source = 'https://feed.example/sub';
    const raw = 'https://feed.example/raw';
    final catalog = DesktopCatalog(
      DesktopCatalog.empty(),
      (method, payload) async => switch (method) {
        'fetchSubscription' => {
          'body': payload['url'] == source
              ? jsonEncode({
                  'info': {'subUrl': raw},
                })
              : 'share links',
          'headers': {},
          'url': payload['url'],
        },
        'importClientProfiles' => {
          'profiles': [
            {
              'format': 'xrayProfile',
              'name': 'Server',
              'config': {
                'outbounds': [
                  {
                    'protocol': 'vless',
                    'settings': {'id': 'server'},
                  },
                ],
              },
            },
          ],
        },
        _ => throw StateError(method),
      },
      (operation, payload) => switch (operation) {
        'catalogIdentity' => {'identity': jsonEncode(payload)},
        _ => throw StateError(operation),
      },
    );
    catalog.putSource({
      'id': 'feed',
      'name': 'Feed',
      'url': source,
      'format': 'source',
      'status': 'ready',
      'nodes': <CatalogMap>[],
    });
    await catalog.refresh('feed');
    expect(catalog.deviceIdentity('feed')['url'], source);
    await catalog.refresh('feed', format: 'raw');
    await catalog.refresh('feed');
    expect(catalog.source('feed')['format'], 'raw');
    expect(catalogMap(catalog.source('feed')['links'])['raw'], raw);
    // The device ID shown is named from the URL the chosen format fetches.
    expect(catalog.deviceIdentity('feed')['url'], raw);
  });

  test('a provider DNS that asks the system resolver is noted', () async {
    final catalog = DesktopCatalog(
      DesktopCatalog.empty(),
      (method, payload) async => switch (method) {
        'fetchSubscription' => {
          'body': 'profiles',
          'headers': {},
          'url': payload['url'],
        },
        'importClientProfiles' => {
          'profiles': [
            {
              'format': 'xrayProfile',
              'name': 'Server',
              'config': {
                'dns': {
                  'servers': ['localhost'],
                },
                'outbounds': [
                  {
                    'protocol': 'vless',
                    'settings': {'id': 'server'},
                  },
                ],
              },
            },
          ],
          'systemResolverDNS': true,
        },
        _ => throw StateError(method),
      },
      (operation, payload) => switch (operation) {
        'catalogIdentity' => {'identity': jsonEncode(payload)},
        _ => throw StateError(operation),
      },
    );
    catalog.putSource({
      'id': 'feed',
      'name': 'Feed',
      'url': 'https://feed.example/sub',
      'format': 'source',
      'status': 'ready',
      'nodes': <CatalogMap>[],
    });
    await catalog.refresh('feed');
    expect(
      catalogMap(catalog.source('feed')['metadata'])['dnsNotice'],
      startsWith("The provider's DNS asks this device's resolver"),
    );
  });

  test('servers the import skipped are counted and named', () async {
    var imported = <String, Object?>{};
    final catalog = DesktopCatalog(
      DesktopCatalog.empty(),
      (method, payload) async => switch (method) {
        'fetchSubscription' => {
          'body': 'feed',
          'headers': {},
          'url': payload['url'],
        },
        'importClientProfiles' => {
          'profiles': [
            {
              'format': 'xrayProfile',
              'name': 'Server',
              'config': {
                'outbounds': [
                  {
                    'protocol': 'vless',
                    'settings': {'id': 'server'},
                  },
                ],
              },
            },
          ],
          ...imported,
        },
        _ => throw StateError(method),
      },
      (operation, payload) => switch (operation) {
        'catalogIdentity' => {'identity': jsonEncode(payload)},
        _ => throw StateError(operation),
      },
    );
    catalog.putSource({
      'id': 'feed',
      'name': 'Feed',
      'url': 'https://feed.example/sub',
      'format': 'source',
      'status': 'ready',
      'nodes': <CatalogMap>[],
    });
    CatalogMap metadata() => catalogMap(catalog.source('feed')['metadata']);
    imported = {
      // The core lists at most 32; the count is of all of them.
      'skippedCount': 40,
      'skipped': [
        for (final kind in ['ssh', 'naive', 'ssh', '', 'mieru', 'juicity'])
          {'name': 'Server $kind', 'kind': kind},
        {'kind': 'anytls'},
        {'kind': 'snell'},
      ],
    };
    await catalog.refresh('feed');
    expect(metadata()['skippedServers'], 40);
    expect(metadata()['skippedKinds'], 'ssh, naive, mieru, juicity, anytls');
    // Nothing skipped on the next refresh: nothing is reported.
    imported = {};
    await catalog.refresh('feed');
    expect(metadata().containsKey('skippedServers'), isFalse);
    expect(metadata().containsKey('skippedKinds'), isFalse);
  });

  test('a refresh leaves out any server the compiler refuses but the last', () {
    final catalog = DesktopCatalog(
      DesktopCatalog.empty(),
      (method, payload) async => throw StateError(method),
      (operation, payload) => throw StateError(operation),
    );
    catalog.putSource({
      'id': 'feed',
      'name': 'Feed',
      'url': 'https://feed.example/sub',
      'format': 'source',
      'status': 'ready',
      'nodes': [
        for (final (id, protocol) in [('a', 'vless'), ('b', 'trojan')])
          {
            'id': id,
            'name': id,
            'format': 'xrayProfile',
            'config': {
              'outbounds': [
                {'protocol': protocol},
              ],
            },
          },
      ],
    });
    expect(catalog.dropRefused('feed', {'other'}), isFalse);
    expect(catalog.dropRefused('feed', {'a', 'b'}), isFalse);
    expect(catalog.dropRefused('feed', {'a'}), isTrue);
    expect(catalogList(catalog.source('feed')['nodes']).single['id'], 'b');
    final metadata = catalogMap(catalog.source('feed')['metadata']);
    expect(metadata['skippedServers'], 1);
    expect(metadata['skippedKinds'], 'vless');
    // A refresh moves to the provider's first server left.
    expect(catalog.selected, 'b');
  });

  test(
    'a local import leaves out only new servers the compiler refuses',
    () async {
      CatalogMap server(String name, [String protocol = 'vless']) => {
        'format': 'xrayProfile',
        'name': name,
        'config': {
          'outbounds': [
            {
              'protocol': protocol,
              'settings': {'id': name},
            },
          ],
        },
      };
      var imported = <String, Object?>{};
      final catalog = DesktopCatalog(
        DesktopCatalog.empty(),
        (method, payload) async => switch (method) {
          'importClientProfiles' => imported,
          _ => throw StateError(method),
        },
        (operation, payload) => switch (operation) {
          'catalogIdentity' => {'identity': jsonEncode(payload)},
          _ => throw StateError(operation),
        },
      );
      imported = {
        'profiles': [server('old')],
      };
      final first = await catalog.importLocal('old');
      final old = first.added.single['id']! as String;
      CatalogMap metadata() =>
          catalogMap(catalog.source(first.id)['metadata'] ?? const {});
      List<Object?> names() => [
        for (final node in catalogList(catalog.source(first.id)['nodes']))
          node['name'],
      ];

      imported = {
        'profiles': [server('old'), server('good'), server('bad', 'hysteria')],
        'skippedCount': 1,
        'skipped': [
          {'name': 'Server ssh', 'kind': 'ssh'},
        ],
      };
      final second = await catalog.importLocal('old, good and bad');
      expect(second.id, first.id);
      final added = {
        for (final node in second.added) node['name']: node['id']! as String,
      };
      expect(added.keys, ['good', 'bad']);
      // The importer's own report describes the latest import.
      expect(metadata()['skippedServers'], 1);
      expect(metadata()['skippedKinds'], 'ssh');
      // A server that was already there is not this import's to drop.
      expect(
        catalog.dropRefused(second.id, {
          old,
          added['bad']!,
        }, added: added.values.toSet()),
        isTrue,
      );
      expect(names(), ['old', 'good']);
      expect(metadata()['skippedServers'], 2);
      expect(metadata()['skippedKinds'], 'ssh, hysteria');
      // Unlike a refresh, an import selects nothing.
      expect(catalog.selected, isNull);

      // Nothing new would be left: nothing is dropped, and the import fails.
      imported = {
        'profiles': [server('good'), server('worse', 'tuic')],
      };
      final third = await catalog.importLocal('good and worse');
      // This import skipped nothing, so the last one's count goes.
      expect(metadata().containsKey('skippedServers'), isFalse);
      expect(metadata().containsKey('skippedKinds'), isFalse);
      final worse = {for (final node in third.added) node['id']! as String};
      expect(worse, hasLength(1));
      expect(catalog.dropRefused(third.id, worse, added: worse), isFalse);
      expect(names(), ['old', 'good', 'worse']);
      expect(metadata().containsKey('skippedServers'), isFalse);
    },
  );

  test('a catalog over its limits says so instead of reading as corrupt', () {
    for (final (key, count) in [('subscriptions', 1025), ('profiles', 257)]) {
      final catalog = DesktopCatalog(
        {
          ...DesktopCatalog.empty(),
          key: [
            for (var i = 0; i < count; i++) {'id': '$i'},
          ],
        },
        (method, payload) async => throw StateError(method),
        (operation, payload) => throw StateError(operation),
      );
      expect(
        catalog.validate,
        throwsA(
          isA<PlatformException>().having(
            (e) => e.code,
            'code',
            'catalog_limit',
          ),
        ),
        reason: key,
      );
    }
  });
}
