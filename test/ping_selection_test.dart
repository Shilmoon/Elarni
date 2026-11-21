import 'dart:async';

import 'package:elarni/elarni_app.dart';
import 'package:elarni/tunnel_channel.dart';
import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  final now = DateTime.utc(2026, 11, 21, 12);
  PingFailure failure({
    Duration age = Duration.zero,
    bool unreachable = true,
  }) => (at: now.subtract(age), unreachable: unreachable);
  PingResult result(
    String status, {
    int latency = -1,
    Duration age = Duration.zero,
    String revision = 'r1',
  }) => PingResult(
    nodeId: 'a',
    status: status,
    latencyMs: latency,
    timestamp: now.subtract(age).millisecondsSinceEpoch / 1000,
    configurationRevision: revision,
  );
  bool unreachable({PingResult? result, PingFailure? failure}) =>
      pingUnreachable(
        result: result,
        failure: failure,
        revision: 'r1',
        now: now,
      );

  group('what a failed test leaves', () {
    test('a test that never ran leaves nothing', () {
      for (final code in ['busy', 'cancelled', 'operation_cancelled']) {
        expect(pingFailureFor(PlatformException(code: code), now), isNull);
      }
    });

    test('a failure to reach the server counts', () {
      for (final error in [
        PlatformException(code: 'ping_failed'),
        PlatformException(code: 'operation_failed'),
        PlatformException(code: 'connection_failed'),
        PlatformException(code: 'icmp_failed_restored'),
        StateError('Synthetic endpoint unavailable'),
        TimeoutException('seven seconds'),
      ]) {
        expect(pingFailureFor(error, now), (at: now, unreachable: true));
      }
    });

    test('a test stopped before it reached the server does not count', () {
      for (final code in [
        'not_connected',
        'unsupported_ping_method',
        'disconnect_to_test',
        'unavailableProbePath',
        'icmp_restore_failed',
        'stale_probe',
        'native_unavailable',
        'per_app_target_missing',
        'invalid_profile',
        'invalid_request',
        'storage_unavailable',
        'storage_corrupt',
        'automation_staleContext',
      ]) {
        expect(pingFailureFor(PlatformException(code: code), now), (
          at: now,
          unreachable: false,
        ), reason: code);
      }
      expect(
        pingFailureFor(
          const FormatException('Ping settings are invalid.'),
          now,
        ),
        (at: now, unreachable: false),
      );
    });

    test('a failure the bulk test cut short does not count', () {
      for (final error in [
        PlatformException(code: 'ping_failed'),
        TimeoutException('seven seconds'),
      ]) {
        expect(pingFailureFor(error, now, cutShort: true), (
          at: now,
          unreachable: false,
        ));
      }
      // Busy still left nothing to cut short.
      expect(
        pingFailureFor(PlatformException(code: 'busy'), now, cutShort: true),
        isNull,
      );
    });
  });

  group('Select unreachable', () {
    test('ticks a current failure to reach the server', () {
      expect(unreachable(failure: failure()), isTrue);
      expect(
        unreachable(failure: failure(age: const Duration(seconds: 299))),
        isTrue,
      );
      expect(unreachable(result: result('failed')), isTrue);
      expect(unreachable(result: result('success', latency: -1)), isTrue);
    });

    test('skips a failure that did not reach the server', () {
      expect(unreachable(failure: failure(unreachable: false)), isFalse);
      expect(unreachable(), isFalse);
    });

    test('skips a test as old as the list marks stale', () {
      expect(
        unreachable(failure: failure(age: const Duration(minutes: 5))),
        isFalse,
      );
      expect(
        unreachable(failure: failure(age: const Duration(hours: 3))),
        isFalse,
      );
      expect(
        unreachable(result: result('failed', age: const Duration(minutes: 5))),
        isFalse,
      );
      expect(
        unreachable(failure: failure(age: const Duration(seconds: -5))),
        isFalse,
        reason: 'a clock that went back',
      );
    });

    test('skips a test of an earlier configuration', () {
      expect(unreachable(result: result('failed', revision: 'r0')), isFalse);
    });

    test('skips a passed test', () {
      expect(unreachable(result: result('success', latency: 40)), isFalse);
      expect(
        unreachable(result: result('success', latency: 40), failure: failure()),
        isFalse,
        reason: 'the result wins, as in the row label',
      );
    });
  });
}
