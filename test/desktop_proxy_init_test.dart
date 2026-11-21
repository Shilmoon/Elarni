import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:elarni/desktop_catalog.dart';
import 'package:elarni/desktop_proxy.dart';
import 'package:elarni/system_proxy.dart';
import 'package:elarni/tunnel_channel.dart';

// An OS setting that is either the user's own or a port Elarni pointed it at.
final class _FakeOS implements SystemProxy {
  ProxySettings current = {'user': 'pac'};

  @override
  Future<ProxySettings> read() async => {...current};

  @override
  Future<void> point(int port) async => current = {'port': port};

  final restored = Completer<void>();

  @override
  Future<void> write(ProxySettings settings) async {
    current = {...settings};
    if (!restored.isCompleted) restored.complete();
  }

  @override
  bool pointsAt(ProxySettings settings, int port) => settings['port'] == port;
}

// A core that answers each request with [answer]'s reply, if any, and exits
// when its input closes; a [oneShot] core exits after its reply.
final class _FakeCore implements Process {
  _FakeCore(
    FutureOr<String?> Function(Map request) answer, {
    bool oneShot = false,
  }) {
    stdin = _Requests((line) async {
      final reply = await answer(jsonDecode(line) as Map);
      if (reply != null && !_exit.isCompleted)
        _output.add(utf8.encode('$reply\n'));
      if (oneShot) kill();
    }, kill);
  }
  final _output = StreamController<List<int>>();
  final _exit = Completer<int>();
  @override
  late final IOSink stdin;
  @override
  Stream<List<int>> get stdout => _output.stream;
  @override
  Stream<List<int>> get stderr => const Stream.empty();
  @override
  Future<int> get exitCode => _exit.future;
  @override
  int get pid => 1;
  @override
  bool kill([ProcessSignal signal = ProcessSignal.sigterm]) {
    if (!_exit.isCompleted) {
      _exit.complete(0);
      _output.close();
    }
    return true;
  }
}

final class _Requests implements IOSink {
  _Requests(this._line, this._closed);
  final void Function(String) _line;
  final void Function() _closed;
  @override
  void writeln([Object? object = '']) => _line('$object');
  @override
  Future<void> flush() async {}
  @override
  Future<void> close() async => _closed();
  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  // The UI polls state every second; a locked keyring or a second instance must
  // not respawn the core or re-prompt for the keyring on every poll.
  test('a failed catalog load is retried only after a pause', () async {
    var loads = 0;
    final proxy = DesktopProxy(
      corePath: 'unused-core',
      domainPath: 'unused-domain',
      catalogIO: (method, _) async {
        if (method == 'loadCatalog') loads++;
        throw PlatformException(code: 'storage_busy');
      },
      platformIO: (_, _) async => {},
    );
    addTearDown(proxy.dispose);
    for (var i = 0; i < 3; i++) {
      await expectLater(
        proxy.control.subscriptions(),
        throwsA(isA<PlatformException>()),
      );
    }
    expect(loads, 1);
  });

  // The window words the code in its language.
  test('a client link that cannot be imported is reported by code', () async {
    final notices = <String?>[];
    final proxy = DesktopProxy(
      corePath: 'unused-core',
      domainPath: 'unused-domain',
      catalogIO: (_, _) async => throw PlatformException(code: 'storage_busy'),
      platformIO: (_, _) async => {},
      confirmLink: (_) async => true,
    );
    addTearDown(proxy.dispose);
    proxy.control.setImportListener(notices.add);
    await proxy.handleLinks([
      'vless://00000000-0000-4000-8000-000000000001@127.0.0.1:443#Fixture',
    ]);
    expect(notices, ['links_failed']);
  });

