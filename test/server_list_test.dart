import 'dart:async';
import 'dart:convert';
import 'package:flutter/gestures.dart';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:elarni/desktop_integration.dart';
import 'package:elarni/elarni_app.dart';
import 'package:elarni/elarni_widgets.dart';
import 'package:elarni/server_locations.dart';
import 'package:elarni/tunnel_channel.dart';
import 'elarni_app_test.dart' show reveal, selectCategory;
import 'server_browser_test.dart'
    show NoticeControl, closedGroups, countryFixture, server;

PingResult reply(String id, {int latency = 30}) => PingResult(
  nodeId: id,
  status: 'success',
  latencyMs: latency,
  timestamp: DateTime.now().millisecondsSinceEpoch / 1000,
);

Future<void> openServers(WidgetTester tester, String id, String name) async {
  await tester.tap(find.byKey(const Key('nav-subscriptions')));
  await tester.pumpAndSettle();
  await tester.tap(
    find.descendant(
      of: find.byKey(ValueKey('subscription:$id')),
      matching: find.text(name),
    ),
  );
  await tester.pumpAndSettle();
}

void main() {
  testWidgets(
    'launch tests only open groups, and opening a group tests its stale servers',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1280, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final control = NoticeControl()..snapshot = countryFixture();
      control.onPing = (id) async => id == 'nl-vmess'
          ? throw StateError('Synthetic endpoint unavailable')
          : reply(id);
      List<String> pinged() =>
          control.pingCalls.map((call) => call.nodeId).toList();
      closedGroups(tester, ['daily:NL', 'travel:NL']);
      await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
      await tester.pumpAndSettle();
      // The two Netherlands groups were closed before; the others are open.
      expect(pinged(), ['de', 'unknown']);
      final netherlands = find.byKey(const ValueKey('country:daily:NL::all'));
      await tester.tap(netherlands);
      await tester.pumpAndSettle();
      expect(pinged(), ['de', 'unknown', 'nl-vless', 'nl-trojan']);
      // Reopening within five minutes repeats nothing.
      await tester.tap(netherlands);
      await tester.pumpAndSettle();
      await tester.tap(netherlands);
      await tester.pumpAndSettle();
      expect(pinged(), hasLength(4));
      final travel = find.byKey(const ValueKey('country:travel:NL::all'));
      await reveal(tester, travel);
      await tester.tap(travel);
      await tester.pumpAndSettle();
      expect(pinged().last, 'nl-vmess');
      expect(find.text('Unavailable'), findsOneWidget);
      // A server that just failed is not retried by every open.
      await tester.tap(travel);
      await tester.pumpAndSettle();
      await tester.tap(travel);
      await tester.pumpAndSettle();
      expect(pinged(), hasLength(5));
      // Test all still tests every server.
      control.pingCalls.clear();
      await tester.tap(find.byKey(const Key('ping-all')));
      await tester.pumpAndSettle();
      expect(pinged()..sort(), [
        'de',
        'nl-trojan',
        'nl-vless',
        'nl-vmess',
        'unknown',
      ]);
      expect(control.connectCalls, 0);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox());
    },
  );

  testWidgets('a group opened during a test is tested when the test ends', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1280, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final pending = <String, Completer<PingResult>>{};
    final control = NoticeControl()..snapshot = countryFixture();
    control.onPing = (id) => (pending[id] = Completer<PingResult>()).future;
    closedGroups(tester, ['daily:NL', 'travel:NL']);
    await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
    await tester.pumpAndSettle();
    expect(pending.keys, ['de', 'unknown']);
    await tester.tap(find.byKey(const ValueKey('country:daily:NL::all')));
    await tester.pumpAndSettle();
    expect(pending.keys, ['de', 'unknown']);
    pending['de']!.complete(reply('de'));
    pending['unknown']!.complete(reply('unknown'));
    await tester.pumpAndSettle();
    expect(pending.keys, ['de', 'unknown', 'nl-vless', 'nl-trojan']);
    pending['nl-vless']!.complete(reply('nl-vless', latency: 80));
    pending['nl-trojan']!.complete(reply('nl-trojan', latency: 90));
    await tester.pumpAndSettle();
    expect(
      tester
          .widget<Text>(find.byKey(const ValueKey('country-ping:daily:NL')))
          .data,
      '80 ms',
    );
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('a long list builds only the rows on screen', (tester) async {
    await tester.binding.setSurfaceSize(const Size(1280, 900));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final control = NoticeControl()
      ..snapshot = SubscriptionSnapshot(
        selectedNodeId: 's0',
        subscriptions: [
          SubscriptionGroup(
            id: 'big',
            name: 'Big provider',
            status: 'ready',
            nodes: [
              for (var i = 0; i < 2000; i++)
                server('s$i', 'Server $i', 'VLESS', selected: i == 0),
            ],
          ),
        ],
      );
    control.onPing = (id) async => reply(id);
    await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
    await tester.pumpAndSettle();
    expect(find.byKey(const ValueKey('node:s0')), findsOneWidget);
    expect(
      find.byKey(const ValueKey('node:s1999'), skipOffstage: false),
      findsNothing,
    );
    await tester.scrollUntilVisible(
      find.byKey(const ValueKey('node:s200')),
      600,
      scrollable: find
          .descendant(
            of: find.byKey(const ValueKey('servers-list')),
            matching: find.byType(Scrollable),
          )
          .first,
    );
    expect(
      find.byKey(const ValueKey('node:s0'), skipOffstage: false),
      findsNothing,
    );
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets('favorites come first unless turned off', (tester) async {
    var stored = '{}';
    final messenger = tester.binding.defaultBinaryMessenger;
    messenger.setMockMethodCallHandler(DesktopIntegration.channel, (
      call,
    ) async {
      if (call.method == 'getPreferences') return stored;
      if (call.method == 'setPreferences')
        stored = (call.arguments as Map)['json'] as String;
      return null;
    });
    addTearDown(
      () =>
          messenger.setMockMethodCallHandler(DesktopIntegration.channel, null),
    );
    await tester.binding.setSurfaceSize(const Size(1280, 1400));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final fixture = countryFixture();
    final daily = fixture.subscriptions.first;
    final control = NoticeControl()
      ..snapshot = SubscriptionSnapshot(
        selectedNodeId: fixture.selectedNodeId,
        subscriptions: [
          SubscriptionGroup(
            id: daily.id,
            name: daily.name,
            status: daily.status,
            nodes: [
              for (final node in daily.nodes)
                SubscriptionNode(
                  id: node.id,
                  name: node.name,
                  protocol: node.protocol,
                  transport: node.transport,
                  security: node.security,
                  selected: node.selected,
                  favorite: node.id == 'nl-trojan',
                ),
            ],
          ),
        ],
      );
    await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
    await tester.pumpAndSettle();
    double top(String id) =>
        tester.getTopLeft(find.byKey(ValueKey('node:$id'))).dy;
    expect(top('nl-trojan'), lessThan(top('nl-vless')));
    await tester.tap(find.byKey(const Key('nav-settings')));
    await tester.pumpAndSettle();
    await selectCategory(tester, SettingsCategory.general);
    await reveal(tester, find.text('Favorites first'));
    await tester.tap(find.text('Favorites first'));
    await tester.pumpAndSettle();
    expect(jsonDecode(stored)['favoritesFirst'], isFalse);
    await tester.tap(find.byKey(const Key('nav-home')));
    await tester.pumpAndSettle();
    expect(top('nl-vless'), lessThan(top('nl-trojan')));
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets(
    'the favorites filter lists servers without groups, each with its flag',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1280, 900));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final fixture = countryFixture();
      const favorites = {'nl-trojan', 'unknown', 'nl-vmess'};
      final control = NoticeControl()
        ..snapshot = SubscriptionSnapshot(
          selectedNodeId: fixture.selectedNodeId,
          subscriptions: [
            for (final group in fixture.subscriptions)
              SubscriptionGroup(
                id: group.id,
                name: group.name,
                status: group.status,
                nodes: [
                  for (final node in group.nodes)
                    SubscriptionNode(
                      id: node.id,
                      name: node.name,
                      protocol: node.protocol,
                      transport: node.transport,
                      security: node.security,
                      selected: node.selected,
                      favorite: favorites.contains(node.id),
                    ),
                ],
              ),
          ],
        );
      control.onPing = (id) async => reply(id);
      closedGroups(tester, ['daily:NL', 'travel:NL']);
      await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
      await tester.pumpAndSettle();
      // The Netherlands groups were closed before, so their servers are untested.
      expect(find.byKey(const ValueKey('node:nl-trojan')), findsNothing);
      control.pingCalls.clear();
      await tester.tap(find.widgetWithText(ChoiceChip, 'Favorites'));
      await tester.pumpAndSettle();
      Finder row(String id) => find.byKey(ValueKey('node:$id'));
      for (final id in favorites) expect(row(id), findsOneWidget);
      expect(row('de'), findsNothing);
      expect(row('nl-vless'), findsNothing);
      expect(
        find.byWidgetPredicate((widget) {
          final key = widget.key;
          return key is ValueKey<String> &&
              (key.value.startsWith('country:') ||
                  key.value.startsWith('server-subscription:'));
        }),
        findsNothing,
      );
      expect(find.text('Netherlands'), findsNothing);
      // The flag follows the name; a server without a country has none.
      for (final (id, name) in [
        ('nl-trojan', 'Netherlands • Amsterdam 02'),
        ('nl-vmess', 'NL Rotterdam'),
      ]) {
        final flag = find.descendant(
          of: row(id),
          matching: find.byType(LocationFlag),
        );
        expect(flag, findsOneWidget);
        expect(
          tester.getTopLeft(flag).dx,
          greaterThan(
            tester
                .getTopRight(
                  find.descendant(of: row(id), matching: find.text(name)),
                )
                .dx,
          ),
        );
      }
      expect(
        find.descendant(
          of: row('unknown'),
          matching: find.byType(LocationFlag),
        ),
        findsNothing,
      );
      // The subscription stays on the row, the only place left that names it.
      expect(
        find.descendant(of: row('nl-vmess'), matching: find.text('Travel')),
        findsOneWidget,
      );
      // A favorite is listed whether or not its group is closed, and showing
      // the list tests the servers that a closed group kept untested.
      expect(control.pingCalls.map((call) => call.nodeId).toList()..sort(), [
        'nl-trojan',
        'nl-vmess',
      ]);
      // The other filters keep their groups.
      await tester.tap(find.widgetWithText(ChoiceChip, 'All'));
      await tester.pumpAndSettle();
      expect(
        find.byKey(const ValueKey('server-subscription:daily')),
        findsOneWidget,
      );
      expect(
        find.byKey(const ValueKey('country:daily:NL::all')),
        findsOneWidget,
      );
      expect(
        find.descendant(of: row('de'), matching: find.byType(LocationFlag)),
        findsNothing,
      );
      expect(control.connectCalls, 0);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox());
    },
  );

  testWidgets(
    'every group starts open, and a closed one stays closed after a restart',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1280, 1400));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final control = NoticeControl()..snapshot = countryFixture();
      control.onPing = (id) async => reply(id);
      List<String> pinged() =>
          control.pingCalls.map((call) => call.nodeId).toList()..sort();
      final stored = closedGroups(tester, []);
      Object? remembered() => jsonDecode(stored())['closedGroups'];
      Finder row(String id) => find.byKey(ValueKey('node:$id'));
      const all = ['de', 'nl-trojan', 'nl-vless', 'nl-vmess', 'unknown'];
      await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
      await tester.pumpAndSettle();
      for (final id in all) expect(row(id), findsOneWidget);
      expect(pinged(), all);
      // Under its subscription a row says how it connects, not the subscription again.
      expect(
        find.descendant(of: row('de'), matching: find.text('tcp · tls')),
        findsOneWidget,
      );
      expect(
        find.descendant(of: row('de'), matching: find.text('boVPN')),
        findsNothing,
      );
      await tester.tap(find.byKey(const ValueKey('country:daily:NL::all')));
      await tester.pumpAndSettle();
      expect(row('nl-vless'), findsNothing);
      expect(remembered(), ['daily:NL']);
      await tester.tap(
        find.byKey(const ValueKey('server-subscription:travel')),
      );
      await tester.pumpAndSettle();
      expect(row('nl-vmess'), findsNothing);
      expect(remembered(), ['daily:NL', 'travel']);

      await tester.pumpWidget(const SizedBox());
      control.pingCalls.clear();
      await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
      await tester.pumpAndSettle();
      expect(row('de'), findsOneWidget);
      expect(row('unknown'), findsOneWidget);
      for (final id in ['nl-vless', 'nl-trojan', 'nl-vmess'])
        expect(row(id), findsNothing);
      // A start tests what it shows.
      expect(pinged(), ['de', 'unknown']);
      // Opening a group forgets it, and tests the servers it kept untested.
      await tester.tap(find.byKey(const ValueKey('country:daily:NL::all')));
      await tester.pumpAndSettle();
      expect(row('nl-vless'), findsOneWidget);
      expect(remembered(), ['travel']);
      await tester.tap(
        find.byKey(const ValueKey('server-subscription:travel')),
      );
      await tester.pumpAndSettle();
      expect(row('nl-vmess'), findsOneWidget);
      expect(remembered(), isEmpty);
      expect(pinged(), all);
      expect(control.connectCalls, 0);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox());
    },
  );

  testWidgets(
    'By country lists the servers of every subscription under their country',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1280, 1400));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final control = NoticeControl()..snapshot = countryFixture();
      control.onPing = (id) async => reply(id);
      final stored = closedGroups(tester, ['travel']);
      await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
      await tester.pumpAndSettle();
      expect(find.widgetWithText(ChoiceChip, 'Recent'), findsNothing);
      Finder row(String id) => find.byKey(ValueKey('node:$id'));
      expect(row('nl-vmess'), findsNothing);
      control.pingCalls.clear();
      await tester.tap(find.widgetWithText(ChoiceChip, 'By country'));
      await tester.pumpAndSettle();
      expect(
        find.byWidgetPredicate((widget) {
          final key = widget.key;
          return key is ValueKey<String> &&
              key.value.startsWith('server-subscription:');
        }),
        findsNothing,
      );
      // One group for a country, whichever subscriptions its servers are in.
      final netherlands = find.byKey(const ValueKey('country::NL::country'));
      expect(
        find.descendant(of: netherlands, matching: find.text('3 servers')),
        findsOneWidget,
      );
      for (final (id, subscription) in [
        ('nl-vless', 'boVPN'),
        ('nl-trojan', 'boVPN'),
        ('nl-vmess', 'Travel'),
        ('de', 'boVPN'),
        ('unknown', 'boVPN'),
      ]) {
        expect(
          find.descendant(of: row(id), matching: find.text(subscription)),
          findsOneWidget,
        );
      }
      double top(Finder finder) => tester.getTopLeft(finder).dy;
      expect(
        top(find.byKey(const ValueKey('country::DE::country'))),
        lessThan(top(netherlands)),
      );
      expect(top(netherlands), lessThan(top(row('nl-vmess'))));
      expect(
        top(row('nl-vmess')),
        lessThan(top(find.byKey(const ValueKey('country::other::country')))),
      );
      // The list shows a server its closed subscription kept untested.
      expect(control.pingCalls.map((call) => call.nodeId), ['nl-vmess']);
      // Its groups close apart from those of All.
      await tester.tap(netherlands);
      await tester.pumpAndSettle();
      expect(row('nl-vless'), findsNothing);
      expect(jsonDecode(stored())['closedGroups'], [':NL', 'travel']);
      await tester.tap(find.widgetWithText(ChoiceChip, 'All'));
      await tester.pumpAndSettle();
      expect(row('nl-vless'), findsOneWidget);
      expect(control.connectCalls, 0);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox());
    },
  );

  testWidgets('select unreachable hides the failed servers together', (
    tester,
  ) async {
    await tester.binding.setSurfaceSize(const Size(1280, 1400));
    addTearDown(() => tester.binding.setSurfaceSize(null));
    final control = NoticeControl()..snapshot = countryFixture();
    control.onPing = (id) async => const {'nl-trojan', 'de'}.contains(id)
        ? throw StateError('Synthetic endpoint unavailable')
        : reply(id);
    await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
    await tester.pumpAndSettle();
    await openServers(tester, 'daily', 'boVPN');
    await tester.tap(find.byKey(const ValueKey('subscription-ping-all:daily')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('select-servers:daily')));
    await tester.pumpAndSettle();
    // The connected server cannot be removed, so it cannot be selected.
    expect(
      tester
          .widget<Checkbox>(find.byKey(const ValueKey('select-node:de')))
          .onChanged,
      isNull,
    );
    await tester.tap(find.text('Select unreachable'));
    await tester.pumpAndSettle();
    expect(find.text('1 selected'), findsOneWidget);
    await tester.tap(find.byKey(const ValueKey('select-node:unknown')));
    await tester.pumpAndSettle();
    expect(find.text('2 selected'), findsOneWidget);
    await tester.tap(find.widgetWithText(TextButton, 'Hide'));
    await tester.pumpAndSettle();
    expect(find.text('Hide 2 servers?'), findsOneWidget);
    await tester.tap(find.widgetWithText(FilledButton, 'Hide'));
    await tester.pumpAndSettle();
    expect(control.deletedBatches, [
      ['nl-trojan', 'unknown'],
    ]);
    expect(find.text('2 selected'), findsNothing);
    expect(find.byKey(const ValueKey('select-node:unknown')), findsNothing);
    expect(control.selectedIDs, isEmpty);
    expect(tester.takeException(), isNull);
    await tester.pumpWidget(const SizedBox());
  });

  testWidgets(
    'a local group shares its chosen servers as links in group order; a provider group does not',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1280, 1400));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final fixture = countryFixture();
      const links =
          'vless://one@127.0.0.1:443#Local%20One\nss://three@127.0.0.1:8388#Local%20Three';
      final control = NoticeControl()
        ..snapshot = SubscriptionSnapshot(
          selectedNodeId: fixture.selectedNodeId,
          subscriptions: [
            ...fixture.subscriptions,
            SubscriptionGroup(
              id: 'mine',
              name: 'Local profiles',
              status: 'ready',
              isLocal: true,
              nodes: [
                server('one', 'Local One', 'VLESS'),
                server('two', 'Local Two', 'Trojan'),
                server('three', 'Local Three', 'Shadowsocks'),
              ],
            ),
          ],
        )
        ..shareLinksText = links;
      control.onPing = (id) async => reply(id);
      await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
      await tester.pumpAndSettle();
      await openServers(tester, 'mine', 'Local profiles');
      await tester.tap(find.byKey(const ValueKey('select-servers:mine')));
      await tester.pumpAndSettle();
      final share = find.byKey(const ValueKey('share-servers:mine'));
      expect(tester.widget<TextButton>(share).onPressed, isNull);
      // Chosen out of order, they are shared in the group's order.
      await tester.tap(find.byKey(const ValueKey('select-node:three')));
      await tester.tap(find.byKey(const ValueKey('select-node:one')));
      await tester.pumpAndSettle();
      await tester.tap(share);
      await tester.pumpAndSettle();
      final (group, ids) = control.sharedLinks.single;
      expect(group, 'mine');
      expect(ids, ['one', 'three']);
      // The links did not fit a QR code; the dialog still shows them.
      expect(control.qrTexts, [links]);
      final dialog = find.byType(AlertDialog);
      expect(
        find.descendant(of: dialog, matching: find.text('Share servers')),
        findsOneWidget,
      );
      expect(
        tester
            .widget<SelectableText>(find.byKey(const Key('shared-links')))
            .data,
        links,
      );
      expect(
        find.text('This configuration is too large for a QR code. Use Copy.'),
        findsOneWidget,
      );
      expect(find.byKey(const Key('shared-save-text')), findsOneWidget);
      await tester.tap(find.text('Close'));
      await tester.pumpAndSettle();
      // A server without a link is named.
      control.shareLinksError = PlatformException(
        code: 'no_share_link',
        message: 'Local Three has no share link. Share it from its own page.',
        details: 'Local Three',
      );
      await tester.tap(share);
      await tester.pumpAndSettle();
      expect(dialog, findsNothing);
      expect(
        find.text(
          'Local Three has no share link that keeps all its settings. Share it with Share server in its menu.',
        ),
        findsOneWidget,
      );
      // A provider's group has its subscription link to share instead.
      await tester.tap(find.text('Cancel'));
      await tester.pumpAndSettle();
      await openServers(tester, 'daily', 'boVPN');
      await tester.tap(find.byKey(const ValueKey('select-servers:daily')));
      await tester.pumpAndSettle();
      expect(find.widgetWithText(TextButton, 'Hide'), findsOneWidget);
      expect(find.byKey(const ValueKey('share-servers:daily')), findsNothing);
      expect(control.sharedLinks, hasLength(2));
      expect(control.connectCalls, 0);
      expect(control.selectedIDs, isEmpty);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox());
    },
  );

  testWidgets(
    'check exit names where a server reaches the internet and flags a name that disagrees',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1280, 1400));
      addTearDown(() => tester.binding.setSurfaceSize(null));
      final control = NoticeControl()..snapshot = countryFixture();
      control.onPing = (id) async => control.pingCalls.last.trace
          ? PingResult(
              nodeId: id,
              status: 'success',
              latencyMs: 40,
              timestamp: DateTime.now().millisecondsSinceEpoch / 1000,
              exitIP: '203.0.113.7',
              exitCountry: 'DE',
            )
          : reply(id);
      await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
      await tester.pumpAndSettle();
      await openServers(tester, 'daily', 'boVPN');
      await tester.tap(find.byKey(const ValueKey('check-exit:nl-vless')));
      await tester.pumpAndSettle();
      final call = control.pingCalls.last;
      expect(call.nodeId, 'nl-vless');
      expect(call.trace, isTrue);
      expect(call.protocol, 'proxy');
      expect(call.url, 'https://cp.cloudflare.com/cdn-cgi/trace');
      expect(call.expectedStatus, 200);
      expect(
        find.descendant(
          of: find.byKey(const ValueKey('node:nl-vless')),
          matching: find.text('Exit: Germany · 203.0.113.7'),
        ),
        findsOneWidget,
      );
      expect(
        find.text(
          "Amsterdam 01's name says Netherlands, but its exit is in Germany.",
        ),
        findsOneWidget,
      );
      expect(find.byKey(const ValueKey('check-exit:nl-vless')), findsNothing);
      await tester.tap(find.byKey(const ValueKey('check-exit:de')));
      await tester.pumpAndSettle();
      expect(
        find.descendant(
          of: find.byKey(const ValueKey('node:de')),
          matching: find.text('Exit: Germany · 203.0.113.7'),
        ),
        findsOneWidget,
      );
      expect(find.textContaining("Frankfurt's name says"), findsNothing);
      // Home offers the same check from a server's menu.
      await tester.tap(find.byKey(const Key('nav-home')));
      await tester.pumpAndSettle();
      await tester.tap(
        find.byKey(const ValueKey('node:unknown')),
        buttons: kSecondaryButton,
      );
      await tester.pumpAndSettle();
      await tester.tap(find.text('Check exit'));
      await tester.pumpAndSettle();
      expect(control.pingCalls.last.nodeId, 'unknown');
      expect(control.pingCalls.last.trace, isTrue);
      expect(
        find.descendant(
          of: find.byKey(const ValueKey('node:unknown')),
          matching: find.text('Exit: Germany · 203.0.113.7'),
        ),
        findsOneWidget,
      );
      expect(control.connectCalls, 0);
      expect(control.selectedIDs, isEmpty);
      expect(tester.takeException(), isNull);
      await tester.pumpWidget(const SizedBox());
    },
  );
}
