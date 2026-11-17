import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:elarni/desktop_integration.dart';
import 'package:elarni/desktop_proxy.dart';
import 'package:elarni/elarni_app.dart';
import 'package:elarni/elarni_widgets.dart';
import 'package:elarni/main.dart' show relaunchTarget;
import 'package:elarni/update_check.dart';
import 'elarni_app_test.dart' show selectCategory, reveal;
import 'support/fake_tunnel_control.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  final messenger =
      TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger;
  const channel = DesktopIntegration.channel;

  test(
    'a feed names a release in Elarni\'s own form or as a GitHub release',
    () {
      expect(
        parseUpdateFeed(
          '{"version":"0.4.0","url":"https://mirror.example/elarni/"}',
        ),
        (version: '0.4.0', url: 'https://mirror.example/elarni/'),
      );
      expect(
        parseUpdateFeed(
          jsonEncode({
            // GitHub's own url is the API address, not the release page.
            'url': 'https://api.github.com/repos/o/elarni/releases/1',
            'tag_name': 'v0.4.1',
            'html_url': 'https://github.com/o/elarni/releases/tag/v0.4.1',
            'assets': [],
          }),
        ),
        (
          version: '0.4.1',
          url: 'https://github.com/o/elarni/releases/tag/v0.4.1',
        ),
      );
      for (final body in [
        'not json',
        '[]',
        '{"version":"0.4.0"}',
        '{"version":"0.4.0","url":"http://mirror.example/"}',
        '{"version":"0.4.0","url":"javascript:alert(1)"}',
        '{"version":"next","url":"https://mirror.example/"}',
        '{"version":4,"url":"https://mirror.example/"}',
      ])
        expect(parseUpdateFeed(body), isNull, reason: body);
    },
  );

  test('a feed lists its release files in GitHub\'s form or its own', () {
    expect(
      parseUpdateAssets(
        jsonEncode({
          'tag_name': 'v0.7.0',
          'html_url': 'https://mirror.example/elarni/0.7.0',
          'assets': [
            {
              'name': 'Elarni-0.7.0-x64.msi',
              // GitHub's own url is the API address, not the file.
              'url': 'https://api.github.com/repos/o/elarni/releases/assets/1',
              'browser_download_url':
                  'https://mirror.example/elarni/Elarni-0.7.0-x64.msi',
            },
            {
              'name': 'SHA256SUMS',
              'browser_download_url':
                  'https://mirror.example/elarni/SHA256SUMS',
            },
          ],
        }),
      ),
      [
        (
          name: 'Elarni-0.7.0-x64.msi',
          url: 'https://mirror.example/elarni/Elarni-0.7.0-x64.msi',
        ),
        (name: 'SHA256SUMS', url: 'https://mirror.example/elarni/SHA256SUMS'),
      ],
    );
    expect(
      parseUpdateAssets(
        jsonEncode({
          'version': '0.7.0',
          'url': 'https://mirror.example/elarni/',
          'assets': [
            {
              'name': 'elarni_0.7.0_amd64.deb',
              'url': 'https://mirror.example/a',
            },
            {'name': 'http', 'url': 'http://mirror.example/b'},
            {'name': 'script', 'url': 'javascript:alert(1)'},
            {'name': 7, 'url': 'https://mirror.example/c'},
            {'url': 'https://mirror.example/d'},
            {'name': 'no address'},
            'https://mirror.example/e',
            null,
          ],
        }),
      ),
      [(name: 'elarni_0.7.0_amd64.deb', url: 'https://mirror.example/a')],
    );
    final many = parseUpdateAssets(
      jsonEncode({
        'version': '0.7.0',
        'url': 'https://mirror.example/elarni/',
        'assets': [
          for (var i = 0; i < 300; i++)
            {'name': 'file$i', 'url': 'https://mirror.example/$i'},
        ],
      }),
    );
    expect(many, hasLength(200));
    expect(many.last, (name: 'file199', url: 'https://mirror.example/199'));
    for (final body in [
      'not json',
      '[]',
      '{"version":"0.7.0","url":"https://mirror.example/"}',
      '{"version":"0.7.0","url":"https://mirror.example/","assets":{}}',
    ])
      expect(parseUpdateAssets(body), isEmpty, reason: body);
  });

  test('versions compare by number and ignore the build', () {
    expect(compareVersions('0.3.10', '0.3.9'), greaterThan(0));
    expect(compareVersions('v0.4.0', '0.3.6+9'), greaterThan(0));
    expect(compareVersions('0.3.6', '0.3.6+9'), 0);
    expect(compareVersions('0.3', '0.3.0'), 0);
    expect(compareVersions('0.3.5', '0.3.6'), lessThan(0));
    expect(compareVersions('0.4.0-beta', '0.3.6'), isNull);
    expect(compareVersions('', '0.3.6'), isNull);
  });

  test('a feed is read over https only', () async {
    final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
    addTearDown(() => server.close(force: true));
    var requests = 0;
    server.listen((request) {
      requests++;
      request.response
        ..write('{"version":"9.0.0","url":"https://mirror.example/"}')
        ..close();
    });
    await expectLater(
      fetchUpdateFeed(Uri.parse('http://127.0.0.1:${server.port}/feed')),
      throwsA(isA<FormatException>()),
    );
    expect(requests, 0);
  });

  test('Windows and Linux open only an https release page', () async {
    final opened = <Object?>[];
    final proxy = DesktopProxy(
      corePath: 'unused-core',
      domainPath: 'unused-domain',
      catalogIO: (_, _) async => throw PlatformException(code: 'storage_busy'),
      platformIO: (method, payload) async {
        opened.add([method, payload['url']]);
        return {};
      },
    );
    addTearDown(proxy.dispose);
    await expectLater(
      proxy.control.openUpdateLink('http://mirror.example/elarni/'),
      throwsA(isA<PlatformException>()),
    );
    // The page needs no catalog, so a locked one does not stop it.
    await proxy.control.openUpdateLink('https://mirror.example/elarni/');
    expect(opened, [
      ['openURL', 'https://mirror.example/elarni/'],
    ]);
  });

  test(
    'the update commands reach the core without a catalog and keep its codes',
    () async {
      final requests = <List<Object?>>[];
      final replies = [
        '{"success":true,"data":{"kind":"msi","silent":true}}',
        '{"success":false,"error":"update_signature_invalid"}',
        '{"success":false,"error":"elevation_denied"}',
        '{"success":true,"data":{"exit":true}}',
      ];
      final proxy = DesktopProxy(
        corePath: 'unused-core',
        domainPath: 'unused-domain',
        startProcess: (_) async => _OneShotCore(
          requests,
          replies.isEmpty
              ? '{"success":false,"error":"unexpected"}'
              : replies.removeAt(0),
        ),
        catalogIO: (_, _) async =>
            throw PlatformException(code: 'storage_busy'),
        platformIO: (_, _) async => {},
      );
      addTearDown(proxy.dispose);
      expect(await proxy.control.updateStatus(), {
        'kind': 'msi',
        'silent': true,
      });
      const asset = (name: 'Elarni.msi', url: 'https://mirror.example/a.msi');
      await expectLater(
        proxy.control.downloadUpdate([asset]),
        throwsA(
          isA<PlatformException>().having(
            (error) => error.code,
            'code',
            'update_signature_invalid',
          ),
        ),
      );
      await expectLater(
        proxy.control.installUpdate(relaunch: true),
        throwsA(
          isA<PlatformException>().having(
            (error) => error.code,
            'code',
            'elevation_denied',
          ),
        ),
      );
      expect(await proxy.control.installUpdate(relaunch: false), {
        'exit': true,
      });
      await expectLater(
        proxy.request('downloadUpdate', {'assets': 'https://mirror.example/'}),
        throwsA(isA<PlatformException>()),
      );
      expect(requests, [
        ['updateStatus', {}],
        [
          'downloadUpdate',
          {
            'assets': [
              {'name': 'Elarni.msi', 'url': 'https://mirror.example/a.msi'},
            ],
          },
        ],
        [
          'installUpdate',
          {'relaunch': true},
        ],
        [
          'installUpdate',
          {'relaunch': false},
        ],
      ]);
    },
  );

  test(
    'a Linux restart starts the AppImage it runs from, otherwise its executable',
    () async {
      final folder = await Directory.systemTemp.createTemp('elarni-relaunch');
      addTearDown(() => folder.delete(recursive: true));
      final image = File('${folder.path}/Elarni.AppImage')
        ..writeAsStringSync('');
      const mount = '/tmp/.mount_ElarniX', executable = '$mount/elarni';
      expect(
        relaunchTarget({'APPIMAGE': image.path, 'APPDIR': mount}, executable),
        image.path,
      );
      expect(
        relaunchTarget({
          'APPIMAGE': image.path,
          'APPDIR': '$mount/',
        }, executable),
        image.path,
      );
      expect(relaunchTarget({}, '/opt/elarni/elarni'), '/opt/elarni/elarni');
      for (final environment in [
        // The image is gone, or this build does not run from it.
        {'APPIMAGE': '${folder.path}/missing', 'APPDIR': mount},
        {'APPIMAGE': image.path, 'APPDIR': '/tmp/.mount_Other'},
        {'APPIMAGE': image.path, 'APPDIR': '/tmp/.mount_Elarni'},
        {'APPIMAGE': image.path},
        {'APPIMAGE': 'Elarni.AppImage', 'APPDIR': mount},
      ])
        expect(
          relaunchTarget(environment, executable),
          executable,
          reason: '$environment',
        );
    },
  );

  group('the update notice', () {
    late List<String> stored;
    late List<Uri> fetched;
    late String feed;
    late FakeTunnelControl control;

    setUp(() {
      stored = ['{}'];
      fetched = [];
      feed = '{"version":"0.4.0","url":"https://mirror.example/elarni/"}';
      messenger.setMockMethodCallHandler(channel, (call) async {
        if (call.method == 'getPreferences') return stored.last;
        if (call.method == 'setPreferences')
          stored.add((call.arguments as Map)['json'] as String);
        return null;
      });
      updateFeedFetch = (url) async {
        fetched.add(url);
        return feed;
      };
      control = FakeTunnelControl()
        ..systemInfoValue = {'appVersion': '0.3.6+9'};
    });
    tearDown(() {
      messenger.setMockMethodCallHandler(channel, null);
      updateFeedFetch = fetchUpdateFeed;
    });

    testWidgets('stays off and silent until a feed is set', (tester) async {
      await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
      await tester.pumpAndSettle();
      expect(fetched, isEmpty);
      expect(find.textContaining('is available'), findsNothing);
      await tester.pumpWidget(const SizedBox());
    });

    testWidgets('opens the release page, and Not now hides that version', (
      tester,
    ) async {
      stored = [
        jsonEncode({'updateFeedURL': 'https://mirror.example/feed.json'}),
      ];
      await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
      await tester.pumpAndSettle();
      expect(fetched, [Uri.parse('https://mirror.example/feed.json')]);
      const notice = 'Elarni 0.4.0 is available';
      expect(find.text(notice), findsOneWidget);
      expect(find.textContaining('You have 0.3.6+9'), findsOneWidget);
      await tester.tap(find.text('Download'));
      await tester.pumpAndSettle();
      expect(control.openedUpdateLinks, ['https://mirror.example/elarni/']);
      expect(find.text(notice), findsOneWidget);
      // The open local proxy's warning has its own Not now.
      final banner = find
          .ancestor(of: find.text(notice), matching: find.byType(DecoratedBox))
          .first;
      await tester.tap(
        find.descendant(of: banner, matching: find.text('Not now')),
      );
      await tester.pumpAndSettle();
      expect(find.text(notice), findsNothing);
      expect(
        find.text('Your local proxy accepts apps without a login'),
        findsOneWidget,
      );
      expect(jsonDecode(stored.last)['dismissedUpdate'], '0.4.0');
      // A restart keeps that version hidden; a newer one shows again.
      await tester.pumpWidget(const SizedBox());
      await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
      await tester.pumpAndSettle();
      expect(find.text(notice), findsNothing);
      feed = '{"version":"0.4.1","url":"https://mirror.example/elarni/"}';
      await tester.pumpWidget(const SizedBox());
      await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
      await tester.pumpAndSettle();
      expect(find.text('Elarni 0.4.1 is available'), findsOneWidget);
      await tester.pumpWidget(const SizedBox());
    });

    testWidgets('shows nothing for the running version or an unreadable feed', (
      tester,
    ) async {
      stored = [
        jsonEncode({'updateFeedURL': 'https://mirror.example/feed.json'}),
      ];
      feed = '{"version":"0.3.6","url":"https://mirror.example/elarni/"}';
      await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
      await tester.pumpAndSettle();
      expect(fetched, hasLength(1));
      expect(find.textContaining('is available'), findsNothing);
      await tester.pumpWidget(const SizedBox());
      updateFeedFetch = (url) async => throw const SocketException('blocked');
      await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
      await tester.pumpAndSettle();
      expect(find.textContaining('is available'), findsNothing);
      await tester.pumpWidget(const SizedBox());
    });

    testWidgets('the feed is set in Settings, https only, and checked at once', (
      tester,
    ) async {
      await tester.binding.setSurfaceSize(const Size(1200, 2800));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('nav-settings')));
      await tester.pumpAndSettle();
      await selectCategory(tester, SettingsCategory.general);
      await reveal(tester, find.text('Update feed'));
      expect(find.text('Off'), findsOneWidget);
      await tester.tap(find.widgetWithText(OutlinedButton, 'Change'));
      await tester.pumpAndSettle();
      final field = find.byKey(const Key('update-feed-url'));
      await tester.enterText(field, 'http://mirror.example/feed.json');
      await tester.tap(find.widgetWithText(FilledButton, 'Save'));
      await tester.pumpAndSettle();
      expect(
        find.text(
          'Enter an https:// address, or leave it empty to turn the check off.',
        ),
        findsOneWidget,
      );
      expect(stored, ['{}']);
      await tester.enterText(field, ' https://mirror.example/feed.json ');
      await tester.tap(find.widgetWithText(FilledButton, 'Save'));
      await tester.pumpAndSettle();
      expect(
        jsonDecode(stored.last)['updateFeedURL'],
        'https://mirror.example/feed.json',
      );
      expect(fetched, [Uri.parse('https://mirror.example/feed.json')]);
      expect(find.text('https://mirror.example/feed.json'), findsOneWidget);
      expect(find.text('Elarni 0.4.0 is available'), findsOneWidget);
      // Clearing the address turns the check and its notice off.
      await tester.tap(find.widgetWithText(OutlinedButton, 'Change'));
      await tester.pumpAndSettle();
      await tester.enterText(field, '');
      await tester.tap(find.widgetWithText(FilledButton, 'Save'));
      await tester.pumpAndSettle();
      expect(jsonDecode(stored.last)['updateFeedURL'], '');
      expect(find.text('Elarni 0.4.0 is available'), findsNothing);
      await tester.pumpWidget(const SizedBox());
    });
  });

  group('installing an update', () {
    const feedURL = 'https://mirror.example/feed.json';
    const page = 'https://mirror.example/elarni/0.7.0';
    const assets = [
      (
        name: 'Elarni-0.7.0-x64.msi',
        url: 'https://mirror.example/elarni/Elarni-0.7.0-x64.msi',
      ),
      (name: 'SHA256SUMS', url: 'https://mirror.example/elarni/SHA256SUMS'),
    ];
    final release = jsonEncode({
      'tag_name': 'v0.7.0',
      'html_url': page,
      'assets': [
        for (final asset in assets)
          {'name': asset.name, 'browser_download_url': asset.url},
      ],
    });
    const downloaded = {
      'version': '0.7.0',
      'name': 'Elarni-0.7.0-x64.msi',
      'silent': true,
    };
    late List<String> stored;
    late FakeTunnelControl control;
    late List<bool> quits;
    Map<String, Object?> saved() =>
        Map<String, Object?>.from(jsonDecode(stored.last) as Map);
    Future<void> quit({required bool restart}) async => quits.add(restart);

    setUp(() {
      stored = [
        jsonEncode({'updateFeedURL': feedURL}),
      ];
      quits = [];
      messenger.setMockMethodCallHandler(channel, (call) async {
        if (call.method == 'getPreferences') return stored.last;
        if (call.method == 'setPreferences')
          stored.add((call.arguments as Map)['json'] as String);
        return null;
      });
      updateFeedFetch = (url) async => release;
      control = FakeTunnelControl()
        ..systemInfoValue = {'appVersion': '0.6.1'}
        ..downloadUpdateValue = downloaded;
    });
    tearDown(() {
      messenger.setMockMethodCallHandler(channel, null);
      updateFeedFetch = fetchUpdateFeed;
    });

    // The desktop integration alone, as the app starts it on Windows and Linux.
    Future<DesktopIntegration> start() async {
      final desktop = DesktopIntegration()
        ..updateControl = control
        ..onQuit = quit;
      addTearDown(desktop.dispose);
      await desktop.load();
      desktop.appVersion = '0.6.1';
      await pumpEventQueue();
      return desktop;
    }

    // An install shows a moving progress bar, so these pump instead of settling.
    Future<void> settle(WidgetTester tester) async {
      for (var i = 0; i < 10; i++)
        await tester.pump(const Duration(milliseconds: 50));
    }

    test(
      'a newer release downloads by itself and waits for the next start',
      () async {
        control.updateStatusValue = {'kind': 'msi', 'silent': true};
        final desktop = await start();
        expect(control.updateStatusCalls, 1);
        expect(control.downloadedAssets, [assets]);
        expect(desktop.updateStage, UpdateStage.ready);
        expect(desktop.updateVersion, '0.7.0');
        expect(desktop.updateSupport, (kind: 'msi', silent: true));
        // A new check neither asks the core again nor downloads twice.
        desktop.appVersion = '0.6.1+1';
        await pumpEventQueue();
        expect(control.updateStatusCalls, 1);
        expect(control.downloadedAssets, hasLength(1));
        expect(control.installRelaunches, isEmpty);
        expect(quits, isEmpty);
      },
    );

    test(
      'with automatic installs off, Install downloads and installs',
      () async {
        stored = [
          jsonEncode({'updateFeedURL': feedURL, 'autoUpdate': false}),
        ];
        control.updateStatusValue = {'kind': 'msi', 'silent': false};
        Object? reconnect;
        control.onInstallUpdate = () async =>
            reconnect = saved()['reconnectAfterUpdate'];
        final desktop = await start();
        expect(desktop.autoUpdate, isFalse);
        expect(control.downloadedAssets, isEmpty);
        expect(desktop.updateStage, UpdateStage.none);
        expect(desktop.updateInstallable, isTrue);
        await desktop.installUpdate(reconnect: true);
        expect(control.downloadedAssets, [assets]);
        expect(control.installRelaunches, [true]);
        // Saved before the installer ran, for the build it starts.
        expect(reconnect, isTrue);
        expect(quits, [false]);
        expect(desktop.updateStage, UpdateStage.installing);
      },
    );

    test(
      'a silent package waiting at start installs once, saved before it runs',
      () async {
        control.updateStatusValue = {
          'kind': 'msi',
          'silent': true,
          'ready': '0.7.0',
        };
        Object? attempted;
        control.onInstallUpdate = () async =>
            attempted = saved()['updateAttempted'];
        await start();
        expect(control.installRelaunches, [true]);
        expect(attempted, '0.7.0');
        expect(quits, [false]);
        expect(control.downloadedAssets, isEmpty);
      },
    );

    test(
      'a package that needs approval, or a tried one, waits for Install',
      () async {
        for (final (status, attempted) in [
          ({'kind': 'msi', 'silent': false, 'ready': '0.7.0'}, ''),
          ({'kind': 'deb', 'silent': false, 'ready': '0.7.0'}, ''),
          ({'kind': 'msi', 'silent': true, 'ready': '0.7.0'}, '0.7.0'),
        ]) {
          stored = [
            jsonEncode({
              'updateFeedURL': feedURL,
              'updateAttempted': attempted,
            }),
          ];
          control = FakeTunnelControl()..updateStatusValue = status;
          final desktop = await start();
          expect(control.installRelaunches, isEmpty, reason: '$status');
          expect(desktop.updateStage, UpdateStage.ready, reason: '$status');
          expect(control.downloadedAssets, isEmpty, reason: '$status');
        }
      },
    );

    testWidgets('Home offers Install for a package that was tried at start', (
      tester,
    ) async {
      stored = [
        jsonEncode({'updateFeedURL': feedURL, 'updateAttempted': '0.7.0'}),
      ];
      control.updateStatusValue = {
        'kind': 'msi',
        'silent': true,
        'ready': '0.7.0',
      };
      await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
      await tester.pumpAndSettle();
      expect(control.installRelaunches, isEmpty);
      expect(find.text('Elarni 0.7.0 is ready to install'), findsOneWidget);
      expect(find.text('Install'), findsOneWidget);
      expect(find.text('Download'), findsNothing);
      await tester.pumpWidget(const SizedBox());
    });

    test(
      'an AppImage swaps by itself and runs the new build after a restart',
      () async {
        control
          ..updateStatusValue = {'kind': 'appimage', 'silent': true}
          ..installUpdateValue = {'restart': true};
        final desktop = await start();
        expect(control.downloadedAssets, [assets]);
        expect(control.installRelaunches, [true]);
        expect(desktop.updateStage, UpdateStage.installed);
        expect(quits, isEmpty);
        await desktop.restartForUpdate(reconnect: true);
        expect(saved()['reconnectAfterUpdate'], isTrue);
        expect(quits, [true]);
      },
    );

    testWidgets('an installed AppImage offers Restart on Home', (tester) async {
      control
        ..updateStatusValue = {'kind': 'appimage', 'silent': true}
        ..installUpdateValue = {'restart': true};
      await tester.pumpWidget(
        ElarniApp(tunnelControl: control, linux: true, onQuit: quit),
      );
      await tester.pumpAndSettle();
      expect(find.text('Elarni 0.7.0 is installed'), findsOneWidget);
      expect(find.text('Restart Elarni to use it.'), findsOneWidget);
      expect(quits, isEmpty);
      await tester.tap(find.text('Restart'));
      await tester.pumpAndSettle();
      expect(quits, [true]);
      expect(saved()['reconnectAfterUpdate'], isNot(isTrue));
      await tester.pumpWidget(const SizedBox());
    });

    testWidgets('a refused download leaves the release page link', (
      tester,
    ) async {
      control
        ..updateStatusValue = {'kind': 'msi', 'silent': true}
        ..downloadUpdateError = PlatformException(code: 'update_unsigned');
      await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
      await tester.pumpAndSettle();
      expect(control.downloadedAssets, hasLength(1));
      expect(find.text('Elarni 0.7.0 is available'), findsOneWidget);
      expect(find.textContaining('You have 0.6.1'), findsOneWidget);
      expect(find.text('Install'), findsNothing);
      await tester.tap(find.text('Download'));
      await tester.pumpAndSettle();
      expect(control.openedUpdateLinks, [page]);
      await tester.pumpWidget(const SizedBox());
    });

    test('a refused download is not tried again in this run', () async {
      control
        ..updateStatusValue = {'kind': 'msi', 'silent': true}
        ..downloadUpdateError = PlatformException(code: 'update_unsigned');
      final desktop = await start();
      expect(desktop.updateStage, UpdateStage.none);
      expect(desktop.updateInstallable, isFalse);
      // Saving the feed checks it again; the refused version stays refused.
      await desktop.update(updateFeedURL: feedURL);
      await pumpEventQueue();
      expect(desktop.availableUpdate?.version, '0.7.0');
      expect(control.downloadedAssets, hasLength(1));
      await desktop.installUpdate(reconnect: false);
      expect(control.downloadedAssets, hasLength(1));
      expect(control.installRelaunches, isEmpty);
    });

    test(
      'a download the network cut waits an hour; Install tries it now',
      () async {
        control
          ..updateStatusValue = {'kind': 'msi', 'silent': true}
          ..downloadUpdateError = PlatformException(code: 'cancelled');
        final desktop = await start();
        expect(desktop.updateStage, UpdateStage.none);
        expect(desktop.updateInstallable, isTrue);
        expect(control.downloadedAssets, hasLength(1));
        // The periodic check does not download again within the hour.
        await desktop.checkForUpdate();
        await pumpEventQueue();
        expect(control.downloadedAssets, hasLength(1));
        control.downloadUpdateError = null;
        await desktop.installUpdate(reconnect: false);
        expect(control.downloadedAssets, hasLength(2));
        expect(control.installRelaunches, [true]);
        expect(quits, [false]);
      },
    );

    test(
      'a cancelled approval waits for Install again; a failed install stops',
      () async {
        control
          ..updateStatusValue = {'kind': 'msi', 'silent': false}
          ..installUpdateError = PlatformException(code: 'elevation_denied');
        final desktop = await start();
        expect(desktop.updateStage, UpdateStage.ready);
        await desktop.installUpdate(reconnect: true);
        expect(desktop.updateStage, UpdateStage.ready);
        expect(desktop.reconnectAfterUpdate, isFalse);
        expect(saved()['reconnectAfterUpdate'], isFalse);
        control.installUpdateError = PlatformException(code: 'update_failed');
        await desktop.installUpdate(reconnect: true);
        expect(control.installRelaunches, [true, true]);
        expect(desktop.updateStage, UpdateStage.failed);
        expect(desktop.updateInstallable, isFalse);
        expect(saved()['reconnectAfterUpdate'], isFalse);
        expect(quits, isEmpty);
      },
    );

    testWidgets('a failed install points to the release page', (tester) async {
      control.updateStatusValue = {
        'kind': 'msi',
        'silent': true,
        'failed': '0.7.0',
      };
      await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
      await tester.pumpAndSettle();
      expect(control.downloadedAssets, isEmpty);
      expect(find.text('Elarni 0.7.0 is available'), findsOneWidget);
      expect(
        find.text(
          'Elarni 0.7.0 could not be installed. Download it from the release page.',
        ),
        findsOneWidget,
      );
      expect(find.text('Install'), findsNothing);
      await tester.tap(find.text('Download'));
      await tester.pumpAndSettle();
      expect(control.openedUpdateLinks, [page]);
      await tester.pumpWidget(const SizedBox());
    });

    test('a failed install holds back only its own version', () async {
      control.updateStatusValue = {
        'kind': 'msi',
        'silent': true,
        'failed': '0.6.9',
      };
      final desktop = await start();
      expect(control.downloadedAssets, [assets]);
      expect(desktop.updateStage, UpdateStage.ready);
      expect(desktop.updateVersion, '0.7.0');
    });

    test('the app connects once after an update that asked it to', () async {
      stored = [
        jsonEncode({'reconnectAfterUpdate': true}),
      ];
      final actions = <String>[];
      final desktop = DesktopIntegration()..onAction = actions.add;
      addTearDown(desktop.dispose);
      await desktop.load();
      await pumpEventQueue();
      expect(actions, ['connect']);
      expect(saved()['reconnectAfterUpdate'], isFalse);
      expect(desktop.reconnectAfterUpdate, isFalse);
      desktop.onAction = actions.add;
      expect(actions, ['connect']);
      // The page may take the action only after the preferences loaded.
      stored = [
        jsonEncode({'reconnectAfterUpdate': true}),
      ];
      final later = DesktopIntegration();
      addTearDown(later.dispose);
      await later.load();
      await pumpEventQueue();
      final laterActions = <String>[];
      later.onAction = laterActions.add;
      later.onAction = laterActions.add;
      expect(laterActions, ['connect']);
      expect(saved()['reconnectAfterUpdate'], isFalse);
      // Without the request nothing connects.
      final plain = DesktopIntegration()..onAction = laterActions.add;
      addTearDown(plain.dispose);
      await plain.load();
      await pumpEventQueue();
      expect(laterActions, ['connect']);
    });

    testWidgets(
      'the connect after an update waits for the tunnel state to load',
      (tester) async {
        stored = [
          jsonEncode({'reconnectAfterUpdate': true}),
        ];
        // The preferences load before the core answers, as at a real start.
        final answer = Completer<void>();
        control.onState = () async {
          await answer.future;
          return control.phase;
        };
        await tester.pumpWidget(
          ElarniApp(tunnelControl: control, windows: true),
        );
        await tester.pump(const Duration(milliseconds: 100));
        expect(saved()['reconnectAfterUpdate'], isFalse);
        expect(control.connectCalls, 0);
        answer.complete();
        await tester.pumpAndSettle();
        expect(control.connectCalls, 1);
        await tester.pumpWidget(const SizedBox());
      },
    );

    testWidgets('macOS keeps the release page link and asks the core nothing', (
      tester,
    ) async {
      control.updateStatusValue = {
        'kind': 'msi',
        'silent': true,
        'ready': '0.7.0',
      };
      await tester.pumpWidget(ElarniApp(tunnelControl: control));
      await tester.pumpAndSettle();
      expect(control.updateStatusCalls, 0);
      expect(control.downloadedAssets, isEmpty);
      expect(control.installRelaunches, isEmpty);
      expect(find.text('Elarni 0.7.0 is available'), findsOneWidget);
      expect(find.text('Download'), findsOneWidget);
      expect(find.text('Install'), findsNothing);
      await tester.pumpWidget(const SizedBox());
    });

    for (final connected in [false, true])
      testWidgets(
        'Install on Home asks to reconnect only while connected ($connected)',
        (tester) async {
          control
            ..phase = connected ? 'connected' : 'disconnected'
            ..updateStatusValue = {'kind': 'msi', 'silent': false};
          Object? reconnect;
          control.onInstallUpdate = () async =>
              reconnect = saved()['reconnectAfterUpdate'];
          await tester.pumpWidget(
            ElarniApp(tunnelControl: control, windows: true, onQuit: quit),
          );
          await tester.pumpAndSettle();
          expect(find.text('Elarni 0.7.0 is ready to install'), findsOneWidget);
          expect(
            find.textContaining('The VPN reconnects after the update.'),
            connected ? findsOneWidget : findsNothing,
          );
          expect(control.installRelaunches, isEmpty);
          await tester.tap(find.text('Install'));
          await settle(tester);
          expect(control.installRelaunches, [true]);
          expect(reconnect == true, connected);
          expect(quits, [false]);
          expect(find.text('Installing Elarni 0.7.0…'), findsOneWidget);
          expect(find.byType(LinearProgressIndicator), findsOneWidget);
          expect(find.text('Install'), findsNothing);
          await tester.pumpWidget(const SizedBox());
        },
      );

    testWidgets(
      'the automatic install switch shows only where Elarni replaces itself',
      (tester) async {
        await tester.binding.setSurfaceSize(const Size(1200, 2800));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        Future<void> openUpdates() async {
          await tester.tap(find.byKey(const Key('nav-settings')));
          await tester.pumpAndSettle();
          await selectCategory(tester, SettingsCategory.general);
          await reveal(tester, find.text('Update feed'));
        }

        const label = 'Install updates automatically';
        await tester.pumpWidget(
          ElarniApp(tunnelControl: control, windows: true),
        );
        await tester.pumpAndSettle();
        await openUpdates();
        expect(find.text(label), findsNothing);
        await tester.pumpWidget(const SizedBox());

        stored = [
          jsonEncode({'updateFeedURL': feedURL, 'autoUpdate': false}),
        ];
        control.updateStatusValue = {'kind': 'msi', 'silent': true};
        await tester.pumpWidget(
          ElarniApp(tunnelControl: control, windows: true),
        );
        await tester.pumpAndSettle();
        await openUpdates();
        final checkbox = find.descendant(
          of: find.widgetWithText(ElarniCheckboxTile, label),
          matching: find.byType(Checkbox),
        );
        expect(tester.widget<Checkbox>(checkbox).value, isFalse);
        expect(control.downloadedAssets, isEmpty);
        // Turning it on looks for the release at once.
        await tester.tap(find.text(label));
        await tester.pumpAndSettle();
        expect(saved()['autoUpdate'], isTrue);
        expect(control.downloadedAssets, [assets]);
        expect(tester.widget<Checkbox>(checkbox).value, isTrue);
        await tester.tap(find.text(label));
        await tester.pumpAndSettle();
        expect(saved()['autoUpdate'], isFalse);
        await tester.pumpWidget(const SizedBox());
      },
    );
  });
}

// A core that answers one request with [reply] and exits.
final class _OneShotCore implements Process {
  _OneShotCore(this.requests, this.reply);
  final List<List<Object?>> requests;
  final String reply;
  final _output = StreamController<List<int>>();
  final _exit = Completer<int>();

  @override
  late final IOSink stdin = _Requests((line) {
    final request = jsonDecode(line) as Map;
    requests.add([request['method'], request['payload']]);
    _output.add(utf8.encode('$reply\n'));
    kill();
  });
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
      unawaited(_output.close());
    }
    return true;
  }
}

final class _Requests implements IOSink {
  _Requests(this._line);
  final void Function(String) _line;
  @override
  void writeln([Object? object = '']) => _line('$object');
  @override
  Future<void> flush() async {}
  @override
  Future<void> close() async {}
  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}
