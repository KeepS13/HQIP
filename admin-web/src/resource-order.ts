const resourceNameCollator = new Intl.Collator("zh-CN", {
  numeric: true,
  sensitivity: "base",
});

export function compareResourceNames(a: { id: string; name: string }, b: { id: string; name: string }): number {
  const byName = resourceNameCollator.compare(a.name.trim(), b.name.trim());
  // Keep equal names in a stable order when polling returns a different order.
  return byName || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0);
}
