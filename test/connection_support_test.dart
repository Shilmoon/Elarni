import 'package:elarni/connection_support.dart';
import 'package:elarni/desktop_catalog.dart';
import 'package:elarni/strings.dart' show appLocale;
import 'package:flutter/widgets.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  tearDown(() => appLocale = const Locale('en'));

  // The English is what the desktop backend sent before it sent codes.
  const notices = {
    'links_failed': 'One or more client links could not be handled.',
    'link_failed': 'The client link could not be imported.',
    'no_selected_subscription':
        'Select a server first. This routing link turns its profile on for the selected server’s subscription.',
    'scheduled_refresh_failed':
        'A scheduled subscription or routing refresh failed.',
    'connection_stopped':
        'Connection stopped unexpectedly. Reconnect to retry.',
    'connection_stopped_blocked':
        'Connection stopped unexpectedly. Block traffic without VPN keeps traffic blocked until you reconnect. Disconnecting, quitting Elarni or Repair VPN setup lifts the block.',
    'connection_stopped_repair':
        'Connection stopped unexpectedly. Reconnect to retry; if sites stay unreachable, use Repair VPN setup.',
    'vpn_dns_release_failed':
        'Disconnected, but Windows DNS could not be restored yet. If sites stay unreachable, use Repair VPN setup.',
    'system_proxy_restore_failed':
        'The system proxy setting could not be restored. Check it in your system network settings.',
    'system_proxy_not_set:10891':
        'The system proxy could not be set. Apps can still use the HTTP proxy at 127.0.0.1:10891.',
    'storage_unavailable':
        'Elarni could not read or save its catalog. Restart Elarni. If this continues, restore an encrypted backup.',
  };

  test('each backend notice code reads in the interface language', () {
    for (final MapEntry(:key, :value) in notices.entries) {
      appLocale = const Locale('en');
      expect(listenerNotice(key), value, reason: key);
      appLocale = const Locale('ru');
      final russian = listenerNotice(key);
      expect(russian.trim(), isNotEmpty, reason: key);
      expect(russian, isNot(value), reason: key);
      expect(russian, isNot(key), reason: key);
    }
    expect(
      listenerNotice('system_proxy_not_set:10891'),
      contains('127.0.0.1:10891'),
    );
  });

  // macOS words its notices itself and sends them through the same listener.
  test('a notice that is no backend code is shown as it is', () {
    for (final value in [
      'One or more client links could not be handled. Check the link and connection status.',
      'The VPN extension that is running (1.0) is not this app’s (1.1). Delete other copies of Elarni, then disconnect and connect again.',
      'system_proxy_not_set:',
      'system_proxy_not_set:port',
      'login_registration_failed',
    ]) {
      appLocale = const Locale('ru');
      expect(listenerNotice(value), value);
    }
  });

  test('a failure code has its own text, or none for the caller to fill', () {
    expect(
      failureText('route_model_too_large'),
      'The routing profile is too large: its rules and lists exceed 8 MB. '
      'Remove some rules or managed lists, then connect again.',
    );
    expect(failureText('fetch_failed'), desktopError('fetch_failed').message);
    expect(failureText('dns_failed'), desktopError('dns_failed').message);
    // The generic connection text would misname a settings or download failure.
    for (final code in [
      'operation_failed',
      'login_registration_failed',
      'approval_required',
      'provider_unreachable',
    ])
      expect(failureText(code), isNull, reason: code);
    final english = {
      for (final code in [
        'route_model_too_large',
        'fetch_failed',
        'dns_failed',
      ])
        code: failureText(code),
    };
    appLocale = const Locale('ru');
    for (final MapEntry(:key, :value) in english.entries)
      expect(failureText(key), allOf(isNotNull, isNot(value)), reason: key);
  });
}
