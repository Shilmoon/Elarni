import 'dart:async';
import 'dart:convert';
import 'package:flutter/foundation.dart' show kReleaseMode;
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'tunnel_channel.dart';
import 'update_check.dart';
import 'strings.dart';

/// Where an update of this install stands. [DesktopIntegration.updateVersion]
/// names the version it refers to.
enum UpdateStage { none, downloading, ready, installing, installed, failed }

// Only desktop preferences, reminder date and opaque catalog IDs live here. Profiles stay in the
// platform's encrypted catalog; desktop controls call the existing tunnel owner.
final class DesktopIntegration extends ChangeNotifier {
  static const channel = MethodChannel('com.elarni/desktop');
  static const accents = <String, Color>{
    'evergreen': Color(0xff284d42),
    'blue': Color(0xff006ef0),
    'teal': Color(0xff006b60),
    'purple': Color(0xff5546e8),
    'orange': Color(0xff8c5000),
  };
  static final languages = [
    'system',
    for (final locale in AppLocalizations.supportedLocales) locale.languageCode,
  ];

  /// The choices Settings > Server tests offers; a stored value outside them
  /// is replaced by the default.
  static const pingProtocols = ['proxy', 'tcp', 'icmp'],
      pingMethods = ['GET', 'HEAD'],
      pingModes = ['default', 'double', 'keepalive'],
      pingTimeouts = [5, 7, 9, 12, 15];
  static const defaultPingURL = 'https://www.gstatic.com/generate_204';

  /// The longest test URL and expected body kept; the host stores 32 KB in all.
  static const maxPingText = 2048;
  ThemeMode theme = ThemeMode.system;
  // 'system' or the language code of one of the app's locales.
  String language = 'system';
  String accent = 'evergreen', sort = 'provider';
  Set<String> pinned = {};

  /// The Home groups the user closed; every other group is open. A
  /// subscription's id, `<id>:<country>` for a country in it and `:<country>`
  /// for a country in the By country list.
  Set<String> closedGroups = {};
  static const maxClosedGroups = 256;
  static final _closedGroup = RegExp(
    r'^(?=.)[A-Za-z0-9_-]{0,128}(:[A-Za-z]{2,5})?$',
  );
  bool expiryReminders = false, _notifying = false;
  bool favoritesFirst = true;
  int lastExpiryReminderDay = 0;
  bool loginWarningDismissed = false;
  // An empty feed keeps the update check off; Not now stores the version it hid.
  String updateFeedURL = '', dismissedUpdate = '';
  // The latency test, as Settings > Server tests last left it.
  String pingProtocol = 'tcp', pingMethod = 'GET', pingMode = 'default';
  int pingTimeout = 7, pingExpectedStatus = 204;
  String pingURL = defaultPingURL, pingExpectedBody = '';
  AvailableUpdate? availableUpdate;

  /// Downloads a newer release by itself, and installs it where nothing has
  /// to be approved.
  bool autoUpdate = true;

  /// Connects once when the app starts after an update that interrupted the VPN.
  bool reconnectAfterUpdate = false;

  /// The per-app routing mode that leaving VPN mode turned off, which the
  /// return to VPN mode turns on again; '' when there is none. The tunnel
  /// settings cannot keep it: per-app routing is valid only in VPN mode.
  String pausedPerAppMode = '';

  /// The waiting version a start last installed, so an installer that dies
  /// cannot make every start exit again.
  String updateAttempted = '';

  /// The core that replaces this install; null where Elarni does not replace
  /// itself (macOS), which keeps only the release page link.
  TunnelControl? updateControl;

  /// Ends the app; with [restart], starts it again once this process is gone.
  Future<void> Function({required bool restart})? onQuit;
  UpdateStage updateStage = UpdateStage.none;
  String? updateVersion;

  /// How this install replaces itself; null until the core answered, or when
  /// it cannot (a portable folder).
  ({String kind, bool silent})? updateSupport;
  List<UpdateAsset> _updateAssets = const [];
  // Versions whose download was refused or whose install failed: this run
  // offers only their release page.
  final _refusedUpdates = <String>{};
  Future<void>? _updateStatus;
  bool _downloadingUpdate = false;
  // When the automatic download may run again after one the network cut.
  DateTime? _downloadRetry;

