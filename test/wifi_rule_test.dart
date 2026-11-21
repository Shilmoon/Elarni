import 'dart:async';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:elarni/desktop_catalog.dart';
import 'package:elarni/desktop_proxy.dart';
import 'package:elarni/tunnel_channel.dart';
import 'package:elarni/wifi_network.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  Future<Process> noCore(String _) =>
      Future.error(const ProcessException('unused-core', []));

  // A selected server whose core never starts: a Connect shows as the
  // connection intent it saves before starting one. Saving settings needs the
  // native library, so they start stored; windows_proxy_integration saves them.
  Future<({DesktopProxy proxy, List<bool> intents})> start(
    WiFiReading Function() network, {
    TunnelSettings settings = const TunnelSettings(
      connectOnUntrustedWiFi: true,
      trustedWiFiNetworks: ['Home'],
    ),
    bool selected = true,
  }) async {
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
    if (selected) catalog.data['selectedNodeId'] = 'node';
    catalog.data['settings'] = settings.toMap();
    final intents = <bool>[];
    final proxy = DesktopProxy(
      corePath: 'unused-core',
      domainPath: 'unused-domain',
      startProcess: noCore,
      catalogIO: (method, payload) async {
        if (method == 'loadCatalog')
          return {
            'revision': '1',
            'catalog': {'version': 1, 'committed': copyCatalog(catalog.data)},
          };
        if (method == 'saveCatalog')
          intents.add(
            catalogMap(
                  catalogMap(payload['catalog'])['committed'],
                )['connectionIntent'] ==
                true,
          );
        return {'revision': '2'};
      },
      platformIO: (_, _) async => {},
      wifiNetwork: () async => network(),
      wifiInterval: const Duration(milliseconds: 20),
    );
    addTearDown(proxy.dispose);
    await proxy.request('state');
    return (proxy: proxy, intents: intents);
  }

  Future<void> settle() =>
      Future<void>.delayed(const Duration(milliseconds: 200));
  Future<void> until(bool Function() condition) async {
    for (var i = 0; i < 200 && !condition(); i++) {
      await Future<void>.delayed(const Duration(milliseconds: 20));
    }
    expect(condition(), isTrue);
  }

  test('joining an untrusted Wi-Fi network connects once per join', () async {
    WiFiReading network = (name: 'Home', denied: false);
    final (:proxy, :intents) = await start(() => network);
    await settle();
    expect(intents, isNot(contains(true)), reason: 'Home is trusted');
    network = (name: 'Cafe', denied: false);
    await until(() => intents.isNotEmpty && intents.last);
    await proxy.request('disconnect');
    expect(intents.last, isFalse);
    await settle();
    expect(intents.last, isFalse, reason: 'Stop holds until the next network');
    network = (name: 'Home', denied: false);
    await settle();
    expect(intents.last, isFalse, reason: 'Trusted networks stay as they are');
    // Wired and mobile connections have no Wi-Fi name.
    network = (name: null, denied: false);
    await settle();
    expect(intents.last, isFalse);
    network = (name: 'Cafe', denied: false);
    await until(() => intents.last);
  });

  test('the rule is off unless the user turns it on', () async {
    var reads = 0;
    final (proxy: _, :intents) = await start(() {
      reads++;
      return (name: 'Cafe', denied: false);
    }, settings: const TunnelSettings());
    await settle();
    expect(intents, isNot(contains(true)));
    // Windows may ask for location access on the first read.
    expect(reads, 0, reason: 'Off, Elarni never reads the Wi-Fi name');
  });

  test(
    'a Wi-Fi name the system keeps from Elarni is reported and untrusted',
    () async {
      var reads = 0;
      final (:proxy, :intents) = await start(() {
        reads++;
        return (name: null, denied: true);
      });
      var status = <String, Object?>{};
      // Windows asks for Location access; Linux needs nmcli or iw.
      await until(() {
        unawaited(proxy.request('automationStatus').then((s) => status = s));
        return status['wifi'] ==
            (Platform.isLinux ? 'nameUnreadable' : 'locationDenied');
      });
      // Unread, the network is not on the trusted list.
      await until(() => intents.isNotEmpty && intents.last);
      await proxy.request('disconnect');
      expect(intents.last, isFalse);
      final connects = intents.where((intent) => intent).length;
      final read = reads;
      await until(() => reads >= read + 2);
      expect(
        intents.where((intent) => intent).length,
        connects,
        reason: 'Readings on the same addresses are the same join',
      );
      expect(
        intents.last,
        isFalse,
        reason: 'Stop holds until the next network',
      );
    },
  );

  test('an unreadable Wi-Fi name without a selected server stays', () async {
    var reads = 0;
    final (proxy: _, :intents) = await start(() {
      reads++;
      return (name: null, denied: true);
    }, selected: false);
    await until(() => reads >= 2);
    expect(intents, isNot(contains(true)));
  });

  test('trusted Wi-Fi names keep to Wi-Fi limits', () {
    bool valid(List<String> names) =>
        TunnelSettings(trustedWiFiNetworks: names).validTrustedWiFi;
    for (final names in [
      ['x' * 33],
      ['\u00e9' * 17],
      ['tab\there'],
      [''],
      [for (var i = 0; i < 65; i++) 'n$i'],
    ]) {
      expect(valid(names), isFalse, reason: '$names');
    }
    expect(
      valid(['x' * 32, 'Caf\u00e9 5G', for (var i = 0; i < 62; i++) 'n$i']),
      isTrue,
    );
    final saved = TunnelSettings.fromMap(
      const TunnelSettings(
        connectOnUntrustedWiFi: true,
        trustedWiFiNetworks: ['Home'],
      ).toMap(),
    );
    expect(saved.connectOnUntrustedWiFi, isTrue);
    expect(saved.trustedWiFiNetworks, ['Home']);
  });

  test('Linux reads the Wi-Fi name from nmcli or iw', () {
    expect(nmcliWiFi('no:Neighbour\nyes:Caf\\:e \\\\ 5G\n'), 'Caf:e \\ 5G');
    expect(nmcliWiFi('no:Neighbour\n'), isNull);
    expect(
      iwWiFi(
        'Connected to 00:11:22:33:44:55 (on wlan0)\n'
        '\tSSID: Caf\\xc3\\xa9 net\n\tfreq: 2412\n',
      ),
      'Caf\u00e9 net',
    );
    expect(iwWiFi('Not connected.\n'), isNull);
  });

  group('the Linux Wi-Fi reader', () {
    late Directory sysfs;
    setUp(() => sysfs = Directory.systemTemp.createTempSync('elarni-sysfs'));
    tearDown(() => sysfs.deleteSync(recursive: true));
    // A network interface as /sys/class/net shows it.
    void link(String name, {String state = 'up', String? wireless}) {
      final folder = Directory('${sysfs.path}/$name')..createSync();
      if (wireless != null) Directory('${folder.path}/$wireless').createSync();
      File('${folder.path}/operstate').writeAsStringSync('$state\n');
    }

    ProcessResult ok(String output) => ProcessResult(0, 0, output, '');
    const devices = 'phy#0\n\tInterface wlan0\n\t\ttype managed\n';
    ProcessResult iwCafe(List<String> arguments) => ok(
      arguments.length == 1
          ? devices
          : 'Connected to 00:11:22:33:44:55 (on wlan0)\n\tSSID: Cafe\n',
    );
    // Programs missing from the map are not installed, as Process.run throws.
    Future<WiFiReading> read(
      Map<String, ProcessResult Function(List<String>)> programs, [
      List<String>? calls,
    ]) => linuxWiFi(
      run: (program, arguments, {environment}) async {
        calls?.add([program, ...arguments].join(' '));
        final answer = programs[program];
        if (answer == null) throw ProcessException(program, arguments);
        return answer(arguments);
      },
      sysfs: sysfs.path,
    );

    test('takes the active network from nmcli', () async {
      final calls = <String>[];
      expect(
        await read({'nmcli': (_) => ok('no:Neighbour\nyes:Home\n')}, calls),
        (name: 'Home', denied: false),
      );
      expect(calls.single, startsWith('nmcli '));
    });

    test('asks iw when nmcli lists no active network', () async {
      link('wlan0', wireless: 'wireless');
      expect(await read({'nmcli': (_) => ok('no:Neighbour\n'), 'iw': iwCafe}), (
        name: 'Cafe',
        denied: false,
      ));
    });

    test('finds iw in sbin when PATH lacks it', () async {
      final calls = <String>[];
      expect(await read({'/usr/sbin/iw': iwCafe}, calls), (
        name: 'Cafe',
        denied: false,
      ));
      expect(calls, contains('/usr/sbin/iw dev'));
      expect(await read({'/sbin/iw': iwCafe}), (name: 'Cafe', denied: false));
    });

    test('a wireless link that is up without a name is unreadable', () async {
      link('eth0');
      link('wlan0', wireless: 'wireless');
      final unreadable = (name: null, denied: true);
      expect(await read({'nmcli': (_) => ok('no:Neighbour\n')}), unreadable);
      expect(
        await read({'iw': (_) => ok(devices)}),
        unreadable,
        reason: 'iw without a link',
      );
      Directory('${sysfs.path}/wlan0').deleteSync(recursive: true);
      link('wlp2s0', wireless: 'phy80211');
      expect(await read({}), unreadable);
    });

    test('without a wireless link that is up it is off Wi-Fi', () async {
      const off = (name: null, denied: false);
      expect(await read({}), off, reason: 'No interfaces');
      link('eth0');
      link('wlan0', state: 'down', wireless: 'wireless');
      link('wlan1', state: 'dormant', wireless: 'phy80211');
      expect(await read({'nmcli': (_) => ok('no:Neighbour\n')}), off);
      sysfs.deleteSync(recursive: true);
      expect(await read({}), off, reason: 'No sysfs');
      sysfs.createSync();
    });
  });

  test(
    "this desktop's Wi-Fi reader answers",
    () async {
      final reading = await readWiFiNetwork();
      expect(reading.name == null || reading.name!.isNotEmpty, isTrue);
    },
    skip: !Platform.isWindows && !Platform.isLinux,
  );
}
