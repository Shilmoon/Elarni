import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:elarni/tunnel_channel.dart';
import 'package:elarni/desktop_catalog.dart';

void main() {
  test(
    'managed lists refresh independently and keep the last good download',
    () async {
      var now = DateTime.fromMillisecondsSinceEpoch(100000 * 1000);
      var reachable = false;
      final catalog = DesktopCatalog(
        DesktopCatalog.empty(),
        (method, payload) async {
          final host = Uri.parse(payload['url']! as String).host;
          if (host == 'broken.example' && !reachable)
            throw PlatformException(code: 'fetch_failed');
          return {'body': host};
        },
        (operation, payload) => switch (operation) {
          'parseManagedDomains' => {
            'domains': ['full:${payload['text']}'],
          },
          'mergeManagedRouting' => {
            'routeModel': {'lists': payload['lists']},
          },
          _ => throw StateError(operation),
        },
        clock: () => now,
      );
      CatalogMap source(String id) => {
        'id': id,
        'url': 'https://$id.example/list',
        'destination': 'proxy',
        'matchMode': 'exact',
      };
      catalog.putRoute({
        'id': 'route',
        'profile': {
          'ManagedSources': [source('broken'), source('fresh')],
        },
        'lists': {
          'broken': ['full:kept.example'],
        },
        'routeModel': <String, Object?>{},
      });
      String? shownError() =>
          catalogList(
                catalog.routingSnapshot()['profiles'],
              ).single['managedError']
              as String?;
      // The interface words the code; the message is for the headless output.
      String? shownCode() =>
          catalogList(
                catalog.routingSnapshot()['profiles'],
              ).single['managedErrorCode']
              as String?;
      final kept = {
        'broken': ['full:kept.example'],
        'fresh': ['full:fresh.example'],
      };
      // The failed list keeps its last download and retries on the
      // subscription schedule while the other list still updates.
      for (final delay in [60, 300, 1800, 1800]) {
        await catalog.refreshManaged('route');
        final route = catalog.route('route');
        expect(route['lists'], kept);
        expect(route['routeModel'], {'lists': kept});
        expect(route['managedError'], 'fetch_failed');
        expect(route['nextManagedRefreshAt'], catalog.now + delay);
        expect(shownError(), desktopError('fetch_failed').message);
        expect(shownCode(), 'fetch_failed');
        now = now.add(Duration(seconds: delay));
      }
      reachable = true;
      await catalog.refreshManaged('route');
      final route = catalog.route('route');
      expect(route['lists'], {
        ...kept,
        'broken': ['full:broken.example'],
      });
      expect(route['managedError'], isNull);
      expect(route['nextManagedRefreshAt'], catalog.now + 86400);
      expect(shownError(), isNull);
      expect(shownCode(), isNull);
      reachable = false;
      await catalog.refreshManaged('route');
      expect(
        catalog.route('route')['nextManagedRefreshAt'],
        catalog.now + 60,
        reason: 'A success restarts the retry schedule',
      );
    },
  );

  test(
    'one managed list refreshed alone leaves the others and their schedule',
    () async {
      final now = DateTime.fromMillisecondsSinceEpoch(100000 * 1000);
      final fetched = <String>[];
      var reachable = true;
      final catalog = DesktopCatalog(
        DesktopCatalog.empty(),
        (method, payload) async {
          final host = Uri.parse(payload['url']! as String).host;
          fetched.add(host);
          if (!reachable) throw PlatformException(code: 'fetch_failed');
          return {'body': host};
        },
        (operation, payload) => switch (operation) {
          'parseManagedDomains' => {
            'domains': ['full:${payload['text']}'],
          },
          'mergeManagedRouting' => {
            'routeModel': {'lists': payload['lists']},
          },
          _ => throw StateError(operation),
        },
        clock: () => now,
      );
      CatalogMap source(String id) => {
        'id': id,
        'url': 'https://$id.example/list',
        'destination': 'proxy',
        'matchMode': 'exact',
      };
      final scheduled = catalog.now + 5000;
      catalog.putRoute({
        'id': 'route',
        'profile': {
          'ManagedSources': [source('first'), source('second')],
        },
        'lists': {
          'first': ['full:old-first.example'],
          'second': ['full:old-second.example'],
        },
        'routeModel': <String, Object?>{},
        'nextManagedRefreshAt': scheduled,
        'managedError': 'fetch_failed',
        'managedFailures': 2,
      });
      expect(await catalog.refreshManaged('route', sourceId: 'second'), isNull);
      expect(fetched, ['second.example']);
      var route = catalog.route('route');
      final lists = {
        'first': ['full:old-first.example'],
        'second': ['full:second.example'],
      };
      expect(route['lists'], lists);
      expect(route['routeModel'], {'lists': lists});
      // The other list's failure still stands, and is retried on time.
      expect(route['managedError'], 'fetch_failed');
      expect(route['managedFailures'], 2);
      expect(route['nextManagedRefreshAt'], scheduled);

      reachable = false;
      expect(
        await catalog.refreshManaged('route', sourceId: 'first'),
        'fetch_failed',
      );
      expect(fetched, ['second.example', 'first.example']);
      route = catalog.route('route');
      expect(route['lists'], lists, reason: 'the last good copy stays');
      expect(route['managedError'], 'fetch_failed');
      expect(route['managedFailures'], 3);
      expect(route['nextManagedRefreshAt'], catalog.now + 1800);

      await expectLater(
        catalog.refreshManaged('route', sourceId: 'missing'),
        throwsA(
          isA<PlatformException>().having(
            (error) => error.code,
            'code',
            'not_found',
          ),
        ),
      );
      expect(fetched, hasLength(2));

      reachable = true;
      expect(await catalog.refreshManaged('route'), isNull);
      route = catalog.route('route');
      expect(route['managedError'], isNull);
      expect(route['managedFailures'], 0);
      expect(route['nextManagedRefreshAt'], catalog.now + 86400);
    },
  );

  // A refresh under a clock set years ahead writes a time years away; once
  // the clock is right again, the schedule must not wait for it.
  test('a schedule written under a clock that was ahead is due again', () {
    var now = DateTime.utc(2036);
    final catalog = DesktopCatalog(
      DesktopCatalog.empty(),
      (_, _) async => {},
      (_, _) => {},
      clock: () => now,
    );
    final written = catalog.now + 86400;
    expect(catalog.refreshDue(written, 86400), isFalse);
    expect(catalog.refreshDue(null, 86400), isFalse, reason: 'a paused one');

    now = DateTime.utc(2026, 11, 13);
    expect(catalog.refreshDue(written, 86400), isTrue);
    expect(catalog.refreshDue(catalog.now + 86400, 86400), isFalse);
    expect(
      catalog.refreshDue(catalog.now + 86400 + 600, 86400),
      isFalse,
      reason: 'a clock corrected by minutes refreshes nothing early',
    );
    expect(catalog.refreshDue(catalog.now - 1, 86400), isTrue);
  });

  // A refresh is scheduled its interval ahead, give or take a tenth: a
  // provider's 24 hours can be 26.4 hours away and still be on time.
  test('a refresh scheduled with its jitter is not due before its time', () {
    final now = DateTime.utc(2026, 11, 13);
    final catalog = DesktopCatalog(
      DesktopCatalog.empty(),
      (_, _) async => {},
      (_, _) => {},
      clock: () => now,
    );
    for (final hours in [0, 4, 24, 168, 1000]) {
      final next =
          catalog.now +
          (hours.clamp(1, 168) * 3600 * 1.0999).clamp(900, 604800);
      final source = {
        'id': 'source',
        'url': 'https://example.com/subscription',
        'status': 'ready',
        'metadata': {'updateIntervalHours': hours},
        'lastSuccessfulFetch': catalog.now,
        'refreshInterval': hours * 3600,
        'nextRefreshAt': next,
      };
      expect(
        catalog.subscriptionRefreshDue(source),
        isFalse,
        reason: '$hours hours',
      );
      expect(
        catalog.subscriptionRefreshDue({
          ...source,
          'nextRefreshAt': next + 2 * 86400,
        }),
        isTrue,
        reason: '$hours hours, written under a clock two days ahead',
      );
    }
  });

  test('failed routing downloads retry on the subscription schedule', () {
    var now = DateTime.fromMillisecondsSinceEpoch(100000 * 1000);
    final catalog = DesktopCatalog(
      DesktopCatalog.empty(),
      (_, _) async => {},
      (_, _) => {},
      clock: () => now,
    );
    catalog.putRoute({
      'id': 'route',
      'profile': <String, Object?>{},
      'routeModel': <String, Object?>{},
      'geofileStatus': 'ready',
    });
    final failure = desktopError('fetch_failed');
    for (final delay in [60, 300, 1800, 1800]) {
      catalog.geofilesFailed('route', failure);
      final route = catalog.route('route');
      expect(route['geofileStatus'], 'failed');
      expect(route['geofileError'], failure.message);
      // The interface words the code; the message is for the headless output.
      expect(route['geofileErrorCode'], 'fetch_failed');
      final shown = catalogList(catalog.routingSnapshot()['profiles']).single;
      expect(shown['geofileError'], failure.message);
      expect(shown['geofileErrorCode'], 'fetch_failed');
      expect(route['nextGeoRefreshAt'], catalog.now + delay);
      now = now.add(Duration(seconds: delay));
    }
  });

  test(
    'Windows refresh deadlines apply saved intervals to existing catalogs',
    () {
      const last = 100000;
      final data = DesktopCatalog.empty()..['settings'] = <String, Object?>{};
      final catalog = DesktopCatalog(data, (_, _) async => {}, (_, _) => {});
      final source = <String, Object?>{
        'id': 'fixture',
        'url': 'https://provider.example/feed',
        'status': 'ready',
        'lastSuccessfulFetch': last,
        'nextRefreshAt': last + 86400,
      };
      catalog.putSource(source);
      expect(catalog.nextSubscriptionRefresh(source), last + 4 * 3600);
      expect(
        catalogList(
          catalog.snapshot()['subscriptions'],
        ).single['nextRefreshAt'],
        last + 4 * 3600,
      );
      data['settings'] = const TunnelSettings(
        subscriptionAutoUpdateHours: 12,
      ).toMap();
      final reopened = catalog.copy();
      expect(reopened.settings.subscriptionAutoUpdateHours, 12);
      expect(
        reopened.nextSubscriptionRefresh(reopened.sources.single),
        last + 12 * 3600,
      );
      expect(
        reopened.nextSubscriptionRefresh({
          ...source,
          'refreshInterval': 12 * 3600,
          'nextRefreshAt': last + 44000,
        }),
        last + 44000,
        reason:
            'A successful refresh retains its jitter until the interval changes',
      );
      expect(
        reopened.nextSubscriptionRefresh({...source, 'url': null}),
        isNull,
      );
      expect(
        reopened.nextSubscriptionRefresh({
          ...source,
          'status': 'needsApproval',
        }),
        last + 86400,
        reason: 'One waiting for approval keeps its retry',
      );
      expect(
        reopened.nextSubscriptionRefresh({
          ...source,
          'refreshError': 'hwid_required',
          'nextRefreshAt': null,
        }),
        last + 12 * 3600,
        reason: 'An older device reply waits an interval',
      );
      expect(
        reopened.nextSubscriptionRefresh({
          ...source,
          'refreshError': 'refresh_failed',
          'failures': 1,
          'nextRefreshAt': last + 60,
        }),
        last + 60,
      );
    },
  );

  // A captive portal or a redirect to a private address once must not stop a
  // subscription's refreshes for good.
  test(
    'a refresh that meets a risky answer is retried in two hours, or one interval when that is shorter',
    () async {
      var now = DateTime.fromMillisecondsSinceEpoch(100000 * 1000);
      final catalog = DesktopCatalog(
        DesktopCatalog.empty(),
        (method, payload) async => {
          'warning': {
            'riskCodes': ['privateTarget'],
            'chain': ['fingerprint'],
          },
        },
        (_, _) => {},
        clock: () => now,
      );
      for (final (hours, retry) in [(24, 7200), (4, 7200), (1, 3600)]) {
        catalog.putSource({
          'id': 'source',
          'url': 'https://provider.example/feed',
          'status': 'ready',
          'format': 'source',
          'fetchOptions': {'refreshHours': '$hours'},
          'lastSuccessfulFetch': catalog.now,
          'refreshInterval': hours * 3600,
          'nextRefreshAt': catalog.now,
        });
        final warning = await catalog.refresh('source');
        expect(warning?['riskCodes'], ['privateTarget'], reason: '$hours h');
        final source = catalog.source('source');
        expect(source['status'], 'needsApproval', reason: '$hours h');
        expect(
          source['nextRefreshAt'],
          catalog.now + retry,
          reason: '$hours h',
        );
        expect(
          catalog.nextSubscriptionRefresh(source),
          catalog.now + retry,
          reason: '$hours h',
        );
        expect(
          catalogList(
            catalog.snapshot()['subscriptions'],
          ).single['nextRefreshAt'],
          catalog.now + retry,
          reason: 'The shown time is the retry',
        );
        expect(catalog.subscriptionRefreshDue(source), isFalse);
        now = now.add(Duration(seconds: retry));
        expect(catalog.subscriptionRefreshDue(source), isTrue);
      }
    },
  );

  test(
    'a subscription waiting for approval is due at its retry, and at once when an older version saved none',
    () {
      final now = DateTime.fromMillisecondsSinceEpoch(100000 * 1000);
      final catalog = DesktopCatalog(
        DesktopCatalog.empty(),
        (_, _) async => {},
        (_, _) => {},
        clock: () => now,
      );
      final waiting = <String, Object?>{
        'id': 'source',
        'url': 'https://provider.example/feed',
        'status': 'needsApproval',
        'riskCodes': ['privateTarget'],
        'lastSuccessfulFetch': catalog.now - 86400,
        'refreshInterval': 4 * 3600,
      };
      expect(catalog.subscriptionRefreshDue(waiting), isTrue, reason: 'none');
      expect(
        catalog.subscriptionRefreshDue({
          ...waiting,
          'nextRefreshAt': catalog.now - 1,
        }),
        isTrue,
        reason: 'passed',
      );
      expect(
        catalog.subscriptionRefreshDue({
          ...waiting,
          'nextRefreshAt': catalog.now + 600,
        }),
        isFalse,
        reason: 'ahead',
      );
      expect(
        catalog.nextSubscriptionRefresh({...waiting, 'url': null}),
        isNull,
        reason: 'local profiles are never fetched',
      );
    },
  );

  // A provider that wants a device registered, or has too many, is asked
  // again at the regular interval: the user may free a slot on its site.
  test(
    'a device reply waits one regular interval, not a retry delay and not for good',
    () {
      var now = DateTime.fromMillisecondsSinceEpoch(100000 * 1000);
      final catalog = DesktopCatalog(
        DesktopCatalog.empty(),
        (_, _) async => {},
        (_, _) => {},
        clock: () => now,
      );
      for (final code in ['hwid_required', 'hwid_limit']) {
        catalog.putSource({
          'id': 'source',
          'url': 'https://provider.example/feed',
          'status': 'ready',
          'fetchOptions': {'refreshHours': '6'},
          'lastSuccessfulFetch': catalog.now,
          'refreshInterval': 6 * 3600,
          'nextRefreshAt': catalog.now + 6 * 3600,
        });
        for (var attempt = 1; attempt <= 3; attempt++) {
          catalog.refreshFailed('source', code);
          final source = catalog.source('source');
          expect(source['status'], 'failed', reason: code);
          expect(source['refreshError'], code);
          expect(source['failures'], attempt);
          expect(
            source['nextRefreshAt'],
            catalog.now + 6 * 3600,
            reason: '$code, attempt $attempt',
          );
          expect(catalog.subscriptionRefreshDue(source), isFalse);
          now = now.add(const Duration(hours: 6));
          expect(catalog.subscriptionRefreshDue(source), isTrue);
        }
      }
      // Other failures keep their short retries.
      catalog.refreshFailed('source', 'fetch_failed');
      expect(catalog.source('source')['nextRefreshAt'], catalog.now + 1800);
    },
  );

  test(
    'a device reply an older version saved with no time is due an interval after the last success, or at once',
    () {
      final now = DateTime.fromMillisecondsSinceEpoch(100000 * 1000);
      final catalog = DesktopCatalog(
        DesktopCatalog.empty(),
        (_, _) async => {},
        (_, _) => {},
        clock: () => now,
      );
      final parked = <String, Object?>{
        'id': 'source',
        'url': 'https://provider.example/feed',
        'status': 'failed',
        'fetchOptions': {'refreshHours': '6'},
        'failures': 1,
        'nextRefreshAt': null,
      };
      for (final code in ['hwid_required', 'hwid_limit']) {
        final source = {...parked, 'refreshError': code};
        expect(
          catalog.nextSubscriptionRefresh(source),
          lessThanOrEqualTo(catalog.now),
        );
        expect(catalog.subscriptionRefreshDue(source), isTrue, reason: code);
        final recent = {...source, 'lastSuccessfulFetch': catalog.now - 3600};
        expect(
          catalog.nextSubscriptionRefresh(recent),
          catalog.now + 5 * 3600,
          reason: code,
        );
        expect(catalog.subscriptionRefreshDue(recent), isFalse);
        expect(
          catalog.subscriptionRefreshDue({
            ...source,
            'lastSuccessfulFetch': catalog.now - 7 * 3600,
          }),
          isTrue,
          reason: code,
        );
      }
      expect(
        catalog.nextSubscriptionRefresh({
          ...parked,
          'refreshError': 'fetch_failed',
        }),
        isNull,
        reason: 'Only the device replies were saved with no time',
      );
    },
  );
}