  /// The last check of this feed and version, and whether it read the feed.
  ({DateTime at, bool read})? updateChecked;
  String? _appVersion, _updateCheckedFor;
  bool _checkingUpdate = false;
  bool available = false, saving = false, _closed = false;
  // Completes when the save in progress ends, so the update logic's own saves
  // wait for it instead of failing.
  Completer<void>? _saved;
  // A connect after an update waits until both the preferences loaded and a
  // page took the actions.
  bool _loaded = false, _connectPending = false;

  /// The native host answered, even if its cosmetic preferences were unusable.
  bool hostAvailable = false;
  String? error;
  ({
    String state,
    bool canConnect,
    bool canDisconnect,
    String language,
  })? _status,
      _desiredStatus;
  bool _statusSending = false;
  Timer? _statusRetry;
  int _statusRetrySeconds = 1;
  static String get _statusError => l10n.desktopStatusNotUpdated;
  void Function(String)? _onAction;
  void Function(String)? get onAction => _onAction;
  set onAction(void Function(String)? value) {
    _onAction = value;
    _deliverConnect();
  }

  void _deliverConnect() {
    final action = _onAction;
    if (_closed || !_loaded || !_connectPending || action == null) return;
    _connectPending = false;
    action('connect');
  }

  /// Whether the core can download and install the release the feed names.
  bool get updateInstallable {
    final found = availableUpdate;
    return updateControl != null &&
        updateSupport != null &&
        found != null &&
        _updateAssets.isNotEmpty &&
        updateStage != UpdateStage.failed &&
        !_refusedUpdates.contains(found.version);
  }

