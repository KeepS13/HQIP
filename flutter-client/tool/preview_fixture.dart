// Deterministic, offline data for layout tests and the optional UI preview.
// The production entrypoint never imports this file.
import 'dart:async';

import 'package:hqip_preview/runtime/models.dart';
import 'package:hqip_preview/runtime/transport.dart';

class PreviewTransport implements RuntimeTransport {
  PreviewTransport({
    bool loggedIn = true,
    Json overrides = const {},
    this.startGate,
  }) {
    data = {
      'loggedIn': loggedIn,
      'runState': 'disconnected',
      'connectionIntent': false,
      'desiredSubscriptionId': 'standard',
      'desiredNodeId': 'tokyo',
      'version': '0.1.0-preview.2',
      'deviceName': '我的设备',
      'serverUrl': 'https://hqip.sharevps.online',
      'mode': 'rule',
      'tunEnabled': false,
      'autoStart': {'supported': true, 'enabled': false},
      'subscriptions': loggedIn
          ? [
              {
                'id': 'standard',
                'templateName': '标准订阅',
                'status': 'active',
                'expiresAt': '2099-12-31T00:00:00Z',
                'quotaBytes': 100000000000,
                'usedBytes': 18600000000,
                'occupied': 1,
                'maxConnections': 3,
              },
              {
                'id': 'travel',
                'templateName': '备用订阅',
                'status': 'active',
                'expiresAt': '2099-12-31T00:00:00Z',
                'quotaBytes': 50000000000,
                'usedBytes': 4200000000,
                'occupied': 0,
                'maxConnections': 2,
              },
            ]
          : [],
      'nodeChoices': loggedIn
          ? [
              {
                'nodeId': 'tokyo',
                'subscriptionId': 'standard',
                'name': '日本 · 东京 01',
                'ready': true,
              },
              {
                'nodeId': 'singapore',
                'subscriptionId': 'standard',
                'name': '新加坡 · 01',
                'ready': true,
              },
              {
                'nodeId': 'offline',
                'subscriptionId': 'standard',
                'name': '香港 · 维护中',
                'ready': false,
              },
              {
                'nodeId': 'tokyo',
                'subscriptionId': 'travel',
                'name': '日本 · 东京 02',
                'ready': true,
              },
            ]
          : [],
      'metrics': {
        'available': false,
        'uploadBytes': 28000000,
        'downloadBytes': 164000000,
        'history': [],
      },
      'updates': {'phase': 'idle'},
      ...overrides,
    };
  }
  late Json data;
  int sequence = 0;
  final Completer<void>? startGate;
  final controller = StreamController<Json>.broadcast();
  final requests = <({String method, Json params})>[];
  Json snapshot() => {...data, 'snapshotSequence': ++sequence};
  void emit(Json changes) {
    data = {...data, ...changes};
    controller.add(snapshot());
  }

  @override
  Stream<Json> get events => controller.stream;
  @override
  Future<void> start() async {
    await startGate?.future;
  }

  @override
  Future<void> watch(bool enabled) async {
    requests.add((method: enabled ? 'watch' : 'unwatch', params: {}));
  }

  @override
  Future<void> close() => controller.close();
  @override
  Future<Json> call(String method, [Json params = const {}]) async {
    requests.add((method: method, params: params));
    switch (method) {
      case 'login':
        data['loginPending'] = true;
      case 'cancelLogin':
        data['loginPending'] = false;
      case 'node':
        data.addAll({
          'desiredNodeId': params['nodeId'],
          'desiredSubscriptionId': params['subscriptionId'],
        });
      case 'connect':
        data.addAll({
          'connectionIntent': true,
          'runState': 'connected',
          'actualNodeId': data['desiredNodeId'],
          'actualSubscriptionId': data['desiredSubscriptionId'],
        });
      case 'disconnect':
        data.addAll({'connectionIntent': false, 'runState': 'disconnected'});
      case 'mode':
        data.addAll({'mode': params['mode'], 'tunEnabled': params['tun']});
      case 'autoStart':
        data['autoStart'] = {'supported': true, 'enabled': params['enabled']};
      case 'logout':
        data.addAll({
          'loggedIn': false,
          'subscriptions': [],
          'nodeChoices': [],
        });
    }
    return {'result': snapshot()};
  }
}
