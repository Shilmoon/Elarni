part of 'desktop_proxy.dart';

extension _ProxyCommands on DesktopProxy {
  Future<CatalogMap> _stop({required bool release}) =>
      _disconnecting ??= () async {
        _automation.manualStop();
        await _disconnect(release: release);
        // Stop must clear persisted intent even as the first command. Loading
        // replaces the automation object, so stop the restored episode too.
        await _initialize();
        _automation.manualStop();
        _state = 'disconnected';
        if (!_storageLost) await _persistAutomation();
        return <String, Object?>{'state': _state};
      }().whenComplete(() => _disconnecting = null);

  Future<CatalogMap> _invoke(
    String method,
    CatalogMap? arguments, {
    bool automatic = false,
  }) async {
    if (_disposed) throw desktopError('cancelled');
    // The user's stop (the window, the tray, links, the CLI) lifts Block
    // traffic without VPN, also the kill switch a lost tunnel kept.
    if (method == 'disconnect') return _stop(release: true);
    if (method == 'openUpdateLink') {
      // The page an update feed named needs no catalog.
      final url = arguments?['url'];
      if (url is! String || !isHttpsURL(url)) throw desktopError('invalid_url');
      return _platformRequest('openURL', {'url': url});
    }
    // Updates replace the app, not the catalog. The core checks each file.
    if (method == 'updateStatus') return _native('updateStatus', {});
    // Why the catalog cannot be read: asking never starts a load, it only
    // waits for the one in flight.
    if (method == 'storageStatus') {
      await _initializing?.then<void>((_) {}, onError: (Object _) {});
      return _storageStatus();
    }
    if (method == 'resetStorage') return _resetStorage();
    if (method == 'downloadUpdate') {
      final assets = arguments?['assets'];
      if (assets is! List || assets.any((asset) => asset is! Map))
        throw desktopError('invalid_request');
      // Through the tunnel while it is up, as the other downloads go.
      final proxy = _tunnelFetchProxy();
      return _native('downloadUpdate', {
        'assets': assets,
        if (proxy != null) 'proxy': proxy,
      }, timeout: const Duration(minutes: 35));
    }
    if (method == 'installUpdate') {
      return _native('installUpdate', {
        'relaunch': arguments?['relaunch'] == true,
      }, timeout: const Duration(minutes: 20));
    }
    if (method == 'checkProcessRules') {
      // Rule checks read this computer's programs, not the catalog.
      final rules = arguments?['rules'];
      final apps = arguments?['apps'] ?? const <String>[];
      if (rules is! List ||
          rules.any((rule) => rule is! String) ||
          apps is! List ||
          apps.any((app) => app is! String))
        throw desktopError('invalid_rules');
      return _native('checkProcessRules', {'rules': rules, 'apps': apps});
    }
    final readOnly = {
      'state',
      'subscriptions',
      'routingProfiles',
      'tunnelSettings',
      'automationStatus',
      'runtimeMetrics',
      'readLogs',
      'readConnections',
      'nodeConfig',
      'inboundCredentials',
    }.contains(method);
    // Stop publishes "disconnected" before it saves the cleared intent; a
    // Connect issued in that window follows the Stop instead of failing busy.
    if (method == 'connect' && !automatic && _disconnecting != null)
      await _disconnecting!.catchError((Object _) => <String, Object?>{});
    if (_disconnecting != null && !readOnly) throw desktopError('busy');
    final stopGeneration = _linkStopGeneration;
    await _initialize();
    if (_disposed || !readOnly && stopGeneration != _linkStopGeneration)
      throw desktopError('cancelled');
    if (_storageLost) throw desktopError('storage_unavailable');
    final args = arguments ?? <String, Object?>{};
    switch (method) {
      case 'state':
        return {
          'state': _state,
          if (_state == 'connected') 'liveness': _liveness,
        };
      case 'subscriptions':
        return _catalog!.snapshot();
      case 'routingProfiles':
        return _catalog!.routingSnapshot();
      case 'tunnelSettings':
        return _catalog!.settingsSnapshot();
      case 'inboundCredentials':
        return catalogMap(_catalog!.data['inboundCredentials'] ?? {});
      case 'connect':
        if (!automatic) {
          final generation = await _interruptManualProbes();
          // A scheduled refresh can hold the controller while it reads
          // disconnected; queue behind it instead of failing busy.
          while (_operation != null) await _operation!.future;
          _checkGeneration(generation);
          _automation.intent = true;
          _automation.rearm(_now);
          await _persistAutomation();
        }
        try {
          return await _connectPreparingLostAssets();
        } on PlatformException catch (error) {
          // Recovery must not repeat a consent prompt the user declined; one
          // that timed out unanswered is asked again by the next retry.
          if (error.code == 'elevation_denied' ||
              error.code == 'vpn_guard_stale' &&
                  error.details == 'elevation_denied') {
            _automation.manualStop();
            await _persistAutomation();
          }
          rethrow;
        }
      case 'configureAutomation':
        final configuration = {...args}..remove('selectedNodeId');
        final selected = unattended ? args['selectedNodeId'] as String? : null;
        final proposed = _catalog!.copy();
        if (selected != null) proposed.data['selectedNodeId'] = selected;
        DesktopAutomation.validate(configuration, proposed);
        _automation.invalidate();
        if (_automationBusy) await _operation?.future;
        await _mutate((candidate) async {
          if (selected != null) candidate.data['selectedNodeId'] = selected;
          candidate.data['automation'] = copyCatalog(configuration);
          return {};
        });
        _automation.rearm(_now);
        _automation.status = args['enabled'] == true
            ? 'waitingForPath'
            : 'disabled';
        _candidateFailures.clear();
        await _persistAutomation();
        return _automationStatus();
      case 'automationStatus':
        return _automationStatus();
      case 'systemInfo':
        // Asked once: each question starts a core process.
        try {
          _coreVersion ??=
              (await _native('xrayVersion', {}))['version'] as String?;
        } on Object {
          // The page is still useful without the core's version.
        }
        // The UI reloads on every backend event, so this one is asked again
        // only after half a minute.
        final known = _vpnService;
        if (Platform.isWindows &&
            (known == null || _clock.elapsedMilliseconds - known.at >= 30000)) {
          try {
            _vpnService = (
              enabled: (await _native('vpnServiceStatus', {}))['enabled'],
              at: _clock.elapsedMilliseconds,
            );
          } on Object {
            // The row then reads as off.
            _vpnService = null;
          }
        }
        return {
          ...await _platformRequest(method, args),
          'os': desktopOSVersion(),
          'appVersion': ?desktopAppVersion,
          'coreVersion': ?_coreVersion,
          'vpnService': ?_vpnService?.enabled,
        };
      case 'setVPNService':
        if (!Platform.isWindows) throw desktopError('unsupported_method');
        _vpnService = null;
        // The consent prompt waits for the user.
        final service = await _native('setVPNService', {
          'enabled': args['enabled'] == true,
        }, timeout: const Duration(seconds: 90));
        _vpnService = (
          enabled: service['enabled'],
          at: _clock.elapsedMilliseconds,
        );
        return {'vpnService': service['enabled']};
      // Settings replace their system info with this reply.
      case 'setLaunchAtLogin':
        return {
          ...await _platformRequest(method, args),
          'appVersion': ?desktopAppVersion,
        };
      case 'routeTable':
        return _platformRequest(method, args);
      case 'runtimeMetrics':
        return _runtimeMetrics();
      case 'pingNode':
        // The timeout reaches PowerShell and process deadlines; the UI offers 5-15 s.
        final pingTimeout = args['timeout'];
        if (pingTimeout is! int || pingTimeout < 1 || pingTimeout > 60)
          throw desktopError('invalid_request');
        final exclusive = args['protocol'] == 'icmp';
        if (_interruptingProbes ||
            (_operation != null && (exclusive || _manualProbes == 0)) ||
            {'connecting', 'disconnecting'}.contains(_state))
          throw desktopError('busy');
        final done = _operation ??= Completer<void>();
        if (!exclusive) _manualProbes++;
        try {
          return await (exclusive
              ? _pingICMP(args, _generation)
              : _ping(args, _generation));
        } finally {
          if (exclusive || --_manualProbes == 0) {
            _operation = null;
            done.complete();
          }
        }
      case 'readLogs':
        final after = args['after'] is int ? args['after']! as int : 0;
        return {
          'level': _catalog!.settings.logLevel,
          'entries': [
            for (final line in _logs)
              if ((line['seq']! as int) > after) line,
          ],
          'last': _logSeq,
        };
      case 'readConnections':
        if (_state != 'connected')
          return {'epoch': 0, 'seq': 0, 'connections': const <Object?>[]};
        final epoch = args['epoch'] is int ? args['epoch']! as int : 0;
        final after = epoch == _connectionsEpoch && args['after'] is int
            ? args['after']! as int
            : 0;
        return {
          'epoch': _connectionsEpoch,
          'seq': _connections.isEmpty ? 0 : _connections.values.last['seq'],
          'connections': [
            for (final entry in _connections.values)
              if ((entry['seq']! as int) > after) entry,
          ],
        };
      case 'saveLogs':
        final content = args['content'];
        if (content is! String || content.isEmpty)
          throw desktopError('invalid_settings');
        await _platformRequest('saveLogs', {'content': content});
        return {};
      case 'saveFile':
        final kind = args['kind'], bytes = args['bytes'];
        if (!{'text', 'png', 'json'}.contains(kind) || bytes is! Uint8List)
          throw desktopError('invalid_request');
        final CatalogMap picked;
        try {
          picked = await _platformRequest('pickSavePath', {'kind': kind});
        } on PlatformException catch (error) {
          if (error.code == 'cancelled') return {'saved': false};
          rethrow;
        }
        try {
          await File(picked['path']! as String).writeAsBytes(bytes);
        } on FileSystemException {
          throw desktopError('save_failed');
        }
        return {'saved': true};
      case 'exportEvidence':
        final evidence = connectionDiagnostics(
          platform: Platform.isLinux ? 'linux' : 'windows',
          state: _state,
          provider: _runtimeMetrics(),
          automation: _automationStatus(),
          issueCodes: (args['issueCodes'] as List? ?? []).whereType<String>(),
        );
        if (args['headless'] != true)
          await _platformRequest('saveEvidence', {
            'content': jsonEncode(evidence),
          });
        return evidence;
      case 'markRecording' when recordingBuild:
        await _native('diagnostics', {'action': 'mark'});
        return {};
      case 'exportRecording' when recordingBuild:
        if (args['headless'] == true) {
          await _native('diagnostics', {
            'action': 'export',
            'destination': args['destination'],
          });
          return {};
        }
        // The ZIP is built first, so an empty recording says so before any dialog.
        final temporary = await Directory.systemTemp.createTemp('elarni-');
        try {
          final source = '${temporary.path}${Platform.pathSeparator}rec.zip';
          await _native('diagnostics', {
            'action': 'export',
            'destination': source,
          });
          final picked = await _platformRequest('pickRecordingPath', {});
          try {
            await File(source).copy(picked['path']! as String);
          } on FileSystemException {
            throw desktopError('recording_failed');
          }
        } finally {
          await temporary.delete(recursive: true).catchError((Object _) {
            return temporary;
          });
        }
        return {};
      // Subscriptions a backup may not hold are left out; the result names them.
      case 'exportCatalogBackup':
        final (:document, :excluded) = _catalog!.backup();
        final CatalogMap sealed;
        try {
          sealed = await _native('sealCatalogBackup', {
            'catalog': document,
            'password': args['password'],
          });
        } on PlatformException catch (error) {
          // Sealing validates like restore; the UI checks the password first.
          if (error.code == 'invalid_backup')
            throw desktopError('backup_unsupported');
          rethrow;
        }
        if (args['headless'] == true)
          return {'text': sealed['text'], 'excluded': excluded};
        await _platformRequest('saveBackup', {'content': sealed['text']});
        return {'excluded': excluded};
      case 'restoreCatalogBackup':
        if (_operation != null ||
            !{'disconnected', 'prepared', 'failed'}.contains(_state)) {
          throw desktopError('busy');
        }
        await _invoke('disconnect', {});
        final generation = _generation;
        final text = args['headless'] == true
            ? args['content'] as String
            : await _importContent('backup', null);
        _checkGeneration(generation);
        if (!{'disconnected', 'prepared', 'failed'}.contains(_state))
          throw desktopError('busy');
        final restored = await _mutate((candidate) async {
          final opened = await _native('openCatalogBackup', {
            'text': text,
            'password': args['password'],
          });
          final validated = await _native('validateCatalogBackup', {
            'catalog': opened['catalog'],
          });
          candidate.restoreBackup(catalogMap(validated['catalog']));
          return candidate.snapshot();
        }, refreshAssets: false);
        _challenges.clear();
        return restored;
      case 'repairTunnel':
        // Repair lifts the kill switch itself, elevated if it must, so its
        // stop asks for nothing.
        await _stop(release: false);
        if (_operation != null || _state != 'disconnected')
          throw desktopError('busy');
        final done = _operation = Completer<void>();
        try {
          final session = _vpnSession;
          await _reap(session);
          if (_vpnSession == session) _vpnSession = null;
          // A stopped helper's DNS guard stays until a helper removes it.
          if (Platform.isWindows || Platform.isLinux)
            await _native(
              'clearVPNGuard',
              {},
              timeout: const Duration(seconds: 90),
            );
          _guardKept = _guardHeld = false;
          return {'state': _state};
        } finally {
          _operation = null;
          done.complete();
        }
      // Showing a server's config is subject to the same provider limits as sharing it.
      case 'shareClient':
      case 'nodeConfig':
        final id = args['id']! as String;
        final subscription =
            method == 'shareClient' && args['subscription'] == true;
        final source = subscription
            ? _catalog!.source(id)
            : _catalog!.sources.firstWhere(
                (source) => catalogList(
                  source['nodes'],
                ).any((node) => node['id'] == id),
                orElse: () => throw desktopError('not_found'),
              );
        if (catalogMap(source['metadata'] ?? {})['hideURL'] == true)
          throw desktopError('sharing_disabled');
        if (source['url'] != null &&
            catalogMap(source['metadata'] ?? {})['hideURL'] == null)
          throw desktopError('sharing_refresh_required');
        String text;
        if (subscription) {
          if (source['url'] == null) throw desktopError('invalid_profile');
          text = (source['shareLink'] ?? source['url'])! as String;
        } else {
          final node = _catalog!.node(id);
          if (node['hideURL'] == true) throw desktopError('sharing_disabled');
          if (method == 'nodeConfig') {
            if (node['format'] != 'xrayProfile')
              throw desktopError('unsupported_profile');
            return {'config': node['config']};
          }
          if (node['format'] == 'telegram') {
            text = catalogMap(node['config'])['url']! as String;
          } else {
            text = jsonEncode({
              ...catalogMap(node['config']),
              'remarks': node['manualName'] ?? node['name'],
            });
          }
        }
        return {
          'text': text,
          ...await _native('clientQR', {'text': text}),
        };
      // A local group has no subscription link; its servers are shared as
      // their links, one per line in group order, as macOS, iOS and Android do.
      case 'shareLinks':
        final ids = args['nodeIds'];
        if (ids is! List || ids.any((id) => id is! String))
          throw desktopError('invalid_request');
        final source = _catalog!.source(args['subscriptionId']! as String);
        if (source['url'] != null) throw desktopError('invalid_profile');
        if (catalogMap(source['metadata'] ?? {})['hideURL'] == true)
          throw desktopError('sharing_disabled');
        final nodes = [
          for (final node in catalogList(source['nodes']))
            if (ids.contains(node['id'])) node,
        ];
        if (nodes.isEmpty) throw desktopError('invalid_profile');
        final links = <String>[];
        for (final node in nodes) {
          if (node['hideURL'] == true) throw desktopError('sharing_disabled');
          if (node['format'] == 'telegram') {
            links.add(catalogMap(node['config'])['url']! as String);
            continue;
          }
          final name = (node['manualName'] ?? node['name'])! as String;
          // The window words this in its language, with the name it is given.
          final none = PlatformException(
            code: 'no_share_link',
            message: '$name has no share link. Share it from its own page.',
            details: name,
          );
          // A profile with several outbounds has no link.
          final outbounds = catalogList(
            catalogMap(node['config'])['outbounds'],
          );
          if (outbounds.length != 1) throw none;
          final CatalogMap converted;
          try {
            converted = await _native('convertXrayJsonToShareLinks', {
              // The link takes its name from the outbound tag.
              'xrayJson': jsonEncode({
                'outbounds': [
                  {...outbounds.single, 'tag': name},
                ],
              }),
            });
          } on PlatformException catch (error) {
            // The core's refusal of a protocol it writes no link for, such as
            // WireGuard or HTTP, reads as operation_failed.
            if (error.code != 'operation_failed') rethrow;
            throw none;
          }
          final text = converted['links'];
          // An outbound whose link would lose settings comes back as the JSON
          // it was given.
          if (text is! String || text.isEmpty || text.startsWith('{'))
            throw none;
          links.add(text);
        }
        return {'text': links.join('\n')};
      case 'qrCode':
        final text = args['text'];
        if (text is! String || text.isEmpty)
          throw desktopError('invalid_request');
        return _native('clientQR', {'text': text});
      case 'openTelegramProxy':
        final node = _catalog!.node(args['id']! as String);
        if (node['format'] != 'telegram') throw desktopError('invalid_profile');
        final url = catalogMap(node['config'])['url']! as String;
        final parsed = catalogList(
          (await _native('importClientProfiles', {'text': url}))['profiles'],
        );
        if (parsed.length != 1 || parsed.single['format'] != 'telegram')
          throw desktopError('invalid_url');
        return _platformRequest('openURL', {'url': parsed.single['url']});
      case 'subscriptionDeviceID':
        return _native(
          'subscriptionDeviceID',
          _catalog!.deviceIdentity(args['id']! as String),
        );
      case 'openProviderLink':
        final source = _catalog!.source(args['id']! as String);
        if (!{'support', 'account', 'banner'}.contains(args['action']))
          throw desktopError('invalid_action');
        final url = DesktopCatalog.safeURL(
          catalogMap(source['links'] ?? {})[args['action']],
        );
        if (url == null) throw desktopError('invalid_url');
        return _platformRequest('openURL', {'url': url});
    }
    const mutations = {
      'importClientProfiles',
      'addSubscription',
      'refreshSubscription',
      'selectNode',
      'selectProfileGroup',
      'setNodeIPPreference',
      'setNodeFavorite',
      'renameNode',
      'deleteNode',
      'deleteNodes',
      'showNode',
      'updateNodeConfig',
      'renameSubscription',
      'resetSubscriptionDeviceID',
      'updateSubscriptionFetchOptions',
      'deleteSubscription',
      'updateTunnelSettings',
      'updateInboundCredentials',
      'importRouting',
      'updateRoutingProfile',
      'duplicateRoutingProfile',
      'deleteRoutingProfile',
      'resetRoutingProfile',
      'setRoutingProfileEnabled',
      'assignRoutingProfile',
      'refreshRoutingGeofiles',
      'refreshManagedLists',
    };
    if (!mutations.contains(method))
      throw desktopError('unsupported_operation');
    if (!automatic) {
      _automation.invalidate();
      if (_automationBusy) await _operation?.future;
      if (_automation.intent) _automation.healthAt = _now;
    }
    PlatformException? refreshError;
    String? selectedBeforeRefresh;
    final result = await _mutate((candidate) async {
      CatalogMap? warning;
      String addSource(CatalogMap input) {
        final id = candidate.addSource(input['text']! as String);
        final source = candidate.source(id);
        final name = candidate.display(input['name']);
        candidate.putSource({
          ...source,
          if (input['shareLink'] is String) 'shareLink': input['shareLink'],
          if (name != null) ...{
            'importedName': name,
            if (source['manualName'] == null && source['providerName'] == null)
              'name': name,
          },
        });
        return id;
      }

      Future<void> refresh(String id) async {
        List<String>? approval;
        var format = args['format'] as String?;
        if (args['challenge'] != null) {
          final challenge = _challenges.remove(args['challenge']);
          if (challenge == null ||
              challenge.id != id ||
              DateTime.now().isAfter(challenge.expires) ||
              format != null && format != challenge.format)
            throw desktopError('invalid_challenge');
          approval = challenge.chain;
          format = challenge.format;
        }
        final beforeRefresh = copyCatalog(candidate.data);
        selectedBeforeRefresh = candidate.selected;
        try {
          warning = await candidate.refresh(
            id,
            approval: approval,
            format: format,
          );
          // Servers the compiler refuses fail this refresh, which records it,
          // rather than the publication, which would record nothing. The
          // publication reuses these compiles.
          if (warning == null) {
            try {
              await _semantics(candidate);
            } on PlatformException catch (error) {
              // One refused server is left out, like a line the importer
              // cannot read; with none left the refresh fails.
              if (error.code != 'unsupported_profile') rethrow;
              final refused = await DesktopProxy._refusedOff(
                domainPath,
                candidate.data,
                {
                  for (final node in catalogList(candidate.source(id)['nodes']))
                    node['id']! as String,
                },
              );
              if (!candidate.dropRefused(id, refused)) rethrow;
              await _semantics(candidate);
            }
          }
        } on PlatformException catch (error) {
          candidate.data
            ..clear()
            ..addAll(beforeRefresh);
          candidate.refreshFailed(id, error.code);
          refreshError = error;
        }
        final pending = warning;
        if (pending != null) {
          _challenges.removeWhere(
            (_, value) =>
                value.id == id || DateTime.now().isAfter(value.expires),
          );
          final token = desktopID();
          _challenges[token] = (
            id: id,
            format: pending['format']! as String,
            chain: List<String>.from(pending['chain']! as List),
            expires: DateTime.now().add(const Duration(minutes: 5)),
          );
          warning = {
            'subscriptionId': id,
            'riskCodes': pending['riskCodes'],
            'destinations': pending['destinations'] ?? <Object?>[],
            'challenge': token,
          };
        }
      }

      switch (method) {
        case 'importClientProfiles':
          final input = await _native('normalizeClientInput', {
            'text': await _importContent(
              args['source'] as String? ?? 'text',
              args['content'],
            ),
          });
          final content = input['text']! as String;
          final url = DesktopCatalog.safeURL(content.trim());
          final parsed = url == null ? null : Uri.parse(url);
          if (candidate.isRoutingInput(content)) {
            candidate.importRouting(content);
          } else if (parsed != null &&
              parsed.path.isNotEmpty &&
              parsed.path != '/' &&
              !({'t.me', 'telegram.me'}.contains(parsed.host) &&
                  parsed.path == '/proxy')) {
            await refresh(addSource({...input, 'text': url!}));
          } else {
            final local = await candidate.importLocal(content);
            try {
              await _semantics(candidate);
            } on PlatformException catch (error) {
              // As in a refresh, a server the compiler refuses is left out;
              // only those this import brought may go, and with none of them
              // left the import fails.
              if (error.code != 'unsupported_profile') rethrow;
              final added = {
                for (final node in local.added) node['id']! as String,
              };
              final refused = await DesktopProxy._refusedOff(
                domainPath,
                candidate.data,
                added,
              );
              if (!candidate.dropRefused(local.id, refused, added: added))
                rethrow;
              await _semantics(candidate);
            }
          }
        case 'addSubscription':
        case 'refreshSubscription':
          final id = method == 'addSubscription'
              ? addSource(
                  await _native('normalizeClientInput', {'text': args['url']}),
                )
              : args['id']! as String;
          await refresh(id);
        case 'selectNode':
          if (candidate.node(args['id']! as String)['format'] != 'xrayProfile')
            throw desktopError('unsupported_profile');
          candidate.data['selectedNodeId'] = args['id'];
          if (_automationConfig['enabled'] == true) {
            final source = candidate.sources.firstWhere(
              (source) => catalogList(
                source['nodes'],
              ).any((node) => node['id'] == args['id']),
            );
            // The picked server becomes the primary; the allowed backups stay.
            final ids = [
              args['id']! as String,
              if (_automationConfig['policy'] != 'fixed')
                ...(_automationConfig['nodeIDs']! as List).cast<String>().where(
                  (id) => id != args['id'],
                ),
            ].take(1000).toList();
            candidate.data['automation'] = {
              ..._automationConfig,
              'subscriptionID': source['id'],
              'nodeIDs': ids,
              if (_automationConfig['russianEntryNodeIDs'] case final List old)
                'russianEntryNodeIDs': old.where(ids.contains).toList(),
            };
          }
        case 'setNodeIPPreference':
          candidate.setNodeIPPreference(
            args['nodeId']! as String,
            args['preference'],
          );
        case 'setNodeFavorite':
          if (args['favorite'] is! bool) throw desktopError('invalid_settings');
          candidate.updateNode(args['nodeId']! as String, {
            'favorite': args['favorite'],
          });
        case 'renameNode':
          final text = (args['name'] as String?)?.trim() ?? '';
          final name = candidate.display(text);
          if (text.isNotEmpty && (name == null || name.length > 256))
            throw desktopError('invalid_name');
          // `name` stays the provider's, which refresh matches on.
          candidate.updateNode(args['nodeId']! as String, {'manualName': name});
        case 'deleteNode':
          candidate.deleteNode(args['nodeId']! as String);
        case 'deleteNodes':
          // One save for a whole selection; any refusal keeps every server.
          for (final id in args['nodeIds']! as List)
            candidate.deleteNode(id as String);
        case 'showNode':
          candidate.showNode(args['nodeId']! as String);
        case 'updateNodeConfig':
          await candidate.replaceNodeConfig(
            args['nodeId']! as String,
            args['content']! as String,
          );
        case 'selectProfileGroup':
          candidate.selectGroup(
            args['nodeId']! as String,
            args['group']! as String,
            args['choice']! as String,
          );
        case 'updateSubscriptionFetchOptions':
          final source = candidate.source(args['id']! as String);
          final options = catalogMap(args['options']);
          final hosts = List<String>.from(
            options['fallbackHosts'] as List? ?? [],
          );
          if (source['url'] == null ||
              options.keys.any(
                (key) => !{
                  'fallbackHosts',
                  'userAgent',
                  'connectAddress',
                  'httpHost',
                  'fragmentLength',
                  'fragmentInterval',
                  'refreshHours',
                }.contains(key),
              ) ||
              // Snapshots read the stored options as text.
              options.entries.any(
                (entry) =>
                    entry.key != 'fallbackHosts' && entry.value is! String,
              ) ||
              hosts.length > 8 ||
              hosts.any((host) => !SubscriptionGroup.validFallbackHost(host)) ||
              options['refreshHours'] != null &&
                  !RegExp(
                    r'^([1-9]|[1-9][0-9]|1[0-5][0-9]|16[0-8])$',
                  ).hasMatch(options['refreshHours']! as String)) {
            throw desktopError('invalid_settings');
          }
          await _native('validateSubscriptionFetchOptions', {
            'url': source['url'],
            for (final entry in options.entries)
              if (entry.key != 'refreshHours') entry.key: entry.value,
          });
          candidate.putSource({
            ...source,
            'fallbackHosts': hosts.toSet().toList(),
            'fetchOptions': {
              for (final entry in options.entries)
                if (entry.key != 'fallbackHosts') entry.key: entry.value,
            },
          });
        case 'resetSubscriptionDeviceID':
          final source = candidate.source(args['id']! as String);
          if (source['url'] == null) throw desktopError('invalid_profile');
          // A random key of the subscription's own; every refresh sends the ID
          // it makes. The next scheduled refresh sends the new one at once:
          // after a device reply the provider may take it.
          candidate.putSource({
            ...source,
            'deviceSalt': desktopID(),
            'nextRefreshAt': candidate.now,
          });
        case 'renameSubscription':
          final source = candidate.source(args['id']! as String);
          final text = (args['name'] as String?)?.trim();
          final name = candidate.display(text);
          if (text != null &&
              text.isNotEmpty &&
              (name == null || name.length > 256))
            throw desktopError('invalid_name');
          candidate.putSource({
            ...source,
            'manualName': name,
            'name':
                name ??
                source['providerName'] ??
                source['importedName'] ??
                (source['url'] == null
                    ? 'Local profiles'
                    : Uri.parse(source['url']! as String).host),
          });
        case 'deleteSubscription':
          final id = candidate.source(args['id']! as String)['id'];
          candidate.data['subscriptions'] = candidate.sources
              .where((item) => item['id'] != id)
              .toList();
          candidate.data['assignments'] = candidate.assignments..remove(id);
          candidate.data['profiles'] = candidate.routes
              .where((item) => item['providerSubscriptionId'] != id)
              .toList();
          final validRoutes = candidate.routes
              .map((item) => item['id'])
              .toSet();
          candidate.data['assignments'] = candidate.assignments
            ..removeWhere((_, value) => !validRoutes.contains(value));
          candidate.reconcileSelection();
          // Recovery starts from the selected server, so it turns off with it.
          if (candidate.selected == null &&
              _automationConfig['enabled'] == true)
            candidate.data['automation'] = {
              ...catalogMap(candidate.data['automation']),
              'enabled': false,
            };
        case 'updateTunnelSettings':
          final settings = TunnelSettings.fromMap(args);
          candidate.data['settings'] = settings
              .copyWith(
                includedRoutes: _canonicalRoutes(settings.includedRoutes),
                excludedRoutes: _canonicalRoutes(settings.excludedRoutes),
              )
              .toMap();
        case 'updateInboundCredentials':
          final username = args['username'], password = args['password'];
          final controls = RegExp(r'[\x00-\x1f\x7f-\x9f]');
          if (username is! String ||
              password is! String ||
              utf8.encode(username).isEmpty ||
              utf8.encode(username).length > 255 ||
              utf8.encode(password).isEmpty ||
              utf8.encode(password).length > 255 ||
              username.contains(':') ||
              controls.hasMatch(username) ||
              controls.hasMatch(password))
            throw desktopError('invalid_settings');
          candidate.data['inboundCredentials'] = {
            'username': username,
            'password': password,
          };
          candidate.data['settings'] = candidate.settings
              .copyWith(inboundAuthEnabled: true)
              .toMap();
        case 'importRouting':
        case 'updateRoutingProfile':
          candidate.importRouting(
            await _importContent('text', args['content']),
            update: method == 'updateRoutingProfile'
                ? args['id']! as String
                : null,
          );
        case 'duplicateRoutingProfile':
          final old = candidate.route(args['id']! as String);
          final profile = {
            ...catalogMap(old['profile']),
            'Name': '${old['name']} copy',
          };
          candidate.putRoute({
            ...copyCatalog(old),
            'id': desktopID(),
            'name': '${old['name']} copy',
            'profile': profile,
            'original': profile,
            'source': 'local',
            'providerSubscriptionId': null,
            'edited': false,
          });
        case 'deleteRoutingProfile':
          final id = candidate.route(args['id']! as String)['id'];
          candidate.data['profiles'] = candidate.routes
              .where((item) => item['id'] != id)
              .toList();
          candidate.data['assignments'] = candidate.assignments
            ..removeWhere((_, value) => value == id);
        case 'resetRoutingProfile':
          final old = candidate.route(args['id']! as String);
          candidate.importRouting(
            jsonEncode(old['original']),
            update: old['id']! as String,
          );
          candidate.putRoute({
            ...candidate.route(old['id']! as String),
            'edited': false,
          });
        case 'setRoutingProfileEnabled':
          if (args['enabled'] is! bool) throw desktopError('invalid_routing');
          candidate.putRoute({
            ...candidate.route(args['id']! as String),
            'enabled': args['enabled'],
          });
        case 'assignRoutingProfile':
          candidate.assignRouting(
            candidate.source(args['subscriptionId']! as String)['id']!
                as String,
            args['profileId'] == null
                ? null
                : candidate.route(args['profileId']! as String)['id']!
                      as String,
          );
        case 'refreshRoutingGeofiles':
          try {
            await _refreshGeofiles(
              candidate,
              args['id']! as String,
              refresh: true,
              file: args['file'] as String?,
            );
          } on PlatformException catch (error) {
            candidate.geofilesFailed(args['id']! as String, error);
            refreshError = error;
          }
        // Like a geofile refresh, a failed download is recorded on the
        // profile, which keeps the list's last good copy, and reported.
        case 'refreshManagedLists':
          final failure = await candidate.refreshManaged(
            args['id']! as String,
            sourceId: args['sourceId'] as String?,
            proxy: _tunnelFetchProxy(),
          );
          if (failure != null) refreshError = desktopError(failure);
      }
      return switch (method) {
        'importClientProfiles' ||
        'addSubscription' ||
        'refreshSubscription' ||
        'setNodeIPPreference' ||
        'selectProfileGroup' => {
          'subscriptions': candidate.snapshot(),
          'warning': warning,
        },
        'selectNode' => {'subscriptions': candidate.snapshot()},
        'renameSubscription' ||
        'resetSubscriptionDeviceID' ||
        'setNodeFavorite' ||
        'renameNode' ||
        'deleteNode' ||
        'deleteNodes' ||
        'showNode' ||
        'updateNodeConfig' ||
        'deleteSubscription' ||
        'updateSubscriptionFetchOptions' => candidate.snapshot(),
        'updateTunnelSettings' ||
        'updateInboundCredentials' => candidate.settingsSnapshot(),
        _ => candidate.routingSnapshot(),
      };
    }, subscriptionRefresh: method == 'refreshSubscription');
    if (refreshError != null) throw refreshError!;
    // The refresh removed the selected server and selected another; manual
    // and scheduled refreshes both say so, once.
    if (_catalog!.replacedSelection(selectedBeforeRefresh))
      _listener?.call('selected_server_replaced');
    // A refresh that succeeded, scheduled or not, restarts the scheduled one's backoff.
    if (method == 'refreshSubscription') {
      _refreshBackoff.remove('source:${args['id']}');
      // One that needed no review lets the next one that does say so again.
      if (result['warning'] == null) _reviewNoticed.remove(args['id']);
    }
    if (method == 'refreshRoutingGeofiles')
      _refreshBackoff.remove('geo:${args['id']}');
    if (method == 'refreshManagedLists')
      _refreshBackoff.remove('managed:${args['id']}');
    if (method == 'selectNode') result['state'] = _state;
    if (result.containsKey('profiles')) return _catalog!.routingSnapshot();
    return result;
  }