  Future<void> load() async {
    channel.setMethodCallHandler((call) async {
      if (!_closed &&
          call.method == 'action' &&
          const [
            'connect',
            'disconnect',
            'settings',
          ].contains(call.arguments)) {
        onAction?.call(call.arguments as String);
      }
    });
    try {
      final raw = await channel.invokeMethod<String>('getPreferences');
      hostAvailable = true;
      if (_closed) return;
      if (raw == null || raw.length > 64 * 1024) throw const FormatException();
      final value = Map<String, Object?>.from(jsonDecode(raw) as Map);
      final nextTheme = ThemeMode.values.byName(
        value['theme'] as String? ?? 'system',
      );
      final nextAccent = value['accent'] as String? ?? 'evergreen';
      final nextLanguage = value['language'] as String? ?? 'system';
      final nextSort = value['sort'] as String? ?? 'provider';
      final nextPins = (value['pinned'] as List? ?? []).cast<String>().toSet();
      final nextFeed =
          value['updateFeedURL'] as String? ??
          (kReleaseMode ? elarniReleasesFeed : '');
      final nextDismissed = value['dismissedUpdate'] as String? ?? '';
      final nextAttempted = value['updateAttempted'] as String? ?? '';
      if ((value['version'] ?? 1) != 1 ||
          !accents.containsKey(nextAccent) ||
          !languages.contains(nextLanguage) ||
          !['provider', 'alphabet', 'latency'].contains(nextSort) ||
          nextPins.length > 1024 ||
          nextPins.any(
            (id) => !RegExp(r'^[A-Za-z0-9_-]{1,128}$').hasMatch(id),
          ) ||
          (nextFeed.isNotEmpty && !isHttpsURL(nextFeed)) ||
          nextDismissed.length > 64 ||
          nextAttempted.length > 64)
        throw const FormatException();
      theme = nextTheme;
      accent = nextAccent;
      language = nextLanguage;
      sort = nextSort;
      pinned = nextPins;
      // Unreadable entries are dropped: their groups open again.
      closedGroups = {
        if (value['closedGroups'] case final List stored)
          for (final key in stored.take(maxClosedGroups))
            if (key is String && _closedGroup.hasMatch(key)) key,
      };
      expiryReminders = value['expiryReminders'] == true;
      favoritesFirst = value['favoritesFirst'] != false;
      lastExpiryReminderDay = value['lastExpiryReminderDay'] as int? ?? 0;
      loginWarningDismissed = value['loginWarningDismissed'] == true;
      updateFeedURL = nextFeed;
      dismissedUpdate = nextDismissed;
      // Test settings an older or another version left unreadable keep their
      // defaults; they do not make the other preferences unusable.
      T choice<T>(String key, List<T> choices, T fallback) =>
          switch (value[key]) {
            final T stored when choices.contains(stored) => stored,
            _ => fallback,
          };
      String text(String key, String fallback) => switch (value[key]) {
        final String stored when stored.length <= maxPingText => stored,
        _ => fallback,
      };
      // "Current rules / active tunnel" is no longer a choice: it tested the
      // selected server as Proxy does, and no other server.
      pingProtocol = value['pingProtocol'] == 'activeProxy'
          ? 'proxy'
          : choice('pingProtocol', pingProtocols, 'tcp');
      pingMethod = choice('pingMethod', pingMethods, 'GET');
      pingMode = choice('pingMode', pingModes, 'default');
      pingTimeout = choice('pingTimeout', pingTimeouts, 7);
      pingExpectedStatus = switch (value['pingExpectedStatus']) {
        final int status when status >= 200 && status <= 299 => status,
        _ => 204,
      };
      pingURL = text('pingURL', defaultPingURL);
      pingExpectedBody = text('pingExpectedBody', '');
      pausedPerAppMode = choice('pausedPerAppMode', const [
        '',
        'bypass',
        'proxyOnly',
      ], '');
      autoUpdate = value['autoUpdate'] != false;
      // Asked for once: the app connects at this start only.
      _connectPending = value['reconnectAfterUpdate'] == true;
      updateAttempted = nextAttempted;
      available = true;
    } on MissingPluginException {
      // Native desktop integration is absent in the headless widget host.
    } on Object {
      hostAvailable = true;
      if (!_closed) error = l10n.appPreferencesNotLoaded;
    }
    if (!_closed) notifyListeners();
    if (_connectPending) await _store(reconnectAfterUpdate: false);
    _loaded = true;
    _deliverConnect();
    unawaited(checkForUpdate());
  }

  /// Remembers the closed Home groups; waits for a save in progress and never
  /// throws, as a group opens and closes whether or not that is stored.
  Future<void> rememberClosedGroups(Set<String> groups) async {
    while (saving && !_closed) await _saved?.future;
    if (_closed || !available) return;
    try {
      await update(closedGroups: groups.take(maxClosedGroups).toSet());
    } on Object {
      /* update() publishes its error. */
    }
  }

  // Saves for the update logic: they wait for a save in progress and never
  // throw; a failure keeps the error update() published.
  Future<bool> _store({
    bool? reconnectAfterUpdate,
    String? updateAttempted,
    String? pausedPerAppMode,
  }) async {
    while (saving && !_closed) await _saved?.future;
    if (_closed || !available) return false;
    try {
      await update(
        reconnectAfterUpdate: reconnectAfterUpdate,
        updateAttempted: updateAttempted,
        pausedPerAppMode: pausedPerAppMode,
      );
      return true;
    } on Object {
      return false;
    }
  }

  /// Keeps [mode] as [pausedPerAppMode], '' to forget it. Like the update
  /// logic's saves, it waits for a save in progress and never throws.
  Future<bool> rememberPerAppMode(String mode) async =>
      mode == pausedPerAppMode || await _store(pausedPerAppMode: mode);

