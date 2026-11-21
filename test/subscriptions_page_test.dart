import 'dart:convert';

import 'package:elarni/desktop_integration.dart';
import 'package:elarni/elarni_app.dart';
import 'package:elarni/tunnel_channel.dart';
import 'package:flutter/cupertino.dart' show CupertinoIcons;
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import 'support/fake_tunnel_control.dart';

SubscriptionNode server(
  String id,
  String name,
  String protocol, {
  bool selected = false,
  bool favorite = false,
}) => SubscriptionNode(
  id: id,
  name: name,
  protocol: protocol,
  transport: 'tcp',
  security: 'tls',
  selected: selected,
  favorite: favorite,
);

SubscriptionSnapshot fixture() => SubscriptionSnapshot(
  selectedNodeId: 'de',
  subscriptions: [
    SubscriptionGroup(
      id: 'travel',
      name: 'Travel',
      status: 'ready',
      nodes: [server('se', 'SE Stockholm', 'VMess')],
    ),
    SubscriptionGroup(
      id: 'daily',
      name: 'Daily',
      status: 'ready',
      nodes: [
        server('nl', 'NL Amsterdam', 'VLESS'),
        server('de', 'DE Frankfurt', 'VLESS', selected: true),
        server('fi', 'FI Helsinki', 'Trojan', favorite: true),
        server('at', 'AT Vienna', 'Shadowsocks'),
      ],
    ),
  ],
);

class PageControl extends FakeTunnelControl {
  final favorites = <(String, bool)>[];
  @override
  Future<SubscriptionSnapshot> setNodeFavorite(
    String nodeId,
    bool favorite,
  ) async {
    favorites.add((nodeId, favorite));
    return snapshot;
  }
}

/// The desktop preferences as the app last saved them.
String stored = '{}';

Future<PageControl> openPage(
  WidgetTester tester, {
  Size size = const Size(1200, 900),
}) async {
  stored = '{}';
  final messenger = tester.binding.defaultBinaryMessenger;
  messenger.setMockMethodCallHandler(DesktopIntegration.channel, (call) async {
    if (call.method == 'getPreferences') return stored;
    if (call.method == 'setPreferences') {
      stored = (call.arguments as Map)['json'] as String;
    }
    return null;
  });
  addTearDown(
    () => messenger.setMockMethodCallHandler(DesktopIntegration.channel, null),
  );
  await tester.binding.setSurfaceSize(size);
  addTearDown(() => tester.binding.setSurfaceSize(null));
  final control = PageControl()..snapshot = fixture();
  await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
  await tester.pumpAndSettle();
  await tester.tap(find.byKey(const Key('nav-subscriptions')));
  await tester.pumpAndSettle();
  return control;
}

Finder row(String id) => find.byKey(ValueKey('node:$id'));

/// The table's servers, top to bottom.
List<String> shown(WidgetTester tester) {
  final rows = [
    for (final id in const ['nl', 'de', 'fi', 'at', 'se'])
      if (row(id).evaluate().isNotEmpty) (tester.getTopLeft(row(id)).dy, id),
  ]..sort((a, b) => a.$1.compareTo(b.$1));
  return [for (final (_, id) in rows) id];
}

Future<void> choose(WidgetTester tester, String subscription) async {
  await tester.tap(find.byKey(const Key('subscription-picker')));
  await tester.pumpAndSettle();
  await tester.tap(find.widgetWithText(MenuItemButton, subscription));
  await tester.pumpAndSettle();
}

