/** user-announcements v1: immutable plain-text content; audience is admin-only. */
export interface Announcement {
  id: string;
  title: string;
  body: string;
  mode: "always" | "until_acknowledged";
  active: boolean;
  createdAt: string;
  recipientCount?: number;
  acknowledgedCount?: number;
  userIds?: string[];
}
export interface AnnouncementPage { items: Announcement[]; nextCursor: string }
export interface AnnouncementInput {
  id: string;
  title: string;
  body: string;
  mode: Announcement["mode"];
  userIds: string[];
}
export const announcementModeLabel = (mode: Announcement["mode"]) =>
  mode === "always" ? "每次加载都展示" : "已知晓后不再展示";
