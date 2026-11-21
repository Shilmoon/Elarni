import 'dart:async';
import 'dart:ui' show AppExitResponse;

import 'package:flutter_test/flutter_test.dart';
import 'package:elarni/main.dart' show quitDesktop;

void main() {
  test(
    'a quit gives the proxy back, then waits for the disconnect before it exits',
    () async {
      final calls = <String>[];
      final released = Completer<void>(), closed = Completer<void>();
      var answered = false;
      final response =
          quitDesktop(
            () {
              calls.add('release');
              return released.future;
            },
            () {
              calls.add('close');
              return closed.future;
            },
          ).then((value) {
            answered = true;
            return value;
          });
      await pumpEventQueue();
      expect(calls, ['release']);
      released.complete();
      await pumpEventQueue();
      expect(calls, ['release', 'close']);
      expect(answered, isFalse);
      closed.complete();
      expect(await response, AppExitResponse.exit);
    },
  );

  test(
    'a disconnect that hangs past the timeout still lets the app exit',
    () async {
      final calls = <String>[];
      final response = await quitDesktop(() async => calls.add('release'), () {
        calls.add('close');
        return Completer<void>().future;
      }, timeout: const Duration(milliseconds: 20));
      expect(calls, ['release', 'close']);
      expect(response, AppExitResponse.exit);
    },
  );

  test('a disconnect that fails still lets the app exit', () async {
    final calls = <String>[];
    final response = await quitDesktop(
      () async => calls.add('release'),
      () async {
        calls.add('close');
        throw StateError('core gone');
      },
    );
    expect(calls, ['release', 'close']);
    expect(response, AppExitResponse.exit);
  });
}