  Future<CatalogMap> _platformRequest(
    String method,
    CatalogMap payload,
  ) async => _platformIO != null
      ? _platformIO(method, payload)
      : await DesktopProxy._platform.invokeMapMethod<String, Object?>(
              method,
              payload,
            ) ??
            {};

  // The core sends lines already filtered and redacted; malformed ones are dropped.
  void _appendLogs(CatalogMap data) {
    for (final line in data['entries'] as List? ?? const []) {
      if (line is! Map ||
          line['time'] is! int ||
          !TunnelSettings.logLevels.contains(line['level']) ||
          line['text'] is! String)
        continue;
      _logs.add({
        'seq': ++_logSeq,
        'time': line['time'],
        'level': line['level'],
        'text': line['text'],
      });
    }
    if (_logs.length > 1000) _logs.removeRange(0, _logs.length - 1000);
  }

  // The core's list, relayed by its tunnel owner on Windows and Linux.
  void _appendConnections(CatalogMap data) {
    final epoch = data['epoch'];
    if (epoch is! int || epoch == 0) return;
    if (epoch != _connectionsEpoch) {
      _connectionsEpoch = epoch;
      _connections.clear();
    }
    for (final item in data['connections'] as List? ?? const []) {
      if (item is! Map || item['key'] is! int || item['seq'] is! int) continue;
      final key = item['key'] as int;
      _connections.remove(key);
      _connections[key] = catalogMap(item);
    }
    while (_connections.length > 500) {
      _connections.remove(_connections.keys.first);
    }
  }

  CatalogMap _runtimeMetrics() => {
    'schemaVersion': 1,
    'state': _state,
    'backend': _catalog!.settings.xrayTunEnabled ? 'xrayTun' : 'xrayProxy',
    'timestamp': (_metricsAt ?? DateTime.now()).millisecondsSinceEpoch / 1000,
    'metricsAvailable':
        _state == 'connected' &&
        _metricsAt != null &&
        // The core holds back unchanged metrics for up to 10 s at each of its two
        // relays (metricsHeartbeat in Vendor/libXray/cmd/elarni-core/main.go).
        DateTime.now().difference(_metricsAt!).inSeconds < 25,
    'connectedSince': _state == 'connected'
        ? _connectedAt?.millisecondsSinceEpoch
        : null,
    'counters': _state == 'connected' ? _latestMetrics : <String, Object?>{},
    'liveness': _state == 'connected' ? _liveness : null,
  };
}
