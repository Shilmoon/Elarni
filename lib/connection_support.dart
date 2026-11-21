import 'connection_diagnostics.dart';
import 'platform_services.dart';
import 'strings.dart';

export 'connection_diagnostics.dart';

/// Public support copy is selected by code, never by a native error's text.
final class ConnectionIssue {
  const ConnectionIssue(this.code, this.message, this.remedy);
  final String code, message, remedy;

  static ConnectionIssue from(Object error) {
    if (error is! PlatformException) return forCode('operation_failed');
    if (error.details case final Map details) {
      final diagnostic = details['diagnostics'] is Map
          ? details['diagnostics'] as Map
          : details;
      if (diagnostic['code'] == 'target_dns_failed')
        return forCode('target_dns_failed');
      if (diagnostic['code'] == 'timeout') return forCode('healthFailure');
      if (diagnostic['httpStatus'] case final num status when status != 0)
        return forCode('invalidTarget');
    }
    return forCode(error.code);
  }

  static ConnectionIssue forCode(String code) {
    final issue = supportIssueCode(code);
    final (message, remedy) = switch (issue) {
      'permission_denied' => (
        l10n.issuePermissionDenied,
        l10n.issuePermissionDeniedRemedy,
      ),
      'vpn_configuration_failed' => (
        l10n.issueVpnConfigurationFailed,
        l10n.issueVpnConfigurationFailedRemedy,
      ),
      'listener_unavailable' => (
        l10n.issueListenerUnavailable,
        l10n.issueListenerUnavailableRemedy,
      ),
      'check_target_failed' => (
        l10n.issueCheckTargetFailed,
        l10n.issueCheckTargetFailedRemedy,
      ),
      'connection_failed' => (
        l10n.issueConnectionFailed,
        l10n.issueConnectionFailedRemedy,
      ),
      'unsupported_policy' => (
        l10n.issueUnsupportedPolicy,
        l10n.issueUnsupportedPolicyRemedy,
      ),
      'recovery_exhausted' => (
        l10n.issueRecoveryExhausted,
        l10n.issueRecoveryExhaustedRemedy,
      ),
      'missing_credentials' => (
        l10n.issueMissingCredentials,
        l10n.issueMissingCredentialsRemedy,
      ),
      'invalid_recovery' => (
        l10n.issueInvalidRecovery,
        l10n.issueInvalidRecoveryRemedy,
      ),
      'invalid_configuration' => (
        l10n.issueInvalidConfiguration,
        l10n.issueInvalidConfigurationRemedy,
      ),
      'recovery_required' => (
        l10n.issueRecoveryRequired,
        l10n.issueRecoveryRequiredRemedy,
      ),
      'previous_restored' => (
        l10n.issuePreviousRestored,
        l10n.issuePreviousRestoredRemedy,
      ),
      'status_unavailable' => (
        l10n.issueStatusUnavailable,
        l10n.issueStatusUnavailableRemedy,
      ),
      'check_failed' => (l10n.issueCheckFailed, l10n.issueCheckFailedRemedy),
      'provider_unavailable' => (
        l10n.issueProviderUnavailable,
        l10n.issueProviderUnavailableRemedy,
      ),
      'disconnect_unconfirmed' => (
        l10n.issueDisconnectUnconfirmed,
        l10n.issueDisconnectUnconfirmedRemedy,
      ),
      'vpn_firewall_unavailable' => (
        l10n.issueVpnFirewallUnavailable,
        l10n.issueVpnFirewallUnavailableRemedy,
      ),
      'vpn_guard_busy' => (
        l10n.issueVpnGuardBusy,
        l10n.issueVpnGuardBusyRemedy,
      ),
      'vpn_guard_stale' => (
        l10n.issueVpnGuardStale,
        l10n.issueVpnGuardStaleRemedy,
      ),
      'busy' => (l10n.issueBusy, l10n.issueBusyRemedy),
      'storage_unavailable' => (
        l10n.issueStorageUnavailable,
        l10n.issueStorageUnavailableRemedy,
      ),
      'per_app_target_missing' => (
        l10n.issuePerAppTargetMissing,
        l10n.issuePerAppTargetMissingRemedy,
      ),
      'assets_missing' => (
        l10n.issueAssetsMissing,
        l10n.issueAssetsMissingRemedy,
      ),
      'missing_geodata' => (
        l10n.issueMissingGeodata,
        l10n.issueMissingGeodataRemedy,
      ),
      'invalid_route_enforcement' => (
        l10n.issueInvalidRouteEnforcement,
        l10n.issueInvalidRouteEnforcementRemedy,
      ),
      'route_model_too_large' => (
        l10n.issueRouteModelTooLarge,
        l10n.issueRouteModelTooLargeRemedy,
      ),
      'cancelled' => (l10n.issueCancelled, l10n.issueCancelledRemedy),
      _ => (l10n.issueOperationFailed, l10n.issueOperationFailedRemedy),
    };
    return ConnectionIssue(issue, message, remedy);
  }

  String get description => '$message $remedy';
}

/// The text for a failure [code], or null for a code without one of its own:
/// the generic connection text would misname a settings or download failure.
String? failureText(String code) => switch (code) {
  // Download failures that name no provider; the others need its name.
  'fetch_failed' => l10n.failureFetchText,
  'dns_failed' => l10n.failureDnsText,
  _ when supportIssueCode(code) != 'operation_failed' =>
    ConnectionIssue.forCode(code).description,
  _ => null,
};

/// What the desktop backend's notice code says. macOS sends text it worded
/// itself through the same listener, which is shown as it is.
String listenerNotice(String value) {
  const unsetProxy = 'system_proxy_not_set:';
  if (value.startsWith(unsetProxy)) {
    final port = int.tryParse(value.substring(unsetProxy.length));
    return port == null ? value : l10n.systemProxyNotSet(port);
  }
  return switch (value) {
    'links_failed' => l10n.clientLinksNotHandled,
    'link_failed' => l10n.clientLinkNotImported,
    'no_selected_subscription' => l10n.selectServerFirstRoutingLink,
    'scheduled_refresh_failed' => l10n.scheduledRefreshFailed,
    'per_app_target_missing' => l10n.perAppTargetGoneKept,
    'connection_stopped' => l10n.connectionStoppedUnexpectedly,
    'connection_stopped_blocked' => l10n.connectionStoppedBlocked,
    'connection_stopped_repair' => l10n.connectionStoppedRepair,
    'vpn_dns_release_failed' => l10n.windowsDnsNotRestored,
    'system_proxy_restore_failed' => l10n.systemProxyNotRestored,
    _ => failureText(value) ?? value,
  };
}
