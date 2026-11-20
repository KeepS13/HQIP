// Keep this policy aligned with internal/netutil/public_ipv4.go.
const excluded: [string, number][] = [
  ["0.0.0.0", 8], ["10.0.0.0", 8], ["100.64.0.0", 10],
  ["127.0.0.0", 8], ["169.254.0.0", 16], ["172.16.0.0", 12],
  ["192.0.0.0", 24], ["192.0.2.0", 24], ["192.88.99.0", 24],
  ["192.168.0.0", 16], ["198.18.0.0", 15], ["198.51.100.0", 24],
  ["203.0.113.0", 24], ["224.0.0.0", 4], ["240.0.0.0", 4],
];
const numeric = (ip: string) => ip.split(".").reduce((value, part) => value * 256 + Number(part), 0);

export function isPublicIPv4(value: string): boolean {
  const ip = value.trim();
  if (!/^(0|[1-9]\d{0,2})(\.(0|[1-9]\d{0,2})){3}$/.test(ip)) return false;
  if (ip.split(".").some(part => Number(part) > 255)) return false;
  const address = numeric(ip);
  return !excluded.some(([base, bits]) => Math.floor(address / 2 ** (32 - bits)) === Math.floor(numeric(base) / 2 ** (32 - bits)));
}