  // Both ShadowQUIC schemes the system hands over are offered for import, as
  // the other client links are; a scheme Elarni does not take is not.
  test('ShadowQUIC links from the system are offered for import', () async {
    final asked = <String>[];
    final proxy = DesktopProxy(
      corePath: 'unused-core',
      domainPath: 'unused-domain',
      catalogIO: (_, _) async => throw PlatformException(code: 'storage_busy'),
      platformIO: (_, _) async => {},
      confirmLink: (prompt) async {
        asked.add(prompt);
        return false;
      },
    );
    addTearDown(proxy.dispose);
    await proxy.handleLinks([
      'sq://user:secret@127.0.0.1:443?sni=fixture.example#One',
      'SHADOWQUIC://user:secret@127.0.0.1:443?sni=fixture.example#Two',
      'unknown://user:secret@127.0.0.1:443#Three',
    ]);
    expect(asked, ['importClient', 'importClient']);
  });

  // The proxy setting and its backup belong to the instance that holds the
  // catalog; a second one is refused before it may "recover" them.
  test('a refused second instance leaves the system proxy alone', () async {
    final temporary = await Directory.systemTemp.createTemp('elarni-second-');
    addTearDown(() => temporary.delete(recursive: true));
    final backup = File('${temporary.path}/Elarni/system-proxy.json');
    final os = _FakeOS();
    expect(await SystemProxySession(os, backup).sync(10891), isNull);

    final second = DesktopProxy(
      corePath: 'unused-core',
      domainPath: 'unused-domain',
      catalogIO: (_, _) async => throw PlatformException(code: 'storage_busy'),
      platformIO: (_, _) async => {},
      systemProxy: SystemProxySession(os, backup),
    );
    addTearDown(second.dispose);
    await expectLater(
      second.control.subscriptions(),
      throwsA(isA<PlatformException>()),
    );
    expect(os.current, {'port': 10891});
    expect(await backup.exists(), isTrue);
  });

  // A crashed run's setting is put back even while the catalog cannot be read.
  test('a locked catalog does not delay the system proxy recovery', () async {
    final temporary = await Directory.systemTemp.createTemp('elarni-locked-');
    addTearDown(() => temporary.delete(recursive: true));
    final backup = File('${temporary.path}/Elarni/system-proxy.json');
    final os = _FakeOS();
    expect(await SystemProxySession(os, backup).sync(10891), isNull);

    final next = DesktopProxy(
      corePath: 'unused-core',
      domainPath: 'unused-domain',
      catalogIO: (_, _) async =>
          throw PlatformException(code: 'storage_unavailable'),
      platformIO: (_, _) async => {},
      systemProxy: SystemProxySession(os, backup),
    );
    addTearDown(next.dispose);
    await expectLater(
      next.control.subscriptions(),
      throwsA(isA<PlatformException>()),
    );
    expect(os.current, {'user': 'pac'});
    expect(await backup.exists(), isFalse);
  });

  // Reading the catalog can wait on a keyring prompt. The lease already says
  // this is the only instance, so the dead proxy setting does not wait too.
  test(
    'a crashed run\'s proxy is put back before the catalog is read',
    () async {
      final temporary = await Directory.systemTemp.createTemp('elarni-prompt-');
      addTearDown(() => temporary.delete(recursive: true));
      final backup = File('${temporary.path}/Elarni/system-proxy.json');
      final os = _FakeOS();
      expect(await SystemProxySession(os, backup).sync(10891), isNull);

      final release = Completer<void>();
      final proxy = DesktopProxy(
        corePath: 'unused-core',
        domainPath: 'unused-domain',
        // The lease is granted; the catalog read answers when released.
        startProcess: (_) async => _FakeCore(
          (request) => request['method'] == 'acquireCatalog'
              ? '{"success":true}'
              : release.future.then(
                  (_) => '{"success":false,"error":"storage_unavailable"}',
                ),
        ),
        platformIO: (_, _) async => {},
        systemProxy: SystemProxySession(os, backup),
      );
      final loading = proxy.control.subscriptions();
      unawaited(loading.then<void>((_) {}, onError: (Object _) {}));
      await os.restored.future.timeout(const Duration(seconds: 5));
      expect(os.current, {'user': 'pac'});

      release.complete();
      await expectLater(loading, throwsA(isA<PlatformException>()));
      expect(await backup.exists(), isFalse);
      await proxy.dispose();
    },
  );

