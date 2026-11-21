import 'dart:async';
import 'dart:io';
import 'dart:ui' show AppExitResponse;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

import 'elarni_app.dart';
import 'desktop_proxy.dart';
import 'desktop_camera.dart';
import 'headless.dart';
import 'system_proxy.dart';
import 'strings.dart';

Future<void> main(List<String> arguments) async {
  WidgetsFlutterBinding.ensureInitialized();
  final headless = Platform.environment['ELARNI_HEADLESS'] == '1';
  if (!headless)
    SystemChrome.setApplicationSwitcherDescription(
      const ApplicationSwitcherDescription(label: 'Elarni'),
    );
  final navigator = GlobalKey<NavigatorState>();
  final camera = DesktopCamera(navigator, linux: Platform.isLinux);
  final desktop = Platform.isWindows || Platform.isLinux
      ? DesktopProxy(
          unattended: headless,
          // The daemon has no desktop session whose proxy it could set.
          systemProxy: headless
              ? null
              : SystemProxySession.forPlatform(Platform.environment),
          cameraScan: camera.scan,
          cancelCamera: camera.cancel,
          confirmLink: headless
              ? null
              : (prompt) => _confirmLink(navigator, prompt),
          corePath: File(Platform.resolvedExecutable).parent.uri
              .resolve(Platform.isLinux ? 'elarni-core' : 'elarni-core.exe')
              .toFilePath(),
          domainPath: File(Platform.resolvedExecutable).parent.uri
              .resolve(
                Platform.isLinux
                    ? 'lib/libelarni_domain.so'
                    : 'elarni_domain.dll',
              )
              .toFilePath(),
        )
      : null;
  final done = Completer<void>();
  // Binding runs a Windows ACL check that can take seconds; the GUI starts first.
  // Future.sync also routes a missing HOME or LOCALAPPDATA to the handler below.
  final control =
      Future.sync(
        () => HeadlessServer.bind(
          headlessSocketPath,
          HeadlessControl(
            desktop != null
                ? desktop.request
                : (method, args) async =>
                      await const MethodChannel(
                        'com.elarni/tunnel',
                      ).invokeMapMethod<String, Object?>(method, args) ??
                      {},
            unattended: headless && desktop != null,
          ).request,
          onShutdown: headless
              ? () {
                  if (!done.isCompleted) done.complete();
                }
              : null,
        ),
      ).then<HeadlessServer?>(
        (server) => server,
        onError: (Object _) {
          // A control endpoint failure must not prevent ordinary GUI startup.
          stderr.writeln('Elarni local control unavailable.');
          return null;
        },
      );
  if (headless) {
    final server = await control;
    try {
      if (server == null) throw StateError('control_unavailable');
      if (desktop != null) {
        final started = await desktop.request('state');
        // No window shows the notices; the daemon's output gets them.
        logHeadlessNotices(
          desktop.control,
          desktop.request,
          stderr.writeln,
          state: started['state'],
        );
      } else {
        await const MethodChannel(
          'com.elarni/tunnel',
        ).invokeMethod<Object?>('state');
      }
    } on Object {
      if (server != null) stderr.writeln('Elarni local control unavailable.');
      try {
        await server?.close();
      } finally {
        await desktop?.dispose();
        exit(1);
      }
    }
    final signals = [
      for (final signal in [
        ProcessSignal.sigint,
        if (!Platform.isWindows) ProcessSignal.sigterm,
      ])
        signal.watch().listen((_) {
          if (!done.isCompleted) done.complete();
        }),
    ];
    stderr.writeln('Elarni headless control ready.');
    await done.future;
    try {
      await server.close();
    } on Object {
      // A leftover endpoint file must not keep the VPN up.
      exitCode = 1;
    }
    if (desktop != null) {
      await desktop.dispose();
    } else {
      try {
        await const MethodChannel(
          'com.elarni/tunnel',
        ).invokeMethod<void>('disconnect');
      } catch (_) {
        stderr.writeln('Elarni disconnect failed.');
        exitCode = 1;
      }
    }
    for (final signal in signals) {
      await signal.cancel();
    }
    exit(exitCode);
  }
  // Once: a quit, of the window or for an update, closes first, then the
  // window's dispose follows.
  Future<void>? closing;
  Future<void> close({bool keepBlock = false}) => closing ??= () async {
    try {
      await (await control)?.close();
    } finally {
      await desktop?.dispose(keepBlock: keepBlock);
    }
  }();
  if (desktop != null)
    AppLifecycleListener(
      onExitRequested: () => quitDesktop(desktop.releaseSystemProxy, close),
    );
  runApp(
    ElarniApp(
      tunnelControl: desktop?.control,
      navigatorKey: navigator,
      windows: Platform.isWindows,
      linux: Platform.isLinux,
      onDispose: close,
      onQuit: desktop == null
          ? null
          : ({required bool restart}) async {
              try {
                // Block traffic without VPN covers the gap until the new
                // build connects.
                await close(keepBlock: true);
              } on Object {
                // The update needs this process gone, closed cleanly or not.
              }
              // Windows' installer starts the new build itself.
              if (restart && Platform.isLinux)
                try {
                  await Process.start('/bin/sh', [
                    '-c',
                    r'while kill -0 "$1" 2>/dev/null; do sleep 0.2; done; exec "$2"',
                    'sh',
                    '$pid',
                    relaunchTarget(
                      Platform.environment,
                      Platform.resolvedExecutable,
                    ),
                  ], mode: ProcessStartMode.detached);
                } on Object {
                  stderr.writeln('Elarni could not start again.');
                }
              exit(0);
            },
    ),
  );
  if (desktop != null) unawaited(desktop.handleLinks(arguments));
}

