import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

/// Shows the subscription called [name] on the Subscriptions page, which
/// holds one subscription at a time.
Future<void> showSubscription(WidgetTester tester, String name) async {
  final picker = find.byKey(const Key('subscription-picker'));
  if (find
      .descendant(of: picker, matching: find.text(name))
      .evaluate()
      .isNotEmpty) {
    return;
  }
  await tester.ensureVisible(picker);
  await tester.tap(picker);
  await tester.pumpAndSettle();
  await tester.tap(find.widgetWithText(MenuItemButton, name));
  await tester.pumpAndSettle();
}

/// Chooses [item] in the "…" menu beside the shown subscription.
Future<void> subscriptionMenu(
  WidgetTester tester,
  String id,
  String item,
) async {
  final button = find.byKey(ValueKey('subscription-actions:$id'));
  await tester.ensureVisible(button);
  await tester.tap(button);
  await tester.pumpAndSettle();
  await tester.tap(find.widgetWithText(PopupMenuItem<String>, item));
  await tester.pumpAndSettle();
}

/// Opens the "…" menu of a server's table row.
Future<void> openServerMenu(WidgetTester tester, String nodeId) async {
  final button = find.byKey(ValueKey('server-menu:$nodeId'));
  await tester.ensureVisible(button);
  await tester.tap(button);
  await tester.pumpAndSettle();
}

/// Chooses [item] in the "…" menu of a server's table row.
Future<void> serverMenu(WidgetTester tester, String nodeId, String item) async {
  await openServerMenu(tester, nodeId);
  await tester.tap(find.widgetWithText(PopupMenuItem<String>, item));
  await tester.pumpAndSettle();
}

/// Closes an open menu without choosing from it.
Future<void> dismissMenu(WidgetTester tester) async {
  await tester.tapAt(const Offset(2, 2));
  await tester.pumpAndSettle();
}
