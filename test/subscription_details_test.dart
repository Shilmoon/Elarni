import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:elarni/elarni_app.dart';
import 'package:elarni/tunnel_channel.dart';
import 'support/fake_tunnel_control.dart';

const _node = SubscriptionNode(
  id: 'a',
  name: 'NL-4',
  protocol: 'VLESS',
  transport: 'tcp',
  security: 'tls',
  selected: true,
);

SubscriptionSnapshot _subscriptions({
  Map<String, String> options = const {},
  String format = 'source',
  double? fetched,
}) => SubscriptionSnapshot(
  selectedNodeId: 'a',
  subscriptions: [
    SubscriptionGroup(
      id: 'acme',
      name: 'Acme VPN',
      status: 'ready',
      nodes: const [_node],
      fetchOptions: options,
      format: format,
      lastSuccessfulFetch: fetched,
      metadata: const {
        'deviceRegistered': true,
        'updateIntervalHours': 12,
        'hideSettings': true,
      },
    ),
  ],
);

Future<void> _openSubscription(
  WidgetTester tester,
  FakeTunnelControl control,
) async {
  await tester.pumpWidget(ElarniApp(tunnelControl: control));
  await tester.pumpAndSettle();
  await tester.tap(find.byKey(const Key('nav-subscriptions')));
  await tester.pumpAndSettle();
}

SubscriptionSnapshot _group({
  List<SubscriptionNode> nodes = const [_node],
  Map<String, Object?> metadata = const {},
}) => SubscriptionSnapshot(
  selectedNodeId: nodes.isEmpty ? null : 'a',
  subscriptions: [
    SubscriptionGroup(
      id: 'acme',
      name: 'Acme VPN',
      status: 'ready',
      nodes: nodes,
      metadata: metadata,
    ),
  ],
);

