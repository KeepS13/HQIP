typedef Json = Map<String, dynamic>;
Json object(dynamic value) =>
    value is Map ? Map<String, dynamic>.from(value) : {};
List<Json> objects(dynamic value) => value is List
    ? value.whereType<Map>().map(object).toList(growable: false)
    : [];
num number(dynamic value) => value is num ? value : 0;

class Subscription {
  Subscription(Json data) : raw = Map.unmodifiable(data);
  final Json raw;
  String get id => raw['id'] as String? ?? '';
  String get name => raw['templateName'] as String? ?? '订阅';
  int get used => number(raw['usedBytes']).toInt();
  int? get quota => (raw['quotaBytes'] as num?)?.toInt();
  int get reserved => number(raw['reservedBytes']).toInt();
  DateTime? get expires =>
      DateTime.tryParse(raw['expiresAt'] as String? ?? '')?.toLocal();
  bool get valid =>
      raw['status'] == 'active' &&
      (expires?.isAfter(DateTime.now()) ?? false) &&
      (quota == null || used < quota!);
  double get fraction =>
      quota == null || quota! <= 0 ? 0 : (used / quota!).clamp(0, 1);
}

class NodeChoice {
  const NodeChoice(this.id, this.subscriptionId, this.name, this.ready);
  factory NodeChoice.fromJson(Json data) => NodeChoice(
    data['nodeId'] as String? ?? '',
    data['subscriptionId'] as String? ?? '',
    data['name'] as String? ?? '节点',
    data['ready'] == true,
  );
  final String id, subscriptionId, name;
  final bool ready;
  String get key => '$subscriptionId/$id';
}

class Snapshot {
  Snapshot(Json data) : raw = Map.unmodifiable(data);
  final Json raw;
  int get sequence => number(raw['snapshotSequence']).toInt();
  bool get loggedIn => raw['loggedIn'] == true;
  bool get loginPending => raw['loginPending'] == true;
  bool get intent => raw['connectionIntent'] == true;
  bool get selectionPending => raw['selectionPending'] == true;
  bool get tun => raw['tunEnabled'] == true;
  String get runState => raw['runState'] as String? ?? 'disconnected';
  String get mode => raw['mode'] as String? ?? 'rule';
  String get version => raw['version'] as String? ?? '—';
  String get device => raw['deviceName'] as String? ?? '我的设备';
  String get server =>
      raw['serverUrl'] as String? ?? 'https://hqip.sharevps.online';
  String get selectedId =>
      raw['desiredNodeId'] as String? ?? raw['defaultNodeId'] as String? ?? '';
  String get selectedSubscription =>
      raw['desiredSubscriptionId'] as String? ??
      object(raw['subscription'])['id'] as String? ??
      '';
  String get actualSubscription => raw['actualSubscriptionId'] as String? ?? '';
  String get actualId => raw['actualNodeId'] as String? ?? '';
  String get error => (raw['lastError'] as String? ?? '').isNotEmpty
      ? raw['lastError'] as String
      : raw['syncError'] as String? ?? '';
  Json get metrics => object(raw['metrics']);
  Json get updates => object(raw['updates']);
  Json get autoStart => object(raw['autoStart']);
  Json get nodeTests => object(raw['nodeTests']);
  List<Subscription> get subscriptions =>
      objects(raw['subscriptions'])
          .map(Subscription.new)
          .toList(growable: false);
  List<NodeChoice> get choices =>
      objects(raw['nodeChoices'])
          .map(NodeChoice.fromJson)
          .toList(growable: false);
  NodeChoice? choice(String node, String subscription) {
    for (final value in choices) {
      if (value.id == node && value.subscriptionId == subscription) {
        return value;
      }
    }
    return null;
  }

  Subscription? package(String id) {
    for (final value in subscriptions) {
      if (value.id == id) return value;
    }
    return null;
  }
}

String bytes(num value) {
  var amount = value.toDouble();
  for (final unit in ['B', 'KB', 'MB', 'GB', 'TB']) {
    if (amount < 1000 || unit == 'TB') {
      return '${amount.toStringAsFixed(amount < 10 && unit != 'B' ? 1 : 0)} $unit';
    }
    amount /= 1000;
  }
  return '—';
}

String date(DateTime? value) => value == null
    ? '—'
    : '${value.year}/${value.month.toString().padLeft(2, '0')}/${value.day.toString().padLeft(2, '0')}';
