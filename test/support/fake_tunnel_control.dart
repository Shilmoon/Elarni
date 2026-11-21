import 'dart:typed_data';

import 'package:elarni/tunnel_channel.dart';
import 'package:elarni/update_check.dart' show UpdateAsset;

const fixtureSubscriptions = SubscriptionSnapshot(
  selectedNodeId: 'a',
  subscriptions: [
    SubscriptionGroup(
      id: 'group',
      name: 'Fixture',
      status: 'ready',
      nodes: [
        SubscriptionNode(
          id: 'a',
          name: 'Node A',
          protocol: 'VLESS',
          transport: 'tcp',
          security: 'tls',
          selected: true,
        ),
        SubscriptionNode(
          id: 'b',
          name: 'Node B',
          protocol: 'VLESS',
          transport: 'tcp',
          security: 'tls',
          selected: false,
        ),
      ],
    ),
  ],
);

class FakeTunnelControl implements TunnelControl {
  @override
  Future<SubscriptionSnapshot> setNodeFavorite(
    String nodeId,
    bool favorite,
  ) async => snapshot;
  final renamedNodes = <(String, String)>[];
  final deletedNodes = <String>[];
  @override
  Future<SubscriptionSnapshot> renameNode(String nodeId, String name) async {
    renamedNodes.add((nodeId, name));
    return snapshot;
  }

  @override
  Future<SubscriptionSnapshot> deleteNode(String nodeId) async {
    deletedNodes.add(nodeId);
    return snapshot;
  }

  final deletedBatches = <List<String>>[];
  @override
  Future<SubscriptionSnapshot> deleteNodes(List<String> nodeIds) async {
    deletedBatches.add(nodeIds);
    return snapshot;
  }

  final shownNodes = <String>[];
  @override
  Future<SubscriptionSnapshot> showNode(String nodeId) async {
    shownNodes.add(nodeId);
    return snapshot;
  }

  String deviceID =
      '3f9a12c8d0e1f2a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8a9c21e';
  final resetDeviceIDs = <String>[];
  @override
  Future<String> subscriptionDeviceID(String id) async {
    deviceIDLookups++;
    if (deviceIDFails) throw StateError('device ID fixture');
    return deviceID;
  }

  bool deviceIDFails = false;
  int deviceIDLookups = 0;

  final checkedRules = <List<String>>[];
  ProcessRuleChecks ruleChecks = const ProcessRuleChecks({});
  @override
  Future<ProcessRuleChecks> checkProcessRules(List<String> rules) async {
    checkedRules.add(rules);
    return ruleChecks;
  }

  @override
  Future<SubscriptionSnapshot> resetSubscriptionDeviceID(String id) async {
    resetDeviceIDs.add(id);
    deviceID = '${resetDeviceIDs.length}'.padLeft(64, '0');
    return snapshot;
  }

  @override
  void setImportListener(void Function(String? error)? listener) {
    importListener = listener;
  }

  void Function(String? error)? importListener;
  SubscriptionSnapshot snapshot = fixtureSubscriptions;
  TunnelSettings settings = const TunnelSettings();
  String phase = 'disconnected';
  @override
  String? liveness;
  int connectCalls = 0;
  int disconnectCalls = 0;
  int settingsReads = 0;
  int metricsReads = 0;
  final addedURLs = <String>[];
  final challenges = <String?>[];
  final selectedIDs = <String>[];
  final telegramIDs = <String>[];
  final importedProfiles = <String?>[];
  final importedSources = <String>[];
  RoutingSnapshot routingSnapshot = const RoutingSnapshot(
    profiles: [],
    assignments: {},
  );
  final routingActions = <String>[];
  int routingReads = 0;
  Future<SubscriptionOperationResult> Function(String, String?)? onImport;
  final sharedIDs = <String>[];
  // Whether each share asked for the subscription link or the server's.
  final sharedSubscription = <bool>[];
  final savedFetchOptions = <Map<String, Object?>>[];
  final exportedPasswords = <String>[];
  final restoredPasswords = <String>[];
  int repairCalls = 0;
  Map<String, Object?> automation = {
    'enabled': false,
    'policySupported': true,
    'status': 'disabled',
  };
  final List<AutomationConfiguration> recoverySaves = [];
  int diagnosticExports = 0;
  List<String> exportedIssueCodes = [];
  Future<String> Function()? onState;
  @override
  Future<Map<String, Object?>> configureAutomation(
    AutomationConfiguration configuration,
  ) async {
    recoverySaves.add(configuration);
    automation = {
      ...automation,
      'enabled': configuration.enabled,
      'policy': configuration.policy.name,
      'configuration': configuration.toMap(),
    };
    return automation;
  }