  Future<void> update({
    ThemeMode? theme,
    String? accent,
    String? language,
    String? sort,
    Set<String>? pinned,
    Set<String>? closedGroups,
    bool? expiryReminders,
    bool? favoritesFirst,
    int? lastExpiryReminderDay,
    bool? loginWarningDismissed,
    String? updateFeedURL,
    String? dismissedUpdate,
    String? pingProtocol,
    String? pingMethod,
    String? pingMode,
    int? pingTimeout,
    String? pingURL,
    int? pingExpectedStatus,
    String? pingExpectedBody,
    bool? autoUpdate,
    bool? reconnectAfterUpdate,
    String? updateAttempted,
    String? pausedPerAppMode,
  }) async {
    if (_closed || saving || !available)
      throw StateError('Desktop preferences unavailable');
    if (updateFeedURL != null &&
        updateFeedURL.isNotEmpty &&
        !isHttpsURL(updateFeedURL))
      throw ArgumentError.value(updateFeedURL, 'updateFeedURL');
    if (language != null && !languages.contains(language))
      throw ArgumentError.value(language, 'language');
    saving = true;
    final saved = _saved = Completer<void>();
    notifyListeners();
    try {
      if (expiryReminders == true &&
          !this.expiryReminders &&
          await channel.invokeMethod<bool>('requestNotifications') != true) {
        throw PlatformException(code: 'notifications_denied');
      }
      final next = {
        'version': 1,
        'theme': (theme ?? this.theme).name,
        'accent': accent ?? this.accent,
        'language': language ?? this.language,
        'sort': sort ?? this.sort,
        'pinned': (pinned ?? this.pinned).toList()..sort(),
        'closedGroups': (closedGroups ?? this.closedGroups).toList()..sort(),
        'expiryReminders': expiryReminders ?? this.expiryReminders,
        'favoritesFirst': favoritesFirst ?? this.favoritesFirst,
        'lastExpiryReminderDay':
            lastExpiryReminderDay ?? this.lastExpiryReminderDay,
        'loginWarningDismissed':
            loginWarningDismissed ?? this.loginWarningDismissed,
        'updateFeedURL': updateFeedURL ?? this.updateFeedURL,
        'dismissedUpdate': dismissedUpdate ?? this.dismissedUpdate,
        'pingProtocol': pingProtocol ?? this.pingProtocol,
        'pingMethod': pingMethod ?? this.pingMethod,
        'pingMode': pingMode ?? this.pingMode,
        'pingTimeout': pingTimeout ?? this.pingTimeout,
        'pingURL': pingURL ?? this.pingURL,
        'pingExpectedStatus': pingExpectedStatus ?? this.pingExpectedStatus,
        'pingExpectedBody': pingExpectedBody ?? this.pingExpectedBody,
        'autoUpdate': autoUpdate ?? this.autoUpdate,
        'reconnectAfterUpdate':
            reconnectAfterUpdate ?? this.reconnectAfterUpdate,
        'updateAttempted': updateAttempted ?? this.updateAttempted,
        'pausedPerAppMode': pausedPerAppMode ?? this.pausedPerAppMode,
      };
      await channel.invokeMethod<void>('setPreferences', {
        'json': jsonEncode(next),
      });
      if (_closed) return;
      this.theme = theme ?? this.theme;
      this.accent = accent ?? this.accent;
      this.language = language ?? this.language;
      this.sort = sort ?? this.sort;
      this.pinned = pinned ?? this.pinned;
      this.closedGroups = closedGroups ?? this.closedGroups;
      this.expiryReminders = expiryReminders ?? this.expiryReminders;
      this.favoritesFirst = favoritesFirst ?? this.favoritesFirst;
      this.lastExpiryReminderDay =
          lastExpiryReminderDay ?? this.lastExpiryReminderDay;
      this.loginWarningDismissed =
          loginWarningDismissed ?? this.loginWarningDismissed;
      this.dismissedUpdate = dismissedUpdate ?? this.dismissedUpdate;
      this.pingProtocol = pingProtocol ?? this.pingProtocol;
      this.pingMethod = pingMethod ?? this.pingMethod;
      this.pingMode = pingMode ?? this.pingMode;
      this.pingTimeout = pingTimeout ?? this.pingTimeout;
      this.pingURL = pingURL ?? this.pingURL;
      this.pingExpectedStatus = pingExpectedStatus ?? this.pingExpectedStatus;
      this.pingExpectedBody = pingExpectedBody ?? this.pingExpectedBody;
      final enabled = autoUpdate == true && !this.autoUpdate;
      this.autoUpdate = autoUpdate ?? this.autoUpdate;
      this.reconnectAfterUpdate =
          reconnectAfterUpdate ?? this.reconnectAfterUpdate;
      this.updateAttempted = updateAttempted ?? this.updateAttempted;
      this.pausedPerAppMode = pausedPerAppMode ?? this.pausedPerAppMode;
      if (updateFeedURL != null) {
        // Saving the feed, even unchanged, checks it again now.
        this.updateFeedURL = updateFeedURL;
        _updateCheckedFor = null;
        availableUpdate = null;
        _updateAssets = const [];
        updateChecked = null;
        // An installer that already runs finishes on its own.
        if (updateStage != UpdateStage.installing) {
          updateStage = UpdateStage.none;
          updateVersion = null;
        }
        unawaited(checkForUpdate());
      } else if (enabled) {
        _startDownload();
        unawaited(checkForUpdate());
      }
      error = null;
    } on Object {
      if (!_closed) error = l10n.appPreferencesNotSaved;
      rethrow;
    } finally {
      saving = false;
      saved.complete();
      if (!_closed) notifyListeners();
    }
  }

