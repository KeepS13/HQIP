import type { InputHTMLAttributes } from "react";
import { dateRangeShortcut, expiryDateShortcut, type DateRange, type DateRangePreset } from "./date-shortcuts";
import "./date-picker.css";

function openPicker(input: HTMLInputElement) {
  if (!input.showPicker || input.disabled || input.readOnly) return false;
  try {
    input.showPicker();
    return true;
  } catch {
    return false; // Native typing remains available if the picker is unsupported or blocked.
  }
}

export function DateInput({ onClick, onKeyDown, className = "", ...props }: Omit<InputHTMLAttributes<HTMLInputElement>, "type">) {
  return <input {...props} type="date" className={`date-picker-input ${className}`.trim()}
    onClick={event => { onClick?.(event); if (!event.defaultPrevented) openPicker(event.currentTarget); }}
    onKeyDown={event => {
      onKeyDown?.(event);
      if (!event.defaultPrevented && (event.key === "Enter" || event.key === " ") && openPicker(event.currentTarget)) event.preventDefault();
    }} />;
}

const ranges: [DateRangePreset, string][] = [["today", "今天"], ["yesterday", "昨天"], ["last7", "最近 7 天"], ["last30", "最近 30 天"], ["month", "本月"]];

export function DateRangeShortcuts({ value, onChange }: { value: DateRange; onChange: (value: DateRange) => void }) {
  return <div className="date-shortcuts" role="group" aria-label="快捷选择订单日期">
    <span>快捷选择</span>
    {ranges.map(([preset, label]) => {
      const range = dateRangeShortcut(preset);
      return <button type="button" key={preset} aria-pressed={value.from === range.from && value.to === range.to} onClick={() => onChange(dateRangeShortcut(preset))}>{label}</button>;
    })}
    <button type="button" onClick={() => onChange({ from: "", to: "" })}>清除日期</button>
    <small>选择后点击“筛选”</small>
  </div>;
}

export function ExpiryShortcuts({ day, hour, onChange }: { day: string; hour: string; onChange: (value: { day: string; hour: string }) => void }) {
  const choices = [[0, "今天"], [1, "明天"], [7, "7 天后"], [30, "30 天后"]] as const;
  return <div className="date-shortcuts" role="group" aria-label="快捷设置到期时间">
    <span>快捷选择</span>
    {choices.map(([days, label]) => {
      const value = expiryDateShortcut(days, hour);
      return <button type="button" key={days} disabled={!value} aria-pressed={!!value && value.day === day && value.hour === hour} title={!value ? "今天已无可选的未来整点，请选择明天或之后" : undefined} onClick={() => {
        const next = expiryDateShortcut(days, hour);
        if (next) onChange(next);
      }}>{label}</button>;
    })}
    <button type="button" onClick={() => onChange({ day: "", hour: "" })}>清除</button>
  </div>;
}