Future<void> menu(WidgetTester tester, String id, String item) async {
  await tester.tap(find.byKey(ValueKey('subscription-actions:$id')));
  await tester.pumpAndSettle();
  await tester.tap(find.widgetWithText(PopupMenuItem<String>, item));
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('the subscriptions page shows one subscription as a table', (
    tester,
  ) async {
    await openPage(tester);
    expect(find.text('2 subscriptions · 5 servers'), findsOneWidget);
    // The subscription of the selected server opens first, not the first one.
    final picker = find.byKey(const Key('subscription-picker'));
    expect(
      find.descendant(of: picker, matching: find.text('Daily')),
      findsOneWidget,
    );
    expect(
      find.descendant(of: picker, matching: find.text('4 servers')),
      findsOneWidget,
    );
    // Favorites first, then the provider's order.
    expect(shown(tester), ['fi', 'nl', 'de', 'at']);
    expect(find.text('Server'), findsOneWidget);
    expect(find.text('Latency'), findsOneWidget);
    expect(find.text('Automatic'), findsNothing);

    await choose(tester, 'Travel');
    expect(shown(tester), ['se']);
    expect(
      find.descendant(of: picker, matching: find.text('1 server')),
      findsOneWidget,
    );
    expect(find.byKey(const ValueKey('subscription-actions:travel')), findsOne);
    expect(
      find.byKey(const ValueKey('subscription-actions:daily')),
      findsNothing,
    );
    expect(tester.takeException(), isNull);
  });

  testWidgets('a table row selects, stars and tests its server', (
    tester,
  ) async {
    final control = await openPage(tester);
    // The selected server is marked beside its name.
    expect(
      find.descendant(
        of: row('de'),
        matching: find.byKey(const ValueKey('selected-server')),
      ),
      findsOneWidget,
    );
    expect(find.byKey(const ValueKey('selected-server')), findsOneWidget);

    await tester.tap(find.text('NL Amsterdam'));
    await tester.pumpAndSettle();
    expect(control.selectedIDs, ['nl']);

    await tester.tap(find.byKey(const ValueKey('favorite:nl')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('favorite:fi')));
    await tester.pumpAndSettle();
    expect(control.favorites, [('nl', true), ('fi', false)]);

    final tested = control.pingCalls.length;
    await tester.tap(find.byKey(const ValueKey('ping:at')));
    await tester.pumpAndSettle();
    expect(control.pingCalls.skip(tested).map((call) => call.nodeId), ['at']);
    // Neither the star nor the latency selects the server.
    expect(control.selectedIDs, ['nl']);

    await tester.tap(find.byKey(const ValueKey('server-menu:at')));
    await tester.pumpAndSettle();
    for (final item in [
      'Rename server',
      'Share server',
      'Server config',
      'Server IP type',
      'Check exit',
      'Hide server',
    ]) {
      expect(find.widgetWithText(PopupMenuItem<String>, item), findsOneWidget);
    }
    expect(tester.takeException(), isNull);
  });

  testWidgets('the search and the favorites filter narrow the table', (
    tester,
  ) async {
    await openPage(tester);
    final favorites = find.byKey(const Key('subscription-favorites'));
    await tester.tap(favorites);
    await tester.pumpAndSettle();
    expect(shown(tester), ['fi']);
    expect(
      find.descendant(
        of: favorites,
        matching: find.byIcon(CupertinoIcons.star_fill),
      ),
      findsOneWidget,
    );

    final search = find.byKey(
      const PageStorageKey('subscription-search:daily'),
    );
    await tester.enterText(search, 'vless');
    await tester.pumpAndSettle();
    expect(shown(tester), isEmpty);
    expect(find.text('No matching servers'), findsOneWidget);

    await tester.tap(favorites);
    await tester.pumpAndSettle();
    expect(shown(tester), ['nl', 'de']);
    // Each subscription keeps its own search.
    await choose(tester, 'Travel');
    expect(shown(tester), ['se']);
    await choose(tester, 'Daily');
    expect(shown(tester), ['nl', 'de']);
    expect(tester.takeException(), isNull);
  });

  testWidgets('the order menu names the order and sorts the table', (
    tester,
  ) async {
    await openPage(tester);
    final order = find.byKey(const Key('subscription-sort'));
    expect(
      find.descendant(of: order, matching: find.text('Provider order')),
      findsOneWidget,
    );
    // Favorites first is on unless turned off.
    expect(shown(tester), ['fi', 'nl', 'de', 'at']);
    await tester.tap(order);
    await tester.pumpAndSettle();
    await tester.tap(
      find.widgetWithText(CheckedPopupMenuItem<String>, 'Alphabetical'),
    );
    await tester.pumpAndSettle();
    expect(jsonDecode(stored)['sort'], 'alphabet');
    expect(
      find.descendant(of: order, matching: find.text('Alphabetical')),
      findsOneWidget,
    );
    expect(shown(tester), ['fi', 'at', 'de', 'nl']);
    await tester.tap(order);
    await tester.pumpAndSettle();
    await tester.tap(
      find.widgetWithText(CheckedPopupMenuItem<String>, 'Favorites first'),
    );
    await tester.pumpAndSettle();
    expect(shown(tester), ['at', 'de', 'fi', 'nl']);
    expect(tester.takeException(), isNull);
  });

  testWidgets('the subscription menu selects servers and reaches the details', (
    tester,
  ) async {
    // A window too short to show what is under the table.
    await openPage(tester, size: const Size(1200, 520));
    expect(find.text('Test all'), findsOneWidget);
    await menu(tester, 'daily', 'Select servers');
    for (final id in ['nl', 'de', 'fi']) {
      expect(find.byKey(ValueKey('select-node:$id')), findsOneWidget);
    }
    expect(find.text('0 selected'), findsOneWidget);
    // Selecting takes the place of the search and the filters.
    expect(find.byKey(const Key('subscription-favorites')), findsNothing);
    await tester.tap(find.text('NL Amsterdam'));
    await tester.pumpAndSettle();
    expect(find.text('1 selected'), findsOneWidget);
    await tester.tap(find.widgetWithText(TextButton, 'Cancel'));
    await tester.pumpAndSettle();
    expect(find.byKey(const ValueKey('select-node:nl')), findsNothing);

    final view = tester.getRect(
      find.byKey(const ValueKey('subscriptions-list')),
    );
    bool inView(Finder finder) =>
        finder.evaluate().isNotEmpty &&
        view.contains(tester.getRect(finder).topLeft) &&
        view.contains(tester.getRect(finder).bottomRight);
    final routing = find.byKey(const ValueKey('subscription-routing:daily'));
    expect(inView(routing), isFalse);
    await menu(tester, 'daily', 'Routing profile');
    expect(inView(routing), isTrue);

    // Details are closed until asked for; the menu opens and shows them.
    await tester.drag(
      find.byKey(const ValueKey('subscriptions-list')),
      const Offset(0, 900),
    );
    await tester.pumpAndSettle();
    final details = find.byKey(
      const PageStorageKey('subscription-details:daily'),
    );
    expect(find.text('Refreshes'), findsNothing);
    await menu(tester, 'daily', 'Details');
    expect(tester.widget<ExpansionTile>(details).controller!.isExpanded, true);
    expect(inView(find.text('Refreshes')), isTrue);
    expect(tester.takeException(), isNull);
  });

  testWidgets('the subscriptions table fits a narrow window', (tester) async {
    await openPage(tester, size: const Size(380, 700));
    final picker = find.byKey(const Key('subscription-picker'));
    final actions = find.byKey(const ValueKey('subscription-actions:daily'));
    // The subscription's own buttons stay beside its name.
    expect(
      tester.getCenter(actions).dy,
      closeTo(tester.getCenter(picker).dy, 1),
    );
    expect(tester.getRect(actions).right, lessThanOrEqualTo(380 - 16));
    expect(tester.getRect(picker).left, 16);
    for (final id in ['nl', 'de', 'fi', 'at']) {
      final name = find.descendant(
        of: row(id),
        matching: find.byWidgetPredicate(
          (widget) => widget is Text && (widget.data ?? '').contains(' '),
        ),
      );
      // One line each: the name is cut, not wrapped.
      expect(tester.getSize(name.first).height, lessThan(30), reason: id);
      expect(
        tester.getRect(find.byKey(ValueKey('server-menu:$id'))).right,
        lessThanOrEqualTo(380 - 16),
        reason: id,
      );
    }
    expect(tester.takeException(), isNull);
  });
}