  // A catalog that decrypts but is not a catalog says so, and stays as it is
  // on disk for a later version or a repair to read.
  test('a catalog of the wrong shape is corrupt, not a crash', () async {
    for (final document in <Object>[
      'x',
      [1],
      {'version': 1, 'committed': 'x'},
      {
        'version': 1,
        'committed': {...DesktopCatalog.empty(), 'subscriptions': 'x'},
      },
      {
        'version': 1,
        'committed': {
          ...DesktopCatalog.empty(),
          'profiles': [1],
        },
      },
    ]) {
      final calls = <String>[];
      final proxy = DesktopProxy(
        corePath: 'unused-core',
        domainPath: 'unused-domain',
        catalogIO: (method, _) async {
          calls.add(method);
          return {'revision': 'r', 'catalog': document};
        },
        platformIO: (_, _) async => {},
      );
      await expectLater(
        proxy.control.subscriptions(),
        throwsA(
          isA<PlatformException>().having(
            (error) => error.code,
            'code',
            'storage_corrupt',
          ),
        ),
        reason: '$document',
      );
      expect(calls, ['loadCatalog'], reason: '$document');
      await proxy.dispose();
    }
  });

  // A catalog nothing can read again (a damaged file, a profile whose keys
  // changed, a reset keyring) blocks every command. Reset sets it aside
  // through the store and loads again, so the app starts empty.
  group('resetting unreadable storage', () {
    Matcher refused(String code) => throwsA(
      isA<PlatformException>().having((error) => error.code, 'code', code),
    );

    // The store answers from [stored] until something is set aside; then it
    // holds no catalog until the next save.
    ({DesktopProxy proxy, List<String> calls, List<CatalogMap> payloads}) store(
      Future<CatalogMap> Function() stored, {
      Future<void>? setAside,
    }) {
      final calls = <String>[], payloads = <CatalogMap>[];
      var reset = false;
      final proxy = DesktopProxy(
        corePath: 'unused-core',
        domainPath: 'unused-domain',
        catalogIO: (method, payload) async {
          // Windows and Linux also ask the lease about a left-over VPN block
          // once the catalog is loaded; that is no catalog command.
          if (method == 'staleVPNGuard') return {'stale': false};
          calls.add(method);
          payloads.add(payload);
          switch (method) {
            case 'setAsideCatalog':
              await setAside;
              reset = true;
              return {
                'setAside': true,
                'name': 'catalog.unreadable-20261121T120000Z.enc',
              };
            case 'saveCatalog':
              return {'revision': 'r-new'};
            case 'loadCatalog':
              return reset ? {'revision': ''} : await stored();
          }
          throw PlatformException(code: 'invalid_request');
        },
        platformIO: (_, _) async => {},
      );
      addTearDown(proxy.dispose);
      return (proxy: proxy, calls: calls, payloads: payloads);
    }

    test('a corrupt store is reset once and starts empty', () async {
      final (:proxy, :calls, :payloads) = store(
        () async => throw PlatformException(code: 'storage_corrupt'),
      );
      await expectLater(
        proxy.control.subscriptions(),
        refused('storage_corrupt'),
      );
      expect(await proxy.request('storageStatus'), {
        'code': 'storage_corrupt',
        'resettable': true,
      });
      expect(calls, ['loadCatalog'], reason: 'asking loads nothing');
      expect(await proxy.request('resetStorage'), {
        'setAside': true,
        'name': 'catalog.unreadable-20261121T120000Z.enc',
        'state': 'disconnected',
      });
      expect(calls, [
        'loadCatalog',
        'setAsideCatalog',
        'loadCatalog',
        'saveCatalog',
      ]);
      expect(payloads[1], {'revision': ''});
      final saved = catalogMap(catalogMap(payloads[3]['catalog'])['committed']);
      expect(saved['subscriptions'], isEmpty);
      expect(saved['profiles'], isEmpty);
      expect(await proxy.request('state'), {'state': 'disconnected'});
      expect((await proxy.control.subscriptions()).subscriptions, isEmpty);
      expect(await proxy.request('storageStatus'), {
        'code': null,
        'resettable': false,
      });
      // A readable store is never set aside.
      await expectLater(proxy.request('resetStorage'), refused('busy'));
      expect(calls.where((call) => call == 'setAsideCatalog'), hasLength(1));
    });

    test('a busy, locked or changed store is not reset', () async {
      for (final code in [
        'storage_busy',
        'storage_unavailable',
        'storage_conflict',
      ]) {
        final (:proxy, :calls, payloads: _) = store(
          () async => throw PlatformException(code: code),
        );
        await expectLater(proxy.control.subscriptions(), refused(code));
        expect(await proxy.request('storageStatus'), {
          'code': code,
          'resettable': false,
        }, reason: code);
        await expectLater(proxy.request('resetStorage'), refused('busy'));
        expect(calls, ['loadCatalog'], reason: code);
        await proxy.dispose();
      }
    });

    // The file decrypts, but this app cannot use it: only that revision is
    // set aside, so a catalog another copy saved meanwhile stays.
    test('a rejected catalog is set aside by its revision', () async {
      final (:proxy, :calls, :payloads) = store(
        () async => {'revision': 'r-rejected', 'catalog': 'x'},
      );
      await expectLater(
        proxy.control.subscriptions(),
        refused('storage_corrupt'),
      );
      expect((await proxy.request('storageStatus'))['resettable'], isTrue);
      expect((await proxy.request('resetStorage'))['setAside'], isTrue);
      expect(calls.take(2), ['loadCatalog', 'setAsideCatalog']);
      expect(payloads[1], {'revision': 'r-rejected'});
    });

    // Neither the retry pause nor the window's polling loads the store
    // between the set-aside and the reset's own load.
    test('nothing loads between the set-aside and its load', () async {
      final timers = <Timer>[];
      await runZoned(
        () async {
          final release = Completer<void>();
          final (:proxy, :calls, payloads: _) = store(
            () async => throw PlatformException(code: 'storage_corrupt'),
            setAside: release.future,
          );
          await expectLater(
            proxy.control.subscriptions(),
            refused('storage_corrupt'),
          );
          final retry = timers.where((timer) => timer.isActive).toList();
          expect(retry, hasLength(1), reason: 'the failed load armed a retry');
          final reset = proxy.request('resetStorage');
          await pumpEventQueue();
          expect(calls, ['loadCatalog', 'setAsideCatalog']);
          expect(retry.single.isActive, isFalse);
          final polled = proxy.request('state');
          final asked = proxy.request('storageStatus');
          await pumpEventQueue();
          expect(calls, ['loadCatalog', 'setAsideCatalog']);
          release.complete();
          expect((await reset)['setAside'], isTrue);
          expect(await polled, {'state': 'disconnected'});
          expect(await asked, {'code': null, 'resettable': false});
          expect(calls, [
            'loadCatalog',
            'setAsideCatalog',
            'loadCatalog',
            'saveCatalog',
          ]);
        },
        zoneSpecification: ZoneSpecification(
          createTimer: (self, parent, zone, duration, callback) {
            final timer = parent.createTimer(zone, duration, callback);
            timers.add(timer);
            return timer;
          },
        ),
      );
    });

    // A reset that cannot finish leaves the store as it was and retries the
    // load after the usual pause.
    test('a failed set-aside is reported and retried', () async {
      final timers = <Timer>[];
      await runZoned(
        () async {
          final calls = <String>[];
          final proxy = DesktopProxy(
            corePath: 'unused-core',
            domainPath: 'unused-domain',
            catalogIO: (method, _) async {
              calls.add(method);
              throw PlatformException(
                code: method == 'setAsideCatalog'
                    ? 'storage_unavailable'
                    : 'storage_corrupt',
              );
            },
            platformIO: (_, _) async => {},
          );
          await expectLater(
            proxy.control.subscriptions(),
            refused('storage_corrupt'),
          );
          await expectLater(
            proxy.request('resetStorage'),
            refused('storage_unavailable'),
          );
          expect(await proxy.request('storageStatus'), {
            'code': 'storage_unavailable',
            'resettable': false,
          });
          expect(timers.where((timer) => timer.isActive), hasLength(1));
          expect(calls, ['loadCatalog', 'setAsideCatalog']);
          await proxy.dispose();
        },
        zoneSpecification: ZoneSpecification(
          createTimer: (self, parent, zone, duration, callback) {
            final timer = parent.createTimer(zone, duration, callback);
            timers.add(timer);
            return timer;
          },
        ),
      );
    });
  });