void main() {
  // Android stores the count as text, Windows, Linux and macOS as a number.
  for (final count in [3, '3']) {
    testWidgets(
      'subscription details name the servers Elarni did not add (${count.runtimeType})',
      (tester) async {
        final control = FakeTunnelControl()
          ..snapshot = _group(
            metadata: {'skippedServers': count, 'skippedKinds': 'ssh, naive'},
          );
        await _openSubscription(tester, control);
        await tester.tap(
          find.byKey(const PageStorageKey('subscription-details:acme')),
        );
        await tester.pumpAndSettle();
        expect(find.text('Servers not added'), findsOneWidget);
        expect(
          find.text(
            '3 (ssh, naive). Elarni does not support these servers; the rest were added.',
          ),
          findsOneWidget,
        );
      },
    );
  }

  testWidgets('a subscription with every server added has no such row', (
    tester,
  ) async {
    final control = FakeTunnelControl()
      ..snapshot = _group(metadata: {'skippedServers': 0, 'skippedKinds': ''});
    await _openSubscription(tester, control);
    await tester.tap(
      find.byKey(const PageStorageKey('subscription-details:acme')),
    );
    await tester.pumpAndSettle();
    expect(find.text('Servers not added'), findsNothing);
  });

  // A provider sending what Elarni cannot read is not a network problem, and
  // a subscription that never downloaded has no servers to keep.
  for (final (code, nodes, label, message) in [
    (
      'unsupported_profile',
      [_node],
      'Unsupported content',
      'Acme VPN sent nothing Elarni can read: the format or one of its '
          'settings is not supported. The last working servers were kept.',
    ),
    (
      'unsupported_profile',
      <SubscriptionNode>[],
      'Unsupported content',
      'Acme VPN sent nothing Elarni can read: the format or one of its '
          'settings is not supported.',
    ),
    (
      'unencrypted_server',
      <SubscriptionNode>[],
      'Servers without encryption',
      'Acme VPN sent only servers without TLS, REALITY or VLESS encryption. '
          'Elarni accepts such a server only on a private network.',
    ),
    (
      'hwid_limit',
      [_node],
      'Provider device limit reached',
      'Acme VPN reached this subscription’s device limit. Remove a device '
          'in your account with the provider, or contact the provider. '
          'The last working servers were kept.',
    ),
    (
      'operation_failed',
      <SubscriptionNode>[],
      'Refresh failed',
      'Refresh failed.',
    ),
    (
      'fetch_failed',
      <SubscriptionNode>[],
      'Download failed',
      'The download failed. Check this device’s network, then retry.',
    ),
  ]) {
    testWidgets('a $code refresh of ${nodes.length} servers names its cause', (
      tester,
    ) async {
      final control = FakeTunnelControl()
        ..snapshot = _group(nodes: nodes)
        ..onRefresh = (_) async =>
            throw PlatformException(code: code, message: 'secret');
      await _openSubscription(tester, control);
      await tester.tap(find.byKey(const ValueKey('refresh-subscription:acme')));
      await tester.pumpAndSettle();
      expect(find.text(message), findsOneWidget);
      expect(
        find.textContaining('kept'),
        nodes.isEmpty ? findsNothing : findsOneWidget,
      );
      expect(find.textContaining('· $label'), findsOneWidget);
      expect(find.textContaining('secret'), findsNothing);
    });
  }

  testWidgets(
    'subscription details label what the provider set and reset only that device ID',
    (tester) async {
      // The details are under the server table: room for both and the refresh button.
      await tester.binding.setSurfaceSize(const Size(900, 1400));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final copied = <String>[];
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        SystemChannels.platform,
        (call) async {
          if (call.method == 'Clipboard.setData') {
            copied.add((call.arguments as Map)['text'] as String);
          }
          return null;
        },
      );
      final control = FakeTunnelControl()
        ..snapshot = _subscriptions()
        ..routingSnapshot = const RoutingSnapshot(
          profiles: [
            RoutingProfile(
              id: 'acme-rules',
              name: 'Acme rules',
              source: 'provider',
              edited: false,
              enabled: true,
              geofileStatus: 'ready',
              json: '{}',
              providerSubscriptionId: 'acme',
            ),
          ],
          assignments: {'acme': 'acme-rules'},
        );
      await tester.pumpWidget(ElarniApp(tunnelControl: control));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('nav-subscriptions')));
      await tester.pumpAndSettle();
      await tester.tap(
        find.byKey(const PageStorageKey('subscription-details:acme')),
      );
      await tester.pumpAndSettle();

      expect(find.text('Every 12 h'), findsOneWidget);
      // The refresh interval and the routing profile both came from the provider.
      expect(find.text('Set by your provider'), findsNWidgets(2));
      expect(find.text('Device ID'), findsOneWidget);
      expect(find.text('3f9a12c8…c21e'), findsOneWidget);
      expect(find.text('Registered · set by your provider'), findsOneWidget);
      expect(
        find.text(
          'Your provider asks you to keep its settings. You can still change them.',
        ),
        findsOneWidget,
      );
      expect(
        find.text(
          'Elarni sends each provider a different device ID, made from a random key on this device. '
          "Providers can't link your IDs to each other. A renamed subscription, a changed refresh "
          'interval and your routing choice stay after a refresh.',
        ),
        findsOneWidget,
      );

      await tester.tap(find.byKey(const ValueKey('copy-device-id:acme')));
      await tester.pumpAndSettle();
      expect(copied, [control.deviceID]);

      await tester.tap(find.byKey(const ValueKey('reset-device-id:acme')));
      await tester.pumpAndSettle();
      expect(find.text('Reset device ID for Acme VPN?'), findsOneWidget);
      expect(
        find.text(
          'Acme VPN will see this device as new. If it limits devices, the new ID needs a free slot, '
          'and the old one may hold a slot until Acme VPN removes it. Your other subscriptions keep their IDs.',
        ),
        findsOneWidget,
      );
      await tester.tap(find.widgetWithText(TextButton, 'Cancel'));
      await tester.pumpAndSettle();
      expect(control.resetDeviceIDs, isEmpty);
      await tester.tap(find.byKey(const ValueKey('reset-device-id:acme')));
      await tester.pumpAndSettle();
      await tester.tap(find.widgetWithText(FilledButton, 'Reset'));
      await tester.pumpAndSettle();
      expect(control.resetDeviceIDs, ['acme']);
      expect(find.text('00000000…0001'), findsOneWidget);

      // Change opens the download settings, where the user's override wins.
      await tester.tap(find.byKey(const ValueKey('change-refresh:acme')));
      await tester.pumpAndSettle();
      expect(find.text('Subscription downloads'), findsOneWidget);
      await tester.tap(find.widgetWithText(TextButton, 'Cancel').last);
      await tester.pumpAndSettle();
      control.snapshot = _subscriptions(options: const {'refreshHours': '3'});
      await tester.tap(find.byKey(const ValueKey('refresh-subscription:acme')));
      await tester.pumpAndSettle();
      expect(find.text('Every 3 h'), findsOneWidget);
      expect(find.text('Your override'), findsOneWidget);
      expect(find.text('Set by your provider'), findsOneWidget);
    },
  );

  // The ID belongs to the origin the downloads go to. A refresh can move the
  // subscription there (a new URL from the provider), and so can another format.
  testWidgets(
    'the device ID shown is the one the next download sends, and a failed lookup is asked again',
    (tester) async {
      // The details are under the server table: room for both and the refresh button.
      await tester.binding.setSurfaceSize(const Size(900, 1400));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final copied = <String>[];
      tester.binding.defaultBinaryMessenger.setMockMethodCallHandler(
        SystemChannels.platform,
        (call) async {
          if (call.method == 'Clipboard.setData') {
            copied.add((call.arguments as Map)['text'] as String);
          }
          return null;
        },
      );
      final control = FakeTunnelControl()
        ..snapshot = _subscriptions(fetched: 1000)
        ..deviceIDFails = true;
      await _openSubscription(tester, control);
      await tester.tap(
        find.byKey(const PageStorageKey('subscription-details:acme')),
      );
      await tester.pumpAndSettle();
      Future<void> refresh() async {
        await tester.tap(
          find.byKey(const ValueKey('refresh-subscription:acme')),
        );
        await tester.pumpAndSettle();
      }

      expect(find.text('3f9a12c8…c21e'), findsNothing);
      expect(
        tester
            .widget<TextButton>(
              find.byKey(const ValueKey('copy-device-id:acme')),
            )
            .onPressed,
        isNull,
      );
      control.deviceIDFails = false;
      await refresh();
      expect(find.text('3f9a12c8…c21e'), findsOneWidget);
      // A page that is drawn again does not ask again.
      final lookups = control.deviceIDLookups;
      await refresh();
      expect(control.deviceIDLookups, lookups);

      const moved =
          'aaaaaaaa00000000000000000000000000000000000000000000000000001111';
      const other =
          'bbbbbbbb00000000000000000000000000000000000000000000000000002222';
      control
        ..deviceID = moved
        ..snapshot = _subscriptions(fetched: 2000);
      await refresh();
      expect(find.text('aaaaaaaa…1111'), findsOneWidget);
      await tester.tap(find.byKey(const ValueKey('copy-device-id:acme')));
      await tester.pumpAndSettle();
      control
        ..deviceID = other
        ..snapshot = _subscriptions(fetched: 2000, format: 'json');
      await refresh();
      expect(find.text('bbbbbbbb…2222'), findsOneWidget);
      await tester.tap(find.byKey(const ValueKey('copy-device-id:acme')));
      await tester.pumpAndSettle();
      expect(copied, [moved, other]);
    },
  );
}