  @override
  Future<Map<String, Object?>> automationStatus() async => automation;
  @override
  Future<Map<String, Object?>> exportEvidence({
    List<String> issueCodes = const [],
  }) async {
    diagnosticExports++;
    exportedIssueCodes = List.of(issueCodes);
    return {};
  }

  int recordingMarks = 0;
  int recordingExports = 0;
  Object? recordingExportError;
  @override
  Future<void> markRecording() async => recordingMarks++;

  @override
  Future<void> exportRecording() async {
    recordingExports++;
    if (recordingExportError case final Object error) throw error;
  }

  @override
  Future<void> exportCatalogBackup(String password) async {
    exportedPasswords.add(password);
  }

  @override
  Future<SubscriptionSnapshot> restoreCatalogBackup(String password) async {
    restoredPasswords.add(password);
    return snapshot;
  }

  /// The desktop core's storage replies: by default a catalog that loaded.
  Map<String, Object?> storageStatusValue = const {
    'code': null,
    'resettable': false,
  };
  int storageStatusCalls = 0;
  int resetStorageCalls = 0;
  Object? resetStorageError;

  @override
  Future<Map<String, Object?>> storageStatus() async {
    storageStatusCalls++;
    return storageStatusValue;
  }

  @override
  Future<Map<String, Object?>> resetStorage() async {
    resetStorageCalls++;
    if (resetStorageError case final Object error) throw error;
    storageStatusValue = const {'code': null, 'resettable': false};
    return {'setAside': true, 'name': null, 'state': phase};
  }

  @override
  Future<void> repairTunnel() async {
    repairCalls++;
    phase = 'disconnected';
  }

  @override
  Future<SubscriptionSnapshot> updateSubscriptionFetchOptions(
    String id,
    Map<String, Object?> options,
  ) async {
    savedFetchOptions.add({'id': id, ...options});
    return snapshot;
  }

  String? sharingPNG;
  @override
  Future<Map<String, Object?>> shareClient(
    String id, {
    bool subscription = false,
  }) async {
    sharedIDs.add(id);
    sharedSubscription.add(subscription);
    return {
      'text': 'https://sub.example.com/test-vector',
      if (sharingPNG != null) 'png': sharingPNG,
    };
  }

  // The texts given a QR code, and the reply: an error is thrown.
  final qrTexts = <String>[];
  Object? qrReply;
  @override
  Future<Map<String, Object?>> qrCode(String text) async {
    qrTexts.add(text);
    return switch (qrReply) {
      final String png => {'png': png},
      null => {},
      final Object error => throw error,
    };
  }

  // Each shareLinks call's group and servers; the reply, or an error thrown.
  final sharedLinks = <(String, List<String>)>[];
  String shareLinksText = 'socks://fixture@127.0.0.1:1080#Fixture';
  Object? shareLinksError;
  @override
  Future<String> shareLinks(String subscriptionId, List<String> nodeIds) async {
    sharedLinks.add((subscriptionId, nodeIds));
    if (shareLinksError case final error?) throw error;
    return shareLinksText;
  }

  String nodeConfigText = '{\n  "outbounds": []\n}';
  Object? nodeConfigError;
  final nodeConfigEdits = <(String, String)>[];
  @override
  Future<String> nodeConfig(String nodeId) async => nodeConfigText;

  @override
  Future<SubscriptionSnapshot> updateNodeConfig(
    String nodeId,
    String content,
  ) async {
    nodeConfigEdits.add((nodeId, content));
    if (nodeConfigError case final error?) throw error;
    return snapshot;
  }

  Object? importError;
  Future<String> Function()? onConnect;
  Future<SubscriptionOperationResult> Function()? onAdd;
  Future<SubscriptionOperationResult> Function(String?)? onRefresh;
  Future<NodeSelectionResult> Function()? onSelect;
  Future<TunnelSettings> Function(TunnelSettings)? onUpdate;
  Future<TunnelSettings> Function()? onSettings;
  Future<PingResult> Function(String)? onPing;
  final pingCalls =
      <
        ({
          String nodeId,
          String protocol,
          int expectedStatus,
          String url,
          bool trace,
        })
      >[];

