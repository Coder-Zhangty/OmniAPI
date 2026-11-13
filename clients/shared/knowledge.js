// Portable search index created on desktop. All files/chunks are encrypted by sync.
// Mobile retrieval is local; embedding calls go only to an explicitly chosen provider.
export function validateKnowledgeIndex(index) {
  if (!index || index.v !== 1 || typeof index.id !== 'string' || !Array.isArray(index.chunks) || index.chunks.length > 50000) throw Error('INVALID_KNOWLEDGE_INDEX');
  const dimensions = index.dimensions;
  if (!Number.isInteger(dimensions) || dimensions < 1 || dimensions > 8192) throw Error('INVALID_DIMENSIONS');
  const seen = new Set();
  for (const chunk of index.chunks) {
    if (!chunk || typeof chunk.id !== 'string' || seen.has(chunk.id) || typeof chunk.text !== 'string' || chunk.text.length > 100000 || !Array.isArray(chunk.vector) || chunk.vector.length !== dimensions || !chunk.vector.every(Number.isFinite)) throw Error('INVALID_KNOWLEDGE_CHUNK');
    seen.add(chunk.id);
  }
  return index;
}
export function searchKnowledge(index, vector, limit = 5) {
  validateKnowledgeIndex(index);
  if (!Array.isArray(vector) || vector.length !== index.dimensions || !vector.every(Number.isFinite)) throw Error('QUERY_MODEL_MISMATCH');
  const queryNorm = Math.sqrt(vector.reduce((sum, v) => sum + v * v, 0));
  if (!queryNorm) return [];
  return index.chunks.map(chunk => {
    let dot = 0, norm = 0;
    for (let i = 0; i < vector.length; i++) { dot += vector[i] * chunk.vector[i]; norm += chunk.vector[i] * chunk.vector[i]; }
    return { id: chunk.id, text: chunk.text, source: chunk.source, score: norm ? dot / (queryNorm * Math.sqrt(norm)) : 0 };
  }).sort((a, b) => b.score - a.score).slice(0, Math.max(1, Math.min(limit, 20)));
}
export function requireToolApproval(tool, authorization) {
  // Unknown classifications fail closed, including remote MCP tools without hints.
  const safe = tool.classification === 'read-only' && tool.withinGrantedScope === true;
  if (!safe && authorization?.operationId !== tool.operationId) throw Error('TOOL_CONFIRMATION_REQUIRED');
  if (!safe && authorization?.consumed) throw Error('TOOL_AUTHORIZATION_CONSUMED');
  if (!safe) authorization.consumed = true;
}
