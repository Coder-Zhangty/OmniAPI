export type ArchiveRecord = {
  id: string;
  user_id: number;
  created: number;
  updated: number;
  revision: number;
  model: string;
  upstream_model: string;
  project_id: string;
  conversation_id: string;
  status: string;
};
export type Message = {
  role: string;
  text: string;
  thinking?: string;
  tools?: string;
  attachments?: Attachment[];
  choice?: number;
  raw?: string;
};
export type Attachment = { name: string; type: string; data?: string; url?: string };
export type View = {
  messages: Message[];
  title: string;
  preview: string;
  unknown: boolean;
  error: string;
};
export type Decoded = {
  record: ArchiveRecord;
  request: Record<string, any>;
  final: Record<string, any>;
  inputText: string;
  outputText: string;
  seq: number;
  previous: string;
  bytes: number;
  view: View;
};
export type Filters = Record<string, string>;
export type DirectoryGroup = {
  user_id: number;
  project_id: string;
  conversation_id: string;
  count: number;
  latest: number;
  pending: number;
};