  @override
  Future<PingResult> pingNode(
    String nodeId, {
    required String protocol,
    required String method,
    required String mode,
    required int timeout,
    required String url,
    int expectedStatus = 204,
    String expectedBody = '',
    bool trace = false,
  }) {
    pingCalls.add((
      nodeId: nodeId,
      protocol: protocol,
      expectedStatus: expectedStatus,
      url: url,
      trace: trace,
    ));
    return onPing?.call(nodeId) ??
        Future.error(StateError('Synthetic endpoint unavailable'));
  }

  @override
  Future<String> state() async => onState == null ? phase : await onState!();
  int subscriptionReads = 0;
  @override
  Future<SubscriptionSnapshot> subscriptions() async {
    subscriptionReads++;
    return snapshot;
  }

  @override
  Future<RoutingSnapshot> routingProfiles() async {
    routingReads++;
    return routingSnapshot;
  }

  @override
  Future<RoutingSnapshot> setRoutingProfileEnabled(
    String id,
    bool enabled,
  ) async {
    routingActions.add('enable:$id:$enabled');
    return routingSnapshot;
  }

  @override
  Future<RoutingSnapshot> updateRoutingProfile(
    String id,
    String content,
  ) async {
    routingActions.add('update:$id');
    return routingSnapshot;
  }

  @override
  Future<RoutingSnapshot> duplicateRoutingProfile(String id) async {
    routingActions.add('duplicate:$id');
    return routingSnapshot;
  }

  @override
  Future<RoutingSnapshot> resetRoutingProfile(String id) async {
    routingActions.add('reset:$id');
    return routingSnapshot;
  }

  @override
  Future<RoutingSnapshot> deleteRoutingProfile(String id) async {
    routingActions.add('delete:$id');
    return routingSnapshot;
  }

  @override
  Future<RoutingSnapshot> refreshRoutingGeofiles(
    String id, {
    String? file,
  }) async {
    routingActions.add('refresh:$id:${file ?? 'all'}');
    return routingSnapshot;
  }

  Object? managedRefreshError;
  @override
  Future<RoutingSnapshot> refreshManagedLists(
    String id, {
    String? sourceId,
  }) async {
    routingActions.add('lists:$id:${sourceId ?? 'all'}');
    if (managedRefreshError case final error?) throw error;
    return routingSnapshot;
  }

  Map<String, Object?> systemInfoValue = {};
  @override
  Future<Map<String, Object?>> systemInfo() async => systemInfoValue;
  final vpnServiceChanges = <bool>[];
  Object? vpnServiceError;
  @override
  Future<Map<String, Object?>> setVPNService(bool enabled) async {
    vpnServiceChanges.add(enabled);
    if (vpnServiceError case final error?) throw error;
    return {'vpnService': enabled};
  }

  @override
  Future<TunnelSettings> tunnelSettings() async {
    settingsReads++;
    return onSettings?.call() ?? settings;
  }

  /// What runtimeMetrics returns; nothing when null.
  Map<String, Object?> Function()? onMetrics;
  @override
  Future<Map<String, Object?>> runtimeMetrics() async {
    metricsReads++;
    return onMetrics?.call() ?? {};
  }

  /// The core's list: entries with a seq after the read's are returned.
  int connectionsEpoch = 0;
  final connections = <Map<String, Object?>>[];
  final connectionReads = <(int, int)>[];
  @override
  Future<RecentConnections> readConnections({
    int epoch = 0,
    int after = 0,
  }) async {
    connectionReads.add((epoch, after));
    final since = epoch == connectionsEpoch ? after : 0;
    final entries = [
      for (final entry in connections)
        if ((entry['seq']! as int) > since) entry,
    ];
    return RecentConnections.fromMap({
      'epoch': connectionsEpoch,
      'seq': connections.fold<int>(
        0,
        (seq, entry) => entry['seq']! as int > seq ? entry['seq']! as int : seq,
      ),
      'connections': entries,
    });
  }

  final logLines = <Map<String, Object?>>[];
  final logReads = <int>[];
  final savedLogs = <String>[];
  @override
  Future<Map<String, Object?>> readLogs({int after = 0}) async {
    logReads.add(after);
    return {
      'level': settings.logLevel,
      'entries': [
        for (final line in logLines)
          if ((line['seq']! as int) > after) line,
      ],
    };
  }

  @override
  Future<void> saveLogs(String text) async => savedLogs.add(text);

  // Each saved file's kind and bytes; false answers as a cancelled panel.
  final savedFiles = <(String, Uint8List)>[];
  Future<bool> Function(String kind)? onSaveFile;
  @override
  Future<bool> saveFile(String kind, Uint8List bytes) async {
    savedFiles.add((kind, bytes));
    return onSaveFile?.call(kind) ?? Future.value(true);
  }

