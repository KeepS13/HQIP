import { useMemo, useState } from "react";
import { Button, Field } from "../components";
import type { PortalUser } from "../portal/types";

/** Selection owns explicit IDs, including filtered-out rows. Batch selection
 * applies to the full search result, not just the 100 currently rendered rows. */
export function AnnouncementAudience({ users, selected, onChange, disabled }: {
  users: PortalUser[]; selected: string[]; onChange: (ids: string[]) => void; disabled: boolean;
}) {
  const [search, setSearch] = useState(""), [limit, setLimit] = useState(100);
  const filtered = useMemo(() => users.filter(user => `${user.email} ${user.id}`.toLowerCase().includes(search.trim().toLowerCase())), [users, search]);
  const chosen = new Set(selected);
  function toggle(id: string) {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id); else next.add(id);
    onChange([...next]);
  }
  return <fieldset className="announcement-audience" disabled={disabled}>
    <legend>接收用户 · 已选 {selected.length} 人</legend>
    <Field label="搜索用户"><input type="search" placeholder="邮箱或用户 ID" value={search} onChange={e => { setSearch(e.target.value); setLimit(100); }} /></Field>
    <div className="announcement-select-actions">
      <Button variant="secondary" type="button" disabled={disabled || !filtered.length} onClick={() => onChange([...new Set([...selected, ...filtered.map(u => u.id)])])}>全选搜索结果（{filtered.length}）</Button>
      <Button variant="secondary" type="button" disabled={disabled || !selected.length} onClick={() => onChange([])}>清空选择</Button>
    </div>
    <div className="announcement-user-list">
      {filtered.slice(0, limit).map(user => <label key={user.id}>
        <input type="checkbox" checked={chosen.has(user.id)} onChange={() => toggle(user.id)} />
        <span>{user.email}<small>{user.enabled ? user.id : `${user.id} · 账号已停用`}</small></span>
      </label>)}
      {!filtered.length && <p className="muted">没有匹配的用户</p>}
    </div>
    {limit < filtered.length && <Button variant="secondary" type="button" onClick={() => setLimit(limit + 100)}>显示更多用户</Button>}
  </fieldset>;
}