  // What automation was doing when the app last stopped is not worth the catalog.
  test('an unreadable automation episode starts a new one', () async {
    for (final episode in <Object>[
      [1],
      {'attempts': '1'},
    ]) {
      final proxy = DesktopProxy(
        corePath: 'unused-core',
        domainPath: 'unused-domain',
        catalogIO: (method, _) async => {
          'revision': 'r',
          'catalog': {
            'version': 1,
            'committed': {
              ...DesktopCatalog.empty(),
              'automationEpisode': episode,
            },
          },
        },
        platformIO: (_, _) async => {},
      );
      expect(
        await proxy.control.subscriptions(),
        isA<SubscriptionSnapshot>(),
        reason: '$episode',
      );
      await proxy.dispose();
    }
  });

  // A daemon whose catalog never opened must exit at once, not after the retry pause.
  test('dispose cancels the retry pause of a failed load', () async {
    final timers = <Timer>[];
    await runZoned(
      () async {
        final proxy = DesktopProxy(
          corePath: 'unused-core',
          domainPath: 'unused-domain',
          catalogIO: (_, _) async =>
              throw PlatformException(code: 'storage_busy'),
          platformIO: (_, _) async => {},
        );
        await expectLater(
          proxy.control.subscriptions(),
          throwsA(isA<PlatformException>()),
        );
        await proxy.dispose();
      },
      zoneSpecification: ZoneSpecification(
        createTimer: (self, parent, zone, duration, callback) {
          final timer = parent.createTimer(zone, duration, callback);
          timers.add(timer);
          return timer;
        },
      ),
    );
    expect(timers.where((timer) => timer.isActive), isEmpty);
  });

