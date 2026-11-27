import { useState, type FormEvent } from "react";
import { LockKeyhole } from "lucide-react";
import { api } from "./api";
import { Button, ErrorNotice, Field, Notice } from "./components";

export function PasswordForm({
  onDone,
  forced = false,
}: {
  onDone: () => Promise<void>;
  forced?: boolean;
}) {
  const [current, setCurrent] = useState(""),
    [password, setPassword] = useState(""),
    [confirm, setConfirm] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (password !== confirm) {
      setError("两次输入的新密码不一致");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await api("/password", "POST", {
        currentPassword: current,
        newPassword: password,
      });
      await onDone();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <form className="form-stack" onSubmit={submit}>
      {forced && (
        <Notice>首次登录需要设置新的管理密码，完成后即可进入控制台。</Notice>
      )}
      <Field label="当前密码">
        <input
          type="password"
          autoComplete="current-password"
          required
          value={current}
          onChange={(e) => setCurrent(e.target.value)}
          placeholder="输入当前管理密码"
        />
      </Field>
      <Field label="新密码" hint="至少 12 位，建议使用字母、数字与符号的组合。">
        <input
          type="password"
          autoComplete="new-password"
          required
          minLength={12}
          maxLength={128}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          placeholder="设置新的管理密码"
        />
      </Field>
      <Field label="确认新密码">
        <input
          type="password"
          autoComplete="new-password"
          required
          minLength={12}
          maxLength={128}
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
          placeholder="再次输入新密码"
        />
      </Field>
      <ErrorNotice message={error} />
      <Button
        type="submit"
        busy={busy}
        icon={LockKeyhole}
        className="full-width"
      >
        {forced ? "保存并进入控制台" : "保存新密码"}
      </Button>
    </form>
  );
}
