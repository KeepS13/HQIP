import 'dart:convert';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../runtime/session.dart';

// Native snapshots arrive every second. Traffic and node probes subscribe
// separately, so their live data does not rebuild the static page content.
final uiStateProvider = Provider<AppState>((ref) {
  ref.watch(
    sessionProvider.select(
      (app) => (
        app.ready,
        app.error,
        app.draft?.key,
        app.draft?.name,
        app.draft?.ready,
        jsonEncode(app.snapshot.subscriptions.map((s) => s.valid).toList()),
        jsonEncode(app.busy.toList()..sort()),
        jsonEncode({
          for (final key in const [
            'loggedIn',
            'loginPending',
            'connectionIntent',
            'selectionPending',
            'tunEnabled',
            'runState',
            'mode',
            'version',
            'deviceName',
            'serverUrl',
            'desiredNodeId',
            'defaultNodeId',
            'desiredSubscriptionId',
            'subscription',
            'actualNodeId',
            'actualSubscriptionId',
            'lastError',
            'syncError',
            'subscriptions',
            'nodeChoices',
            'updates',
            'autoStart',
          ])
            key: app.snapshot.raw[key],
        }),
      ),
    ),
  );
  return ref.read(sessionProvider);
});
