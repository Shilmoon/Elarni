import 'package:elarni/elarni_app.dart';
import 'package:elarni/elarni_widgets.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

import 'support/fake_tunnel_control.dart';

Future<FakeTunnelControl> pumpSettings(WidgetTester tester, Size size) async {
  final control = FakeTunnelControl();
  await tester.binding.setSurfaceSize(size);
  addTearDown(() => tester.binding.setSurfaceSize(null));
  await tester.pumpWidget(ElarniApp(tunnelControl: control, windows: true));
  await tester.pumpAndSettle();
  await tester.tap(find.byKey(const Key('nav-settings')));
  await tester.pumpAndSettle();
  return control;
}

Future<void> search(WidgetTester tester, String text) async {
  await tester.enterText(find.byKey(const Key('settings-search')), text);
  await tester.pumpAndSettle();
}

void main() {
  test('a settings row is found by its label, help, value text and heading', () {
    final rows = <Widget>[
      const ElarniCheckboxTile(
        title: Text('Launch app at login'),
        subtitle: Text('Registered'),
        value: true,
        onChanged: null,
      ),
      const ElarniSettingsSection('Local proxy'),
      const ElarniCheckboxTile(
        title: ElarniHelpLabel('Allow LAN', help: 'Other devices may connect.'),
        value: false,
        onChanged: null,
      ),
      ElarniSelectTile(
        title: const Text('SOCKS and HTTP ports'),
        trailing: OutlinedButton(onPressed: null, child: const Text('Edit')),
      ),
      const ElarniSettingsSection('Transport'),
      const Column(
        children: [
          ElarniSelect<int>(
            label: 'Timeout',
            value: 1,
            items: [DropdownMenuItem(value: 1, child: Text('1 s'))],
            onChanged: null,
          ),
        ],
      ),
      const SizedBox(height: 8),
    ];
    List<int> found(String query) => [
      for (final row in settingSearchResults(rows, query)) rows.indexOf(row),
    ];
    expect(found('LOGIN'), [0]);
    expect(found('registered'), [0]);
    // Help text finds its row; a heading finds every row under it, itself left out.
    expect(found('other devices'), [2]);
    expect(found('local proxy'), [2, 3]);
    expect(found('timeout'), [5]);
    // A button's own label is not what a setting is called.
    expect(found('edit'), isEmpty);
    expect(found('nothing here'), isEmpty);
  });

  testWidgets('searching settings shows matches from every category, live', (
    tester,
  ) async {
    final control = await pumpSettings(tester, const Size(1200, 900));
    expect(find.text('Language'), findsOneWidget);
    await search(tester, 'sniff');
    // "Enable sniffing" is under Advanced; General was open.
    expect(find.text('ADVANCED'), findsOneWidget);
    expect(find.text('Enable sniffing'), findsOneWidget);
    expect(find.text('Language'), findsNothing);
    expect(find.text('GENERAL'), findsNothing);
    final before = control.settings.sniffingEnabled;
    await tester.tap(find.text('Enable sniffing'));
    await tester.pumpAndSettle();
    expect(control.settings.sniffingEnabled, !before);
    expect(find.text('Enable sniffing'), findsOneWidget);

    // Several categories at once, in the order of the category list.
    await search(tester, 'connect');
    final general = tester.getTopLeft(find.text('GENERAL')).dy;
    final connection = tester.getTopLeft(find.text('CONNECTION')).dy;
    expect(general, lessThan(connection));
    expect(find.text('Connection recovery'), findsOneWidget);
    expect(find.text('Connection mode'), findsOneWidget);

    // A category's name brings the whole category, headings included.
    await search(tester, 'appearance');
    expect(find.text('Theme'), findsOneWidget);
    expect(find.text('Accent color'), findsOneWidget);

    await search(tester, 'no such setting');
    expect(find.text('No settings match'), findsOneWidget);

    // Clearing returns to the category that was open.
    await tester.tap(find.byTooltip('Clear search'));
    await tester.pumpAndSettle();
    expect(find.text('Language'), findsOneWidget);
    expect(find.text('No settings match'), findsNothing);

    // Choosing a category ends a search.
    await search(tester, 'sniff');
    await tester.tap(find.byKey(const Key('settings-tab-dns')));
    await tester.pumpAndSettle();
    expect(find.text('Enable sniffing'), findsNothing);
    expect(
      tester
          .widget<TextField>(find.byKey(const Key('settings-search')))
          .controller!
          .text,
      isEmpty,
    );
    expect(find.text('Resolve server before connecting'), findsOneWidget);
  });

  testWidgets('a narrow window searches settings above its category menu', (
    tester,
  ) async {
    await pumpSettings(tester, const Size(420, 760));
    final field = find.byKey(const Key('settings-search'));
    final category = find.byKey(const Key('settings-category'));
    expect(
      tester.getRect(field).bottom,
      lessThanOrEqualTo(tester.getRect(category).top),
    );
    expect(tester.getSize(field).width, 420 - 24);
    await search(tester, 'sniff');
    // The results take the place of the category and its menu.
    expect(category, findsNothing);
    expect(find.text('Enable sniffing'), findsOneWidget);
    await search(tester, '');
    expect(category, findsOneWidget);
    expect(find.text('Language'), findsOneWidget);
  });
}
