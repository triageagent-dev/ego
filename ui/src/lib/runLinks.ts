// The online evaluation records each hour's scores of a (tenant, kind) as a run named "online · <tenant> · <kind>"
// (the tenant left out when there is none). The runs page groups runs by that name, so a link to the group lists the
// hourly runs of one scope, and a click on one shows how it ran.
export function onlineRunsLink(tenant: string, kind: string): string {
  const name = tenant ? `online · ${tenant} · ${kind}` : `online · ${kind}`;
  return `#runs?group=${encodeURIComponent(`set:${name}`)}`;
}