/// Answers a quit of the window on Windows and Linux: puts the user's proxy
/// setting back, then disconnects, which stops the VPN helper normally and lifts
/// Block traffic without VPN. A disconnect that fails or takes longer than
/// [timeout] does not keep the app from quitting.
Future<AppExitResponse> quitDesktop(
  Future<void> Function() releaseSystemProxy,
  Future<void> Function() close, {
  Duration timeout = const Duration(seconds: 8),
}) async {
  await releaseSystemProxy();
  try {
    await close().timeout(timeout);
  } on Object {
    // A core that does not answer must not keep the app from quitting.
  }
  return AppExitResponse.exit;
}

/// What starts this build again after an update: the AppImage it runs from,
/// which the update replaced, or else its own executable. The AppImage runtime
/// sets APPIMAGE to the image and APPDIR to its mount, which holds [executable].
String relaunchTarget(
  Map<String, String> environment,
  String executable, {
  bool Function(String path) isFile = FileSystemEntity.isFileSync,
}) {
  final image = environment['APPIMAGE'] ?? '';
  final mount = (environment['APPDIR'] ?? '').replaceFirst(RegExp(r'/+$'), '');
  return image.startsWith('/') &&
          mount.startsWith('/') &&
          executable.startsWith('$mount/') &&
          isFile(image)
      ? image
      : executable;
}

Future<bool> _confirmLink(
  GlobalKey<NavigatorState> navigator,
  String prompt,
) async {
  final (message, action) = switch (prompt) {
    'disconnect' => (l10n.linkAsksDisconnect, l10n.disconnect),
    'importRouting' => (l10n.linkImportsRoutingProfile, l10n.importAction),
    _ => (l10n.linkImportsClientProfile, l10n.importAction),
  };
  // Links passed at launch arrive before the first frame builds the navigator.
  while (navigator.currentContext == null) {
    await WidgetsBinding.instance.endOfFrame;
  }
  return await showDialog<bool>(
        context: navigator.currentContext!,
        builder: (context) => AlertDialog(
          title: Text(l10n.openElarniLink),
          content: Text(message),
          actions: <Widget>[
            TextButton(
              onPressed: () => Navigator.pop(context, false),
              child: Text(l10n.cancel),
            ),
            FilledButton(
              onPressed: () => Navigator.pop(context, true),
              child: Text(action),
            ),
          ],
        ),
      ) ==
      true;
}