  /// The running version; the update check waits until the host reports it.
  set appVersion(String? value) {
    if (value == _appVersion) return;
    _appVersion = value;
    unawaited(checkForUpdate());
  }

  /// Reads the feed once a day, or hourly while it cannot be read. A newer
  /// release raises a notice the user can dismiss; where the core replaces
  /// this install, it also downloads the release unless [autoUpdate] is off.
  Future<void> checkForUpdate() async {
    final feed = updateFeedURL, version = _appVersion, at = DateTime.now();
    final last = updateChecked;
    if (!_closed &&
        available &&
        feed.isNotEmpty &&
        version != null &&
        updateControl != null)
      _updateStatus ??= _readUpdateStatus(version);
    // A download the network cut starts again once its hour has passed.
    _startDownload();
    if (_closed ||
        !available ||
        _checkingUpdate ||
        feed.isEmpty ||
        version == null ||
        _updateCheckedFor == '$feed\n$version' &&
            last != null &&
            at.difference(last.at) < Duration(hours: last.read ? 24 : 1))
      return;
    _checkingUpdate = true;
    AvailableUpdate? found;
    var assets = const <UpdateAsset>[];
    var read = false;
    try {
      final body = await updateFeedFetch(
        Uri.parse(feed),
      ).timeout(const Duration(seconds: 30));
      final update = parseUpdateFeed(body);
      read = update != null;
      if (update != null &&
          (compareVersions(update.version, version) ?? 0) > 0) {
        found = update;
        assets = parseUpdateAssets(body);
      }
      // The core's answer decides which notice the release raises.
      await _updateStatus;
    } on Object {
      // An unreachable or unreadable feed only means no notice.
    } finally {
      _checkingUpdate = false;
    }
    // A feed saved while this check ran is checked on its own.
    if (_closed || feed != updateFeedURL) return;
    _updateCheckedFor = '$feed\n$version';
    updateChecked = (at: at, read: read);
    availableUpdate = found;
    _updateAssets = assets;
    // A failed install holds back its own version, not a newer one.
    if (updateStage == UpdateStage.failed &&
        found != null &&
        found.version != updateVersion) {
      updateStage = UpdateStage.none;
      updateVersion = null;
    }
    notifyListeners();
    _startDownload();
  }

