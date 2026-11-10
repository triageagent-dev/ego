// Which response answers the call at `index`. The id decides whenever there is one: an agent that runs the same
// tool twice in a turn (two memory searches, say) must not have both calls shown with the first result. A response
// without an id falls back to its position, then to the tool name, the way internal/vertexeval pairs them; a
// response whose id belongs to another call of the turn is never borrowed.
export function responseFor<R extends { name: string; id?: string }>(
  calls: ReadonlyArray<{ name: string; id?: string }>,
  responses: ReadonlyArray<R> | undefined,
  index: number,
): R | undefined {
  if (!responses?.length) return undefined;
  const call = calls[index];
  if (call.id) {
    const byId = responses.find((r) => r.id === call.id);
    if (byId) return byId;
  }
  const claimed = new Set(calls.map((c) => c.id).filter((id): id is string => !!id));
  const free = (r: R) => !r.id || !claimed.has(r.id);
  const atIndex = responses[index];
  if (atIndex && free(atIndex) && atIndex.name === call.name) return atIndex;
  return responses.find((r) => free(r) && r.name === call.name);
}