  // The host picks the path; Dart writes the bytes, as it copies a recording.
  test('a saved file gets its bytes at the picked path', () async {
    final temporary = await Directory.systemTemp.createTemp('elarni-save-');
    addTearDown(() => temporary.delete(recursive: true));
    final target = File('${temporary.path}/elarni-qr.png');
    final picks = <Object?>[];
    String? picked = target.path;
    final proxy = DesktopProxy(
      corePath: 'unused-core',
      domainPath: 'unused-domain',
      catalogIO: (_, _) async => {
        'revision': 'r',
        'catalog': {'version': 1, 'committed': DesktopCatalog.empty()},
      },
      platformIO: (method, payload) async {
        expect(method, 'pickSavePath');
        picks.add(payload['kind']);
        if (picked == null) throw desktopError('cancelled');
        return {'path': picked};
      },
    );
    addTearDown(proxy.dispose);
    final bytes = Uint8List.fromList([0x89, 0x50, 0x4e, 0x47, 0, 255]);
    expect(await proxy.control.saveFile('png', bytes), isTrue);
    expect(await target.readAsBytes(), bytes);
    // A cancelled panel writes nothing and is no failure.
    picked = null;
    expect(
      await proxy.control.saveFile('json', Uint8List.fromList([1])),
      isFalse,
    );
    expect(await target.readAsBytes(), bytes);
    // A path that cannot be written is a failure.
    picked = '${temporary.path}/missing/elarni-share.txt';
    await expectLater(
      proxy.control.saveFile('text', Uint8List.fromList([1])),
      throwsA(
        isA<PlatformException>().having(
          (error) => error.code,
          'code',
          'save_failed',
        ),
      ),
    );
    // A kind the hosts do not name is refused before any panel opens.
    await expectLater(
      proxy.control.saveFile('zip', Uint8List.fromList([1])),
      throwsA(
        isA<PlatformException>().having(
          (error) => error.code,
          'code',
          'invalid_request',
        ),
      ),
    );
    expect(picks, ['png', 'json', 'text']);
  });