  // Asked once per run: what this install is, and what an earlier run left.
  Future<void> _readUpdateStatus(String running) async {
    final Map<String, Object?> status;
    try {
      status = await updateControl!.updateStatus();
    } on Object {
      return;
    }
    final kind = status['kind'], ready = status['ready'];
    final failed = status['failed'];
    if (_closed || kind is! String || kind.isEmpty) return;
    final support = updateSupport = (
      kind: kind,
      silent: status['silent'] == true,
    );
    bool newer(Object? version) =>
        version is String && (compareVersions(version, running) ?? 0) > 0;
    if (newer(failed)) {
      updateStage = UpdateStage.failed;
      updateVersion = failed as String;
      _refusedUpdates.add(failed);
    } else if (newer(ready)) {
      updateStage = UpdateStage.ready;
      updateVersion = ready as String;
    }
    notifyListeners();
    if (updateStage != UpdateStage.ready || !autoUpdate) return;
    if (support.kind == 'msi' &&
        support.silent &&
        updateAttempted != updateVersion) {
      unawaited(_installAtStart(updateVersion!));
    } else if (support.kind == 'appimage') {
      unawaited(_install(reconnect: false, user: false));
    }
  }

  Future<void> _installAtStart(String version) async {
    // Saved first: an installer that dies must not make every start exit.
    if (await _store(updateAttempted: version))
      await _install(reconnect: false, user: false);
  }

  // Starts the automatic download when everything allows it.
  void _startDownload() {
    final found = availableUpdate;
    if (_closed ||
        found == null ||
        !autoUpdate ||
        updateStage != UpdateStage.none ||
        _downloadingUpdate ||
        (_downloadRetry?.isAfter(DateTime.now()) ?? false) ||
        !updateInstallable)
      return;
    unawaited(() async {
      // Replacing an AppImage only swaps a file, so it needs no prompt.
      if (await _download(found) &&
          updateSupport?.kind == 'appimage' &&
          autoUpdate)
        await _install(reconnect: false, user: false);
    }());
  }

  Future<bool> _download(AvailableUpdate found) async {
    final control = updateControl, assets = _updateAssets;
    if (_closed || control == null || _downloadingUpdate) return false;
    _downloadingUpdate = true;
    updateStage = UpdateStage.downloading;
    updateVersion = found.version;
    notifyListeners();
    var downloaded = false;
    try {
      final reply = await control.downloadUpdate(assets);
      // A feed saved meanwhile reset the stage; its own check decides.
      if (_closed || updateStage != UpdateStage.downloading) return false;
      final version = reply['version'];
      updateStage = UpdateStage.ready;
      updateVersion = version is String && version.isNotEmpty
          ? version
          : found.version;
      downloaded = true;
    } on Object catch (error) {
      if (_closed) return false;
      final code = error is PlatformException ? error.code : '';
      // The core refused the release, and would again: the release page
      // stays, and this run does not try the version again. A download the
      // network cut, or sleep cancelled, is tried again in an hour.
      if (code.startsWith('update_') ||
          code == 'approval_required' ||
          code == 'invalid_request')
        _refusedUpdates.add(found.version);
      else
        _downloadRetry = DateTime.now().add(const Duration(hours: 1));
      if (updateStage == UpdateStage.downloading) {
        updateStage = UpdateStage.none;
        updateVersion = null;
      }
    } finally {
      _downloadingUpdate = false;
      if (!_closed) {
        notifyListeners();
        if (!downloaded) _startDownload();
      }
    }
    return downloaded;
  }

  /// Installs the release, downloading it first when the automatic download
  /// is off. [reconnect]: the VPN is on now and connects again after the update.
  Future<void> installUpdate({required bool reconnect}) =>
      _install(reconnect: reconnect, user: true);

  Future<void> _install({required bool reconnect, required bool user}) async {
    final control = updateControl, found = availableUpdate;
    if (_closed || control == null) return;
    if (updateStage == UpdateStage.none &&
        (found == null || !updateInstallable || !await _download(found)))
      return;
    if (_closed || updateStage != UpdateStage.ready) return;
    updateStage = UpdateStage.installing;
    notifyListeners();
    if (reconnect) await _store(reconnectAfterUpdate: true);
    Map<String, Object?>? reply;
    Object? failure;
    try {
      reply = await control.installUpdate(relaunch: true);
    } on Object catch (error) {
      failure = error;
    }
    if (_closed) return;
    if (reply?['exit'] == true) {
      // A helper installs once this process is gone and starts the new build.
      await onQuit?.call(restart: false);
      return;
    }
    if (reply?['restart'] == true) {
      updateStage = UpdateStage.installed;
      notifyListeners();
      // An automatic swap keeps the running build until the user restarts.
      if (user) await onQuit?.call(restart: true);
      return;
    }
    final denied =
        failure is PlatformException && failure.code == 'elevation_denied';
    updateStage = denied ? UpdateStage.ready : UpdateStage.failed;
    if (!denied && updateVersion != null) _refusedUpdates.add(updateVersion!);
    notifyListeners();
    if (reconnectAfterUpdate) await _store(reconnectAfterUpdate: false);
  }