  @override
  Future<String> connect() {
    connectCalls++;
    return onConnect?.call() ?? Future.value(phase = 'connected');
  }

  @override
  Future<String> disconnect() async {
    disconnectCalls++;
    return phase = 'disconnected';
  }

  @override
  Future<SubscriptionOperationResult> addSubscription(String url) {
    addedURLs.add(url);
    return onAdd?.call() ??
        Future.value(SubscriptionOperationResult(snapshot: snapshot));
  }

  @override
  Future<SubscriptionOperationResult> refreshSubscription(
    String id, {
    String? challenge,
    String? format,
  }) {
    challenges.add(challenge);
    return onRefresh?.call(challenge) ??
        Future.value(SubscriptionOperationResult(snapshot: snapshot));
  }

  @override
  Future<void> openProviderLink(String id, String action) async {}

  final openedUpdateLinks = <String>[];
  @override
  Future<void> openUpdateLink(String url) async => openedUpdateLinks.add(url);

  /// The core's update replies; by default an install that cannot replace itself.
  Map<String, Object?> updateStatusValue = const {'kind': ''};
  Object? updateStatusError;
  int updateStatusCalls = 0;
  Map<String, Object?> downloadUpdateValue = const {};
  Object? downloadUpdateError;
  final downloadedAssets = <List<UpdateAsset>>[];

  /// Runs before a download or install answers, so a test can hold it or
  /// read the preferences saved at that moment.
  Future<void> Function()? onDownloadUpdate, onInstallUpdate;
  Map<String, Object?> installUpdateValue = const {'exit': true};
  Object? installUpdateError;
  final installRelaunches = <bool>[];

  @override
  Future<Map<String, Object?>> updateStatus() async {
    updateStatusCalls++;
    if (updateStatusError case final Object error) throw error;
    return updateStatusValue;
  }

  @override
  Future<Map<String, Object?>> downloadUpdate(List<UpdateAsset> assets) async {
    downloadedAssets.add(assets);
    await onDownloadUpdate?.call();
    if (downloadUpdateError case final Object error) throw error;
    return downloadUpdateValue;
  }

  @override
  Future<Map<String, Object?>> installUpdate({required bool relaunch}) async {
    installRelaunches.add(relaunch);
    await onInstallUpdate?.call();
    if (installUpdateError case final Object error) throw error;
    return installUpdateValue;
  }

  @override
  Future<void> openTelegramProxy(String id) async {
    telegramIDs.add(id);
  }

  @override
  Future<SubscriptionOperationResult> importClientProfiles({
    String source = 'text',
    String? content,
  }) async {
    if (importError case final Object error) throw error;
    importedProfiles.add(content);
    importedSources.add(source);
    if (onImport != null) return onImport!(source, content);
    return SubscriptionOperationResult(snapshot: snapshot);
  }

  @override
  Future<SubscriptionOperationResult> setNodeIPPreference(
    String nodeID,
    String? preference,
  ) async {
    nodeIPPreferences.add((nodeID, preference));
    return SubscriptionOperationResult(snapshot: snapshot);
  }

  final List<(String, String?)> nodeIPPreferences = [];

  @override
  Future<SubscriptionOperationResult> selectProfileGroup(
    String nodeID,
    String group,
    String choice,
  ) async {
    groupChoices.add([nodeID, group, choice]);
    return SubscriptionOperationResult(snapshot: snapshot);
  }

  final List<List<String>> groupChoices = [];

  @override
  Future<NodeSelectionResult> selectNode(String id) {
    selectedIDs.add(id);
    return onSelect?.call() ??
        Future.value(
          NodeSelectionResult(state: phase, subscriptions: snapshot),
        );
  }

  @override
  Future<TunnelSettings> updateTunnelSettings(TunnelSettings value) =>
      onUpdate?.call(value) ?? Future.value(settings = value);

  Map<String, String> inboundLogin = {};
  @override
  Future<Map<String, String>> inboundCredentials() async => inboundLogin;
  @override
  Future<TunnelSettings> updateInboundCredentials(
    String username,
    String password,
  ) async {
    inboundLogin = {'username': username, 'password': password};
    return settings = TunnelSettings.fromMap({
      ...settings.toMap(),
      'inboundAuthEnabled': true,
      'inboundCredentialsConfigured': true,
    });
  }

  // An unexpected native operation must fail a test instead of performing real work.
  @override
  dynamic noSuchMethod(Invocation invocation) =>
      throw UnimplementedError('${invocation.memberName}');
}