  // The old 256 TCP / 128 UDP defaults refused new TUN flows and DNS under
  // daily load. Loading raises them once; later choices survive reloads.
  test('loading a catalog raises old default session limits once', () async {
    Future<(TunnelSettings, CatalogMap?)> load(
      int tcp,
      int udp, {
      required bool migrated,
    }) async {
      final committed = DesktopCatalog.empty()
        ..['settings'] = const TunnelSettings(
          xrayTunEnabled: true,
        ).copyWith(tcpMaxConnections: tcp, udpMaxConnections: udp).toMap();
      if (!migrated) committed.remove('sessionLimitsMigrated');
      CatalogMap? saved;
      final proxy = DesktopProxy(
        corePath: 'unused-core',
        domainPath: 'unused-domain',
        catalogIO: (method, payload) async {
          if (method == 'saveCatalog') {
            saved = catalogMap(catalogMap(payload['catalog'])['committed']);
            return {'revision': 'r2'};
          }
          return {
            'revision': 'r1',
            'catalog': {'version': 1, 'committed': committed},
          };
        },
        platformIO: (_, _) async => {},
      );
      addTearDown(proxy.dispose);
      return (await proxy.control.tunnelSettings(), saved);
    }

    expect(const TunnelSettings().tcpMaxConnections, 2048);
    expect(TunnelSettings.fromMap({}).udpMaxConnections, 1024);

    var (settings, saved) = await load(256, 128, migrated: false);
    expect(
      [settings.tcpMaxConnections, settings.udpMaxConnections],
      [2048, 1024],
    );
    expect(saved?['sessionLimitsMigrated'], true);
    expect(
      TunnelSettings.fromMap(catalogMap(saved?['settings'])).tcpMaxConnections,
      2048,
    );

    (settings, saved) = await load(512, 128, migrated: false);
    expect(
      [settings.tcpMaxConnections, settings.udpMaxConnections],
      [512, 1024],
    );

    (settings, saved) = await load(256, 128, migrated: true);
    expect(
      [settings.tcpMaxConnections, settings.udpMaxConnections],
      [256, 128],
    );
    expect(saved, isNull);
  });