  /// Starts the installed build; [reconnect]: the VPN is on now.
  Future<void> restartForUpdate({required bool reconnect}) async {
    if (_closed || updateStage != UpdateStage.installed) return;
    if (reconnect) await _store(reconnectAfterUpdate: true);
    await onQuit?.call(restart: true);
  }

  Future<void> remindExpiring(
    SubscriptionSnapshot snapshot, {
    DateTime? now,
  }) async {
    if (_closed || !available || !expiryReminders || saving || _notifying)
      return;
    final seconds = (now ?? DateTime.now()).millisecondsSinceEpoch ~/ 1000;
    final day = seconds ~/ Duration.secondsPerDay;
    if (lastExpiryReminderDay >= day) return;
    final count = snapshot.subscriptions.where((group) {
      final expiry = group.metadata['expire'];
      return !group.isLocal &&
          expiry is int &&
          expiry > seconds &&
          expiry <= seconds + 3 * Duration.secondsPerDay;
    }).length;
    if (count == 0) return;
    _notifying = true;
    try {
      await channel.invokeMethod<void>('showExpiryReminder', {'count': count});
      if (_closed) return;
      // Keep the in-session receipt even if persisting preferences fails.
      lastExpiryReminderDay = day;
      await update(lastExpiryReminderDay: day);
    } on Object {
      if (!_closed) {
        error = l10n.remindersUnavailable;
        notifyListeners();
      }
    } finally {
      _notifying = false;
    }
  }

  void status(
    String state, {
    required bool canConnect,
    required bool canDisconnect,
  }) {
    if (_closed || !available) return;
    final next = (
      state: state,
      canConnect: canConnect,
      canDisconnect: canDisconnect,
      // The tray, dialogs and notifications speak the language the app resolved.
      language: appLocale.languageCode,
    );
    if (_desiredStatus != next) {
      _desiredStatus = next;
      _statusRetry?.cancel();
      _statusRetry = null;
      _statusRetrySeconds = 1;
    }
    unawaited(_sendStatus());
  }

  Future<void> _sendStatus() async {
    if (_closed ||
        _statusSending ||
        _statusRetry != null ||
        _desiredStatus == null ||
        _desiredStatus == _status)
      return;
    final value = _desiredStatus!;
    _statusSending = true;
    var changed = false;
    try {
      await channel.invokeMethod<void>('setStatus', {
        'state': value.state,
        'canConnect': value.canConnect,
        'canDisconnect': value.canDisconnect,
        'language': value.language,
      });
      if (_closed) return;
      _status = value;
      _statusRetrySeconds = 1;
      if (error == _statusError) {
        error = null;
        changed = true;
      }
    } on Object {
      if (_closed) return;
      _status = null;
      if (_desiredStatus == value) {
        changed = error != _statusError;
        error = _statusError;
        _statusRetry = Timer(Duration(seconds: _statusRetrySeconds), () {
          _statusRetry = null;
          unawaited(_sendStatus());
        });
        _statusRetrySeconds = (_statusRetrySeconds * 2).clamp(1, 30);
      }
    } finally {
      _statusSending = false;
      if (!_closed) {
        if (_desiredStatus != value) unawaited(_sendStatus());
        if (changed) notifyListeners();
      }
    }
  }

  @override
  void dispose() {
    _closed = true;
    _statusRetry?.cancel();
    onAction = null;
    channel.setMethodCallHandler(null);
    super.dispose();
  }
}
