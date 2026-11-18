import 'dart:async';
import 'package:flutter/gestures.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:elarni/elarni_app.dart';
import 'package:elarni/elarni_widgets.dart';
import 'package:elarni/tunnel_channel.dart';
import 'elarni_app_test.dart' show openTroubleshoot;
import 'support/fake_tunnel_control.dart';

SubscriptionSnapshot fixture({
  bool favorite = false,
  String revision = 'r1',
  bool local = false,
}) => SubscriptionSnapshot(
  selectedNodeId: 'a',
  subscriptions: [
    SubscriptionGroup(
      id: 'source',
      name: 'Personal subscription',
      status: 'ready',
      isLocal: local,
      hiddenNodes: local ? const [] : const [(id: 'c', name: 'Old server')],
      nodes: [
        SubscriptionNode(
          id: 'a',
          name: 'Current server',
          protocol: 'VLESS',
          transport: 'tcp',
          security: 'tls',
          selected: true,
          favorite: favorite,
          configurationRevision: revision,
          lastSuccessAt: 1700000000,
          lastSuccessCurrent: revision == 'r1',
        ),
        const SubscriptionNode(
          id: 'b',
          selected: false,
          name: 'Replacement server',
          protocol: 'VLESS',
          transport: 'tcp',
          security: 'tls',
          lastSuccessAt: 1700000100,
          lastSuccessCurrent: false,
        ),
      ],
    ),
  ],
);

class ConvenienceControl extends FakeTunnelControl {
  final favorites = <String>[];
  @override
  Future<SubscriptionSnapshot> setNodeFavorite(
    String nodeId,
    bool favorite,
  ) async {
    favorites.add('$nodeId:$favorite');
    return snapshot = fixture(favorite: favorite);
  }
}