  // As on macOS, iOS and Android: a local group's servers share as links in
  // group order, each named as the list shows it. One without a link is
  // refused with its name, and only a local group's servers are shared.
  test(
    'a local group shares its servers as named links in group order',
    () async {
      CatalogMap outbound(String protocol) => {
        'protocol': protocol,
        'tag': 'proxy',
        'settings': {'address': '127.0.0.1', 'port': 1080},
      };
      CatalogMap server(
        String id,
        String name,
        List<String> protocols, {
        Map<String, Object?> extra = const {},
      }) => {
        'id': id,
        'name': name,
        'format': 'xrayProfile',
        'config': {
          'outbounds': [for (final protocol in protocols) outbound(protocol)],
        },
        ...extra,
      };
      const telegram =
          'tg://proxy?server=127.0.0.1&port=443&secret=0123456789abcdef0123456789abcdef';
      final committed = DesktopCatalog.empty()
        ..['subscriptions'] = [
          {
            'id': 'local',
            'name': 'Local profiles',
            'status': 'ready',
            'format': 'source',
            'nodes': [
              server(
                'socks',
                'Imported name',
                ['socks'],
                extra: {'manualName': 'Renamed'},
              ),
              server('chain', 'Chain', ['socks', 'socks']),
              {
                'id': 'telegram',
                'name': 'Telegram',
                'format': 'telegram',
                'config': {'url': telegram},
              },
              server('web', 'Web', ['http']),
              server('lossy', 'Lossy', ['vless']),
            ],
          },
          // A hidden import keeps its own local group.
          {
            'id': 'hidden',
            'name': 'Local profiles',
            'status': 'ready',
            'format': 'source',
            'nodes': [
              server('secret', 'Secret', ['socks'], extra: {'hideURL': true}),
            ],
          },
          {
            'id': 'provider',
            'name': 'Provider',
            'url': 'https://127.0.0.1/synthetic-token',
            'status': 'needsApproval',
            'format': 'source',
            'metadata': {'hideURL': false},
            'nodes': [
              server('remote', 'Remote', ['socks']),
            ],
          },
        ];
      final converted = <Object?>[];
      final proxy = DesktopProxy(
        corePath: 'unused-core',
        domainPath: 'unused-domain',
        catalogIO: (_, _) async => {
          'revision': 'r',
          'catalog': {'version': 1, 'committed': committed},
        },
        platformIO: (_, _) async => {},
        // The core writes a SOCKS server's link, gives a VLESS server back as
        // its JSON, as for a link that would lose settings, and has no link for
        // an HTTP proxy.
        startProcess: (_) async => _FakeCore((request) {
          if (request['method'] != 'convertXrayJsonToShareLinks')
            return '{"success":false,"error":"invalid_request"}';
          final json = (request['payload'] as Map)['xrayJson'] as String;
          converted.add(jsonDecode(json));
          final config = catalogMap(jsonDecode(json));
          final outbound = catalogList(config['outbounds']).single;
          return jsonEncode(switch (outbound['protocol']) {
            'socks' => {
              'success': true,
              'data': {'links': 'socks://127.0.0.1:1080#${outbound['tag']}'},
            },
            'vless' => {
              'success': true,
              'data': {'links': json},
            },
            _ => {'success': false, 'error': 'sharing_failed'},
          });
        }, oneShot: true),
      );
      addTearDown(proxy.dispose);
      Matcher refused(String code, [String? name]) => throwsA(
        isA<PlatformException>()
            .having((error) => error.code, 'code', code)
            .having((error) => error.details, 'details', name),
      );
      expect(
        await proxy.control.shareLinks('local', ['telegram', 'socks']),
        'socks://127.0.0.1:1080#Renamed\n$telegram',
      );
      // Only the server's outbound goes to the core, tagged with its name.
      expect(converted, [
        {
          'outbounds': [
            {...outbound('socks'), 'tag': 'Renamed'},
          ],
        },
      ]);
      await expectLater(
        proxy.control.shareLinks('local', ['socks', 'chain']),
        refused('no_share_link', 'Chain'),
      );
      await expectLater(
        proxy.control.shareLinks('local', ['web']),
        refused('no_share_link', 'Web'),
      );
      await expectLater(
        proxy.control.shareLinks('local', ['lossy']),
        refused('no_share_link', 'Lossy'),
      );
      await expectLater(
        proxy.control.shareLinks('hidden', ['secret']),
        refused('sharing_disabled'),
      );
      await expectLater(
        proxy.control.shareLinks('provider', ['remote']),
        refused('invalid_profile'),
      );
      // Servers of another group are not this group's.
      await expectLater(
        proxy.control.shareLinks('local', ['remote']),
        refused('invalid_profile'),
      );
    },
  );
}
