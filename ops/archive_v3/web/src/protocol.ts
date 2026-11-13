import type { Message, View, Attachment } from "./types";
type Data = Record<string, any>;
const parse = (text: string): Data | null => {
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
};
function parts(content: any): {
  text: string;
  thinking: string;
  tools: string;
  attachments: Attachment[];
} {
  const result = { text: "", thinking: "", tools: "", attachments: [] as Attachment[] };
  if (typeof content === "string") return { ...result, text: content };
  for (const item of Array.isArray(content) ? content : content ? [content] : []) {
    if (item.text && !item.thought) result.text += item.text;
    if (item.thinking || item.thought) result.thinking += item.thinking || item.text || "";
    if (
      ["tool_use", "tool_result"].includes(item.type) ||
      item.functionCall ||
      item.functionResponse ||
      item.functionResponse
    )
      result.tools +=
        JSON.stringify(item.functionCall || item.functionResponse || item, null, 2) + "\n";
    const image =
      item.image_url?.url ||
      item.image_url ||
      (item.type === "input_image" ? item.image_url : null);
    if (typeof image === "string") {
      const match = image.match(/^data:([^;,]+);base64,(.*)$/s);
      result.attachments.push(
        match
          ? { name: "图片", type: match[1], data: match[2] }
          : { name: "外部图片（未归档）", type: "external", url: image },
      );
    }
    if (item.source?.type === "base64")
      result.attachments.push({
        name: item.title || "附件",
        type: item.source.media_type || "application/octet-stream",
        data: item.source.data,
      });
    if (item.inlineData)
      result.attachments.push({
        name: "附件",
        type: item.inlineData.mimeType,
        data: item.inlineData.data,
      });
    if (item.fileData)
      result.attachments.push({
        name: "外部文件（未归档）",
        type: "external",
        url: item.fileData.fileUri,
      });
    if (item.type === "input_file" && item.file_data) {
      const match = item.file_data.match(/^data:([^;,]+);base64,(.*)$/s);
      result.attachments.push({
        name: item.filename || "文件",
        type: match?.[1] || "application/octet-stream",
        data: match?.[2] || item.file_data,
      });
    }
  }
  return result;
}
function message(item: Data): Message {
  const body = parts(item.content ?? item.parts ?? item.text ?? item.output ?? "");
  return {
    role:
      item.role === "model"
        ? "assistant"
        : item.role ||
          (["function_call", "function_call_output", "tool_result"].includes(item.type)
            ? "tool"
            : "user"),
    ...body,
    thinking: body.thinking || item.reasoning_content || item.reasoning || "",
    tools:
      body.tools ||
      (Array.isArray(item.tool_calls) && item.tool_calls.length
        ? JSON.stringify(item.tool_calls, null, 2)
        : ["function_call", "function_call_output"].includes(item.type)
          ? JSON.stringify(item, null, 2)
          : ""),
    raw: item.type === "tool_result" ? JSON.stringify(item, null, 2) : undefined,
  };
}
function normalizeUnsafe(inputText: string, outputText: string, format = "json"): View {
  if (format === "websocket-wire") {
    const incoming = parse(inputText)?.websocket_events || [],
      outgoing = parse(outputText)?.websocket_events || [];
    const messages = incoming.flatMap((event) =>
      event.type === "conversation.item.create"
        ? [event.item]
        : event.type === "response.create"
          ? event.response?.input || []
          : [],
    );
    const result = normalize(
      JSON.stringify({ messages }),
      outgoing.map((event) => "data: " + JSON.stringify(event) + "\n\n").join(""),
      "json",
    );
    if (
      [...incoming, ...outgoing].some(
        (event) => event.binary_bytes || event.unsupported_compressed_bytes || event.raw_text,
      )
    ) {
      result.unknown = true;
    }
    return result;
  }
  const input = parse(inputText),
    output = parse(outputText),
    messages: Message[] = [];
  let unknown = false,
    error = "";
  if (format === "multipart-json" && input) {
    const files: Attachment[] = [],
      fields: string[] = [];
    for (const part of input.parts || [])
      if (part.filename)
        files.push({ name: part.filename, type: part.content_type, data: part.data });
      else {
        try {
          fields.push(
            part.name +
              ": " +
              new TextDecoder().decode(Uint8Array.from(atob(part.data), (c) => c.charCodeAt(0))),
          );
        } catch {
          fields.push(part.name);
        }
      }
    messages.push({ role: "user", text: fields.join("\n"), attachments: files });
  } else if (input) {
    if (input.system) messages.push({ role: "system", ...parts(input.system) });
    if (input.systemInstruction)
      messages.push({ role: "system", ...parts(input.systemInstruction.parts) });
    const source =
      input.messages || input.contents || (Array.isArray(input.input) ? input.input : null);
    if (source) for (const item of source) messages.push(message(item));
    else if (typeof input.input === "string" || typeof input.prompt === "string")
      messages.push({ role: "user", text: input.input || input.prompt });
    else if (inputText) {
      unknown = true;
      messages.push({ role: "user", text: "此协议暂不能转换为聊天消息，请查看技术详情。" });
    }
  } else if (inputText) {
    unknown = true;
    messages.push({ role: "user", text: "输入仍在接收中，或为未识别的格式。" });
  }
  const append = (value: Data, index = 0) => {
    const m = message({ ...value, role: value.role || "assistant" });
    messages.push({ ...m, choice: index });
  };
  if (output) {
    if (output.error)
      error =
        typeof output.error === "string"
          ? output.error
          : output.error.message || JSON.stringify(output.error);
    else if (output.choices)
      for (const choice of output.choices)
        append(choice.message || { content: choice.text || "" }, choice.index || 0);
    else if (output.candidates)
      for (const candidate of output.candidates)
        append(candidate.content || {}, candidate.index || 0);
    else if (output.output)
      for (const item of output.output) {
        if (item.type === "message") append(item);
        else if (item.type === "function_call")
          messages.push({ role: "tool", text: "", tools: JSON.stringify(item, null, 2) });
        else if (item.type === "reasoning")
          messages.push({
            role: "assistant",
            text: "",
            thinking: (item.summary || []).map((x) => x.text || "").join("\n"),
          });
      }
    else if (output.content) append(output);
    else if (outputText) unknown = true;
  } else if (/^(data:|event:|:)/m.test(outputText)) {
    const choices = new Map<number, Message>();
    const get = (index = 0) => {
      if (!choices.has(index))
        choices.set(index, { role: "assistant", text: "", thinking: "", tools: "", choice: index });
      return choices.get(index)!;
    };
    for (const block of outputText.split(/\r?\n\r?\n/)) {
      const payload = block
        .split(/\r?\n/)
        .filter((line) => line.startsWith("data:"))
        .map((line) => line.slice(5).trimStart())
        .join("\n");
      if (!payload || payload === "[DONE]") continue;
      const event = parse(payload);
      if (!event) continue;
      if (event.error) error = event.error.message || "模型返回错误";
      if (event.choices)
        for (const choice of event.choices) {
          const m = get(choice.index || 0),
            d = choice.delta || {};
          m.text += d.content || choice.text || "";
          m.thinking += d.reasoning_content || d.reasoning || "";
          if (Array.isArray(d.tool_calls) && d.tool_calls.length) m.tools += JSON.stringify(d.tool_calls) + "\n";
        }
      else if (event.candidates)
        for (const candidate of event.candidates) {
          const m = get(candidate.index || 0),
            p = parts(candidate.content?.parts);
          m.text += p.text;
          m.thinking += p.thinking;
          m.tools += p.tools;
          m.attachments = [...(m.attachments || []), ...p.attachments];
        }
      else if (
        [
          "response.output_text.delta",
          "response.text.delta",
          "response.audio_transcript.delta",
        ].includes(event.type)
      )
        get(event.output_index || 0).text += event.delta || "";
      else if (event.type === "response.reasoning_summary_text.delta")
        get().thinking += event.delta || "";
      else if (event.type === "response.function_call_arguments.delta")
        get().tools += event.delta || "";
      else if (event.type === "content_block_delta") {
        const d = event.delta || {};
        get().text += d.text || "";
        get().thinking += d.thinking || "";
        get().tools += d.partial_json || "";
      } else if (event.type === "content_block_start" && event.content_block?.type === "tool_use")
        get().tools += JSON.stringify(event.content_block) + "\n";
      else if (event.type === "response.failed")
        error = event.response?.error?.message || "请求失败";
    }
    if (outputText.trim() && !choices.size && !error) unknown = true;
    messages.push(
      ...[...choices.values()].filter(
        (m) => m.text || m.thinking || m.tools || m.attachments?.length,
      ),
    );
  } else if (outputText) {
    unknown = true;
  }
  const user = messages.find((item) => item.role === "user" && item.text.trim());
  const title = (
    user?.text ||
    messages.find((item) => item.attachments?.length)?.attachments?.[0]?.name ||
    "未命名请求"
  )
    .replace(/\s+/g, " ")
    .slice(0, 48);
  const preview = (
    messages.filter((item) => item.role === "assistant").at(-1)?.text ||
    error ||
    user?.text ||
    ""
  )
    .replace(/\s+/g, " ")
    .slice(0, 100);
  return { messages, title, preview, unknown, error };
}
export function repeatedPrefix(previous: Message[], current: Message[]): number {
  if (!previous.length || current.length < previous.length) return 0;
  return previous.every((item, index) => JSON.stringify(item) === JSON.stringify(current[index]))
    ? previous.length
    : 0;
}

// Protocol fields are untrusted even when the encrypted event itself is authentic.
export function normalize(inputText: string, outputText: string, format = "json"): View {
  try {
    const view = normalizeUnsafe(inputText, outputText, format);
    const text = (value: any) =>
      typeof value === "string" ? value : value == null ? "" : JSON.stringify(value);
    view.messages = view.messages.map((m) => ({
      ...m,
      choice:typeof m.choice==="number"&&Number.isInteger(m.choice)&&m.choice>=0?m.choice:undefined,
      role: ["user", "assistant", "system", "developer", "tool"].includes(m.role)
        ? m.role
        : "unknown",
      text: text(m.text),
      thinking: text(m.thinking),
      tools: text(m.tools),
      attachments: m.attachments?.map((a) => ({
        ...a,
        name: typeof a.name === "string" ? a.name.slice(0, 512) : "附件",
        type: typeof a.type === "string" ? a.type : "application/octet-stream",
        data: typeof a.data === "string" ? a.data : undefined,
        url: typeof a.url === "string" ? a.url : undefined,
      })),
    }));
    return view;
  } catch {
    return {
      messages: [],
      title: "未识别格式",
      preview: "请查看技术详情或导出原始归档",
      unknown: true,
      error: "",
    };
  }
}
