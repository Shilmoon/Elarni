import 'dart:io';

import 'package:elarni/desktop_catalog.dart';
import 'package:elarni/desktop_integration.dart';
import 'package:elarni/desktop_proxy.dart';
import 'package:elarni/elarni_app.dart';
import 'package:elarni/elarni_widgets.dart';
import 'package:elarni/system_proxy.dart';
import 'package:elarni/tunnel_channel.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import 'elarni_app_test.dart' show reveal, selectCategory;
import 'support/fake_tunnel_control.dart';

// An OS setting that is either the user's own or a port Elarni pointed it at.
final class _FakeOS implements SystemProxy {
  ProxySettings current = {'user': 'pac'};

  @override
  Future<ProxySettings> read() async => {...current};

  @override
  Future<void> point(int port) async => current = {'port': port};

  @override
  Future<void> write(ProxySettings settings) async => current = {...settings};

  @override
  bool pointsAt(ProxySettings settings, int port) => settings['port'] == port;

  @override
  bool matchesPoint(ProxySettings settings, int port) =>
      settings['port'] == port;
}

// A selected server whose core never starts, so every Connect ends failed:
// the state an unexpected stop leaves too.
Future<({DesktopProxy proxy, _FakeOS os, File backup})> failing(
  TunnelSettings settings,
) async {
  final temporary = await Directory.systemTemp.createTemp('elarni-kill-');
  addTearDown(() => temporary.delete(recursive: true));
  final catalog = DesktopCatalog(
    DesktopCatalog.empty(),
    (_, _) async => {},
    (_, _) => {},
  );
  catalog.putSource({
    'id': 'source',
    'name': 'source',
    'format': 'source',
    'status': 'ready',
    'nodes': [
      {'id': 'node', 'name': 'node', 'format': 'xrayProfile', 'config': {}},
    ],
  });
  catalog.data['selectedNodeId'] = 'node';
  catalog.data['settings'] = settings.toMap();
  final os = _FakeOS();
  final backup = File('${temporary.path}/Elarni/system-proxy.json');
  final proxy = DesktopProxy(
    corePath: 'unused-core',
    domainPath: 'unused-domain',
    startProcess: (_) =>
        Future.error(const ProcessException('unused-core', [])),
    catalogIO: (method, _) async => switch (method) {
      'loadCatalog' => {
        'revision': '1',
        'catalog': {'version': 1, 'committed': copyCatalog(catalog.data)},
      },
      'staleVPNGuard' => {'stale': false},
      _ => {'revision': '2'},
    },
    platformIO: (_, _) async => {},
    systemProxy: SystemProxySession(os, backup),
  );
  addTearDown(proxy.dispose);
  await proxy.request('state');
  return (proxy: proxy, os: os, backup: backup);
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();
  const user = {'user': 'pac'};

  test(
    'the kill switch keeps the system proxy on Elarni after a failed connection',
    () async {
      const settings = TunnelSettings(
        systemProxyEnabled: true,
        blockWithoutVPN: true,
      );
      final (:proxy, :os, :backup) = await failing(settings);
      expect(os.current, user, reason: 'nothing is held before a connect');
      await expectLater(proxy.request('connect'), throwsA(anything));
      expect(await proxy.request('state'), {'state': 'failed'});
      // Apps that follow the setting reach a closed port, not the network.
      expect(os.current, {'port': settings.httpPort});
      expect(await backup.exists(), isTrue);
      // A second failure keeps it there.
      await expectLater(proxy.request('connect'), throwsA(anything));
      expect(os.current, {'port': settings.httpPort});

      // Disconnecting lifts the block.
      expect(await proxy.request('disconnect'), {'state': 'disconnected'});
      expect(os.current, user);
      expect(await backup.exists(), isFalse);
    },
  );

  test('quitting gives the proxy setting back from a held block', () async {
    final (:proxy, :os, backup: _) = await failing(
      const TunnelSettings(systemProxyEnabled: true, blockWithoutVPN: true),
    );
    await expectLater(proxy.request('connect'), throwsA(anything));
    expect(os.current, isNot(user));
    await proxy.releaseSystemProxy();
    expect(os.current, user);
  });

  // The setting is kept when the mode changes, so each mode must read it.
  for (final (name, settings) in const [
    ('the kill switch off', TunnelSettings(systemProxyEnabled: true)),
    ('Local proxy only', TunnelSettings(blockWithoutVPN: true)),
    (
      'System-wide VPN',
      TunnelSettings(
        systemProxyEnabled: true,
        xrayTunEnabled: true,
        blockWithoutVPN: true,
      ),
    ),
  ])
    test(
      'a failed connection leaves the proxy setting alone with $name',
      () async {
        final (:proxy, :os, :backup) = await failing(settings);
        await expectLater(proxy.request('connect'), throwsA(anything));
        expect(await proxy.request('state'), {'state': 'failed'});
        expect(os.current, user);
        expect(await backup.exists(), isFalse);
      },
    );

  group('window', () {
    final messenger =
        TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger;
    Future<FakeTunnelControl> open(
      WidgetTester tester,
      TunnelSettings settings, {
      String phase = 'disconnected',
    }) async {
      messenger.setMockMethodCallHandler(
        DesktopIntegration.channel,
        (call) async => call.method == 'getPreferences' ? '{}' : null,
      );
      addTearDown(
        () => messenger.setMockMethodCallHandler(
          DesktopIntegration.channel,
          null,
        ),
      );
      await tester.binding.setSurfaceSize(const Size(1200, 2800));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final control = FakeTunnelControl()
        ..phase = phase
        ..settings = settings;
      await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
      await tester.pumpAndSettle();
      return control;
    }

    Future<void> tapKillSwitch(WidgetTester tester) async {
      await tester.tap(find.byKey(const Key('nav-settings')));
      await tester.pumpAndSettle();
      await selectCategory(tester, SettingsCategory.connection);
      await reveal(tester, find.text('Kill switch'));
      await tester.tap(find.text('Kill switch'));
      await tester.pumpAndSettle();
    }

    testWidgets('the kill switch can be turned on in System proxy mode', (
      tester,
    ) async {
      final control = await open(
        tester,
        const TunnelSettings(systemProxyEnabled: true),
      );
      await tapKillSwitch(tester);
      expect(control.settings.blockWithoutVPN, isTrue);
      await tester.pumpWidget(const SizedBox());
    });

    testWidgets('Local proxy only has no kill switch to turn on', (
      tester,
    ) async {
      final control = await open(tester, const TunnelSettings());
      await tapKillSwitch(tester);
      expect(control.settings.blockWithoutVPN, isFalse);
      await tester.pumpWidget(const SizedBox());
    });

    testWidgets(
      'Home says a failed System proxy connection is blocked, and Lift block disconnects',
      (tester) async {
        const banner = 'Traffic blocked until you reconnect';
        final control = await open(
          tester,
          const TunnelSettings(systemProxyEnabled: true, blockWithoutVPN: true),
          phase: 'failed',
        );
        expect(find.text(banner), findsOneWidget);
        await tester.tap(find.text('Lift block'));
        await tester.pumpAndSettle();
        // No helper holds this block: a disconnect lifts it, without the
        // repair that asks for administrator permission.
        expect(find.text('Repair'), findsNothing);
        expect(control.repairCalls, 0);
        expect(control.disconnectCalls, 1);
        expect(find.text(banner), findsNothing);
        await tester.pumpWidget(const SizedBox());
      },
    );

    testWidgets('a failed Local proxy connection blocks nothing', (
      tester,
    ) async {
      await open(
        tester,
        const TunnelSettings(blockWithoutVPN: true),
        phase: 'failed',
      );
      expect(find.text('Traffic blocked until you reconnect'), findsNothing);
      await tester.pumpWidget(const SizedBox());
    });
  });
}