void main() {
  for (final platform in [TargetPlatform.macOS, TargetPlatform.windows]) {
    testWidgets(
      'active HTTP health uses measurement time and revision on ${platform.name}',
      (tester) async {
        final control = ConvenienceControl()
          ..snapshot = fixture()
          ..phase = 'connected';
        var revision = 'r1';
        var age = 91;
        control.onPing = (id) async => PingResult(
          nodeId: id,
          status: 'success',
          latencyMs: 12,
          timestamp: DateTime.now().millisecondsSinceEpoch / 1000 - age,
          configurationRevision: revision,
          protocol: 'activeProxy',
        );
        await tester.binding.setSurfaceSize(const Size(1280, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        await tester.pumpWidget(
          ElarniApp(
            tunnelControl: control,
            windows: platform == TargetPlatform.windows,
          ),
        );
        await tester.pumpAndSettle();
        await openTroubleshoot(tester);
        await tester.tap(find.text('Check connection'));
        await tester.pumpAndSettle();
        expect(
          find.descendant(
            of: find.byType(AlertDialog),
            matching: find.textContaining('Stale HTTP check'),
          ),
          findsOneWidget,
        );
        expect(find.textContaining('Connection check passed'), findsNothing);
        revision = 'replaced';
        age = 0;
        await tester.tap(find.text('Check connection'));
        await tester.pumpAndSettle();
        expect(find.textContaining('Connection check passed'), findsNothing);
        expect(find.text('Checking connection…'), findsNothing);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox());
      },
      variant: TargetPlatformVariant.only(platform),
    );

    testWidgets(
      'favorites, By country and the last connection on ${platform.name}',
      (tester) async {
        final control = ConvenienceControl()..snapshot = fixture();
        await tester.binding.setSurfaceSize(const Size(1280, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        await tester.pumpWidget(
          ElarniApp(
            tunnelControl: control,
            windows: platform == TargetPlatform.windows,
          ),
        );
        await tester.pumpAndSettle();
        await tester.tap(find.byKey(const ValueKey('favorite:a')));
        await tester.pumpAndSettle();
        expect(control.favorites, ['a:true']);
        expect(control.selectedIDs, isEmpty);
        await tester.tap(find.widgetWithText(ChoiceChip, 'Favorites'));
        await tester.pumpAndSettle();
        expect(find.byKey(const ValueKey('node:a')), findsOneWidget);
        expect(find.byKey(const ValueKey('node:b')), findsNothing);
        // The third list needs no desktop host: a phone has it too.
        expect(find.widgetWithText(ChoiceChip, 'Recent'), findsNothing);
        await tester.tap(find.widgetWithText(ChoiceChip, 'By country'));
        await tester.pumpAndSettle();
        expect(find.byKey(const ValueKey('node:a')), findsOneWidget);
        expect(find.byKey(const ValueKey('node:b')), findsOneWidget);
        expect(
          find.byKey(const ValueKey('server-subscription:source')),
          findsNothing,
        );
        // The row keeps only the server; its last connection is in the tooltip.
        expect(find.textContaining('configuration changed'), findsNothing);
        expect(
          find.byWidgetPredicate(
            (widget) =>
                widget is Tooltip &&
                (widget.message ?? '').contains('configuration changed'),
          ),
          findsOneWidget,
        );
        expect(control.selectedIDs, isEmpty);
        expect(control.connectCalls, 0);
        expect(control.disconnectCalls, 0);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox());
      },
      variant: TargetPlatformVariant.only(platform),
    );

    testWidgets(
      'a server menu renames any server and deletes only others on ${platform.name}',
      (tester) async {
        final control = ConvenienceControl()..snapshot = fixture(local: true);
        await tester.binding.setSurfaceSize(const Size(1280, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        await tester.pumpWidget(
          ElarniApp(
            tunnelControl: control,
            windows: platform == TargetPlatform.windows,
          ),
        );
        await tester.pumpAndSettle();
        await tester.tap(
          find.byKey(const ValueKey('node:a')),
          buttons: kSecondaryButton,
        );
        await tester.pumpAndSettle();
        expect(find.text('Delete server'), findsNothing);
        await tester.tap(find.text('Rename server'));
        await tester.pumpAndSettle();
        await tester.enterText(find.byType(TextField).last, 'Home');
        await tester.tap(find.text('Save'));
        await tester.pumpAndSettle();
        expect(control.renamedNodes, [('a', 'Home')]);
        await tester.ensureVisible(find.byKey(const ValueKey('node:b')));
        await tester.pumpAndSettle();
        // The name: under the test font the middle of a split-layout row is its ping button.
        await tester.longPress(
          find.descendant(
            of: find.byKey(const ValueKey('node:b')),
            matching: find.text('Replacement server'),
          ),
        );
        await tester.pumpAndSettle();
        await tester.tap(find.text('Delete server'));
        await tester.pumpAndSettle();
        await tester.tap(find.widgetWithText(FilledButton, 'Delete'));
        await tester.pumpAndSettle();
        expect(control.deletedNodes, ['b']);
        expect(control.selectedIDs, isEmpty);
        // The keyboard reaches the same menu from a focused row.
        Focus.of(
          tester.element(
            find
                .descendant(
                  of: find.byKey(const ValueKey('node:b')),
                  matching: find.byType(Text),
                )
                .first,
          ),
        ).requestFocus();
        await tester.pump();
        await tester.sendKeyEvent(LogicalKeyboardKey.contextMenu);
        await tester.pumpAndSettle();
        expect(find.text('Delete server'), findsOneWidget);
        await tester.sendKeyEvent(LogicalKeyboardKey.escape);
        await tester.pumpAndSettle();
        await tester.sendKeyDownEvent(LogicalKeyboardKey.shiftLeft);
        await tester.sendKeyEvent(LogicalKeyboardKey.f10);
        await tester.sendKeyUpEvent(LogicalKeyboardKey.shiftLeft);
        await tester.pumpAndSettle();
        expect(find.text('Rename server'), findsOneWidget);
        await tester.sendKeyEvent(LogicalKeyboardKey.escape);
        await tester.pumpAndSettle();
        expect(control.selectedIDs, isEmpty);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox());
      },
      variant: TargetPlatformVariant.only(platform),
    );

    testWidgets(
      'a visible button opens the server menu on ${platform.name}',
      (tester) async {
        final control = ConvenienceControl()..snapshot = fixture(local: true);
        await tester.binding.setSurfaceSize(const Size(1280, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        await tester.pumpWidget(
          ElarniApp(
            tunnelControl: control,
            windows: platform == TargetPlatform.windows,
          ),
        );
        await tester.pumpAndSettle();
        final menu = find.byKey(const ValueKey('server-menu:b'));
        await tester.ensureVisible(menu);
        await tester.pumpAndSettle();
        expect(
          find.descendant(
            of: find.byKey(const ValueKey('node:b')),
            matching: find.byTooltip('Server actions'),
          ),
          findsOneWidget,
        );
        await tester.tap(menu);
        await tester.pumpAndSettle();
        expect(find.text('Rename server'), findsOneWidget);
        await tester.tap(find.text('Delete server'));
        await tester.pumpAndSettle();
        await tester.tap(find.widgetWithText(FilledButton, 'Delete'));
        await tester.pumpAndSettle();
        expect(control.deletedNodes, ['b']);
        // Opening the menu does not select the row.
        expect(control.selectedIDs, isEmpty);
        // The narrowest split column keeps its width for names; the gestures remain.
        await tester.binding.setSurfaceSize(const Size(800, 600));
        await tester.pumpAndSettle();
        expect(find.byKey(const ValueKey('node:a')), findsOneWidget);
        expect(find.byKey(const ValueKey('server-menu:a')), findsNothing);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox());
      },
      variant: TargetPlatformVariant.only(platform),
    );

    testWidgets(
      'a provider server is hidden, and shown again from Hidden servers on ${platform.name}',
      (tester) async {
        final control = ConvenienceControl()..snapshot = fixture();
        await tester.binding.setSurfaceSize(const Size(1280, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        await tester.pumpWidget(
          ElarniApp(
            tunnelControl: control,
            windows: platform == TargetPlatform.windows,
          ),
        );
        await tester.pumpAndSettle();
        await tester.ensureVisible(find.byKey(const ValueKey('node:b')));
        await tester.pumpAndSettle();
        // The name: under the test font the middle of a split-layout row is its ping button.
        await tester.longPress(
          find.descendant(
            of: find.byKey(const ValueKey('node:b')),
            matching: find.text('Replacement server'),
          ),
        );
        await tester.pumpAndSettle();
        expect(find.text('Delete server'), findsNothing);
        await tester.tap(find.text('Hide server'));
        await tester.pumpAndSettle();
        expect(
          find.text(
            'Replacement server stays hidden after refreshes. Show it again from Hidden servers.',
          ),
          findsOneWidget,
        );
        await tester.tap(find.widgetWithText(FilledButton, 'Hide'));
        await tester.pumpAndSettle();
        expect(control.deletedNodes, ['b']);
        await tester.tap(find.byKey(const Key('nav-subscriptions')));
        await tester.pumpAndSettle();
        await tester.tap(find.text('Personal subscription'));
        await tester.pumpAndSettle();
        final hidden = find.byKey(const ValueKey('hidden-servers:source'));
        await tester.ensureVisible(hidden);
        expect(
          find.descendant(
            of: hidden,
            matching: find.text('Hidden servers · 1'),
          ),
          findsOneWidget,
        );
        await tester.tap(
          find.descendant(of: hidden, matching: find.text('Show again')),
        );
        await tester.pumpAndSettle();
        expect(find.text('Old server'), findsOneWidget);
        await tester.tap(find.widgetWithText(TextButton, 'Show again').last);
        await tester.pumpAndSettle();
        expect(control.shownNodes, ['c']);
        expect(control.selectedIDs, isEmpty);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox());
      },
      variant: TargetPlatformVariant.only(platform),
    );

    testWidgets(
      'old and replaced probe results never show healthy latency on ${platform.name}',
      (tester) async {
        final control = ConvenienceControl()..snapshot = fixture();
        control.onPing = (id) async => PingResult(
          nodeId: id,
          status: 'success',
          latencyMs: 12,
          timestamp: DateTime.now().millisecondsSinceEpoch / 1000 - 301,
          configurationRevision: 'r1',
          protocol: 'tcp',
        );
        await tester.binding.setSurfaceSize(const Size(1280, 900));
        addTearDown(() => tester.binding.setSurfaceSize(null));
        await tester.pumpWidget(
          ElarniApp(
            tunnelControl: control,
            windows: platform == TargetPlatform.windows,
          ),
        );
        await tester.pumpAndSettle();
        await tester.tap(find.byKey(const ValueKey('ping:a')));
        await tester.pumpAndSettle();
        expect(find.text('12 ms · stale'), findsOneWidget);
        expect(find.byType(LatencyBadge), findsNothing);
        final delayed = Completer<PingResult>();
        control.onPing = (_) => delayed.future;
        await tester.tap(find.byKey(const ValueKey('ping:a')));
        await tester.pump();
        control.snapshot = fixture(revision: 'r2');
        control.importListener?.call(null);
        await tester.pump();
        delayed.complete(
          PingResult(
            nodeId: 'a',
            status: 'success',
            latencyMs: 9,
            timestamp: DateTime.now().millisecondsSinceEpoch / 1000,
            configurationRevision: 'r1',
            protocol: 'tcp',
          ),
        );
        await tester.pumpAndSettle();
        expect(find.textContaining('9 ms'), findsNothing);
        expect(find.byType(LatencyBadge), findsNothing);
        expect(tester.takeException(), isNull);
        await tester.pumpWidget(const SizedBox());
      },
      variant: TargetPlatformVariant.only(platform),
    );
  }

  Future<void> openServerMenu(WidgetTester tester, String id) async {
    final menu = find.byKey(ValueKey('server-menu:$id'));
    await tester.ensureVisible(menu);
    await tester.pumpAndSettle();
    await tester.tap(menu);
    await tester.pumpAndSettle();
  }

  testWidgets('the server menu shares that one server', (tester) async {
    final control = ConvenienceControl()..snapshot = fixture();
    await tester.binding.setSurfaceSize(const Size(1280, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(ElarniApp(tunnelControl: control));
    await tester.pumpAndSettle();
    await openServerMenu(tester, 'b');
    await tester.tap(find.text('Share server'));
    await tester.pumpAndSettle();
    expect(control.sharedIDs, ['b']);
    expect(control.sharedSubscription, [false]);
    final dialog = find.byType(AlertDialog);
    expect(
      find.descendant(of: dialog, matching: find.text('Share server')),
      findsOneWidget,
    );
    expect(
      find.descendant(
        of: dialog,
        matching: find.text(
          'This configuration is too large for a QR code. Use Copy.',
        ),
      ),
      findsOneWidget,
    );
    await tester.tap(find.widgetWithText(TextButton, 'Close'));
    await tester.pumpAndSettle();
    expect(dialog, findsNothing);
    expect(control.selectedIDs, isEmpty);

    // A provider that hides its link hides its servers' links too.
    final shown = fixture().subscriptions.single;
    control.snapshot = SubscriptionSnapshot(
      selectedNodeId: 'a',
      subscriptions: [
        SubscriptionGroup(
          id: shown.id,
          name: shown.name,
          status: shown.status,
          metadata: const {'hideURL': true},
          nodes: shown.nodes,
        ),
      ],
    );
    control.importListener!(null);
    await tester.pumpAndSettle();
    await openServerMenu(tester, 'b');
    expect(find.text('Rename server'), findsOneWidget);
    expect(find.text('Share server'), findsNothing);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('the server menu sets the IP type of that one server', (
    tester,
  ) async {
    final control = ConvenienceControl()..snapshot = fixture();
    await tester.binding.setSurfaceSize(const Size(1280, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    await tester.pumpWidget(ElarniApp(tunnelControl: control));
    await tester.pumpAndSettle();
    await openServerMenu(tester, 'b');
    await tester.tap(find.text('Server IP type'));
    await tester.pumpAndSettle();
    Finder choice(String value) =>
        find.byKey(ValueKey('server-ip-choice:$value'));
    // The choices Settings offers for the selected server.
    for (final (value, label) in [
      ('default', 'Use app default'),
      ('automatic', 'Automatic'),
      ('ipv4', 'IPv4'),
      ('ipv6', 'IPv6'),
    ]) {
      expect(
        find.descendant(of: choice(value), matching: find.text(label)),
        findsOneWidget,
      );
    }
    expect(tester.widget<ListTile>(choice('default')).selected, isTrue);
    await tester.tap(choice('ipv6'));
    await tester.pumpAndSettle();
    expect(control.nodeIPPreferences, [('b', 'ipv6')]);
    // Keeping the current choice changes nothing.
    await openServerMenu(tester, 'b');
    await tester.tap(find.text('Server IP type'));
    await tester.pumpAndSettle();
    await tester.tap(choice('default'));
    await tester.pumpAndSettle();
    expect(control.nodeIPPreferences, [('b', 'ipv6')]);
    expect(control.selectedIDs, isEmpty);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets(
    'Use last successful server selects the latest unchanged one, as on iOS',
    (tester) async {
      SubscriptionSnapshot snapshot({required bool bCurrent}) {
        final group = fixture().subscriptions.single;
        return SubscriptionSnapshot(
          selectedNodeId: 'a',
          subscriptions: [
            SubscriptionGroup(
              id: group.id,
              name: group.name,
              status: group.status,
              nodes: [
                group.nodes.first,
                SubscriptionNode(
                  id: 'b',
                  selected: false,
                  name: 'Replacement server',
                  protocol: 'VLESS',
                  transport: 'tcp',
                  security: 'tls',
                  lastSuccessAt: 1700000100,
                  lastSuccessCurrent: bCurrent,
                ),
                // A Telegram proxy is not a server to connect through.
                const SubscriptionNode(
                  id: 't',
                  selected: false,
                  name: 'Telegram proxy',
                  protocol: 'MTProto',
                  transport: 'tcp',
                  security: 'none',
                  format: 'telegram',
                  lastSuccessAt: 1700000200,
                  lastSuccessCurrent: true,
                ),
              ],
            ),
          ],
        );
      }

      final control = ConvenienceControl()
        ..snapshot = snapshot(bCurrent: false);
      await tester.binding.setSurfaceSize(const Size(1280, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      await tester.pumpWidget(ElarniApp(tunnelControl: control));
      await tester.pumpAndSettle();
      final button = find.byKey(const Key('use-last-successful'));
      // b changed since it connected, so the latest is a, the selected one.
      expect(button, findsNothing);
      control.snapshot = snapshot(bCurrent: true);
      control.importListener!(null);
      await tester.pumpAndSettle();
      expect(find.text('Use last successful server'), findsOneWidget);
      expect(find.byTooltip('Replacement server'), findsOneWidget);
      await tester.tap(button);
      await tester.pumpAndSettle();
      expect(control.selectedIDs, ['b']);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox());
    },
  );
}
